// Package artists 是“画师表”：谁是画师、按平台分文件、键是改名不变的账号数字 ID（规格-画师原图）。
// 表只回答“这个账号是不是用户确认过的画师”和“按需发现了哪些待确认”；发送行为在 linkexpand、linkplugin，
// 这里不依赖它们，也不发任何请求。匹配只按 ID：handle 和名字只存着给人看。
package artists

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sumika-native/internal/fileatomic"
)

// 一行的状态：confirmed 生效；pending 待确认（按需发现，不生效）；declined 不是画师（用户拒绝，或判断后“不像”）
const (
	StatusConfirmed = "confirmed"
	StatusPending   = "pending"
	StatusDeclined  = "declined"
)

// 一行的来源：import 导入的确认结果；auto 按需判断；manual 用 sumika-ctl 手动改的
const (
	SourceImport = "import"
	SourceAuto   = "auto"
	SourceManual = "manual"
)

// 表的上限
const (
	// MaxEntries 是一个平台文件的行数上限；超了拒绝新增并写日志
	MaxEntries = 20000
	// MaxPending 是待确认行数的上限；满了按需判断只落 declined
	MaxPending = 500
	// MaxIDsPerCommand 是一条命令最多处理几个 ID
	MaxIDsPerCommand = 200

	fileVersion = 1
	textMax     = 64 // handle、名字存前各截这么多字
)

// 错误
var (
	ErrPlatform = errors.New("这个平台还没做")
	ErrBadID    = errors.New("账号 ID 的格式不对")
)

// Entry 是表里的一行
type Entry struct {
	Status string    `json:"status"`
	Source string    `json:"source"`
	Handle string    `json:"handle,omitempty"`
	Name   string    `json:"name,omitempty"`
	At     time.Time `json:"at"`
	// Batch 是最近一次把这一行改成现在状态的导入批次；之后被别的操作改动就清空
	Batch string `json:"batch,omitempty"`
	// Prev 是导入改动前的整行；原来没有这一行就没有 Prev（撤回时删行）
	Prev *Prev `json:"prev,omitempty"`
	// Score、Clues 是按需判断的分数和命中的线索名（只写线索表里的名字）
	Score float64  `json:"score,omitempty"`
	Clues []string `json:"clues,omitempty"`
}

// Prev 是导入改动前的整行（撤回时原样放回去）；它自己不带 Batch、Prev
type Prev Entry

// Row 是 List 给出的一行：ID 加内容
type Row struct {
	ID string
	Entry
}

type fileData struct {
	Version  int              `json:"version"`
	Platform string           `json:"platform"`
	Entries  map[string]Entry `json:"entries"`
}

// idPattern 按平台校验账号 ID；登记了的平台才做。没做的平台不写文件（ErrPlatform）。
// X 的数字 ID：1～20 位数字，不以 0 开头（也就排除了全 0）。
var idPattern = map[string]*regexp.Regexp{
	"x": regexp.MustCompile(`^[1-9][0-9]{0,19}$`),
}

// Platforms 是已经做了的平台
func Platforms() []string { return []string{"x"} }

// ValidID 说这个 ID 在这个平台上格式对不对；平台没做也算不对
func ValidID(platform, id string) bool {
	re := idPattern[platform]
	return re != nil && re.MatchString(id)
}

// Options 是表的可注入项
type Options struct {
	Now  func() time.Time
	Logf func(format string, args ...any) // 只写计数和原因，不写账号、名字、简介
	// Rename 代替 os.Rename（只给测试用：模拟改名失败）
	Rename func(oldpath, newpath string) error
}

// Book 是各平台的表；按平台懒打开
type Book struct {
	dir string
	opt Options
	mu  sync.Mutex
	by  map[string]*Store
}

// NewBook 建一个管 dir 下各平台文件的表集合（目录在第一次写时才建）
func NewBook(dir string, opt Options) *Book {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.Rename == nil {
		// Windows 上杀毒、索引会短暂占着刚写的文件，直接 os.Rename 偶发“拒绝访问”（2026-11-13 测试里连续写第 188 次撞上过）；
		// 和桌面设置一样只等这种短时冲突，最多 2 秒
		opt.Rename = func(from, to string) error { return fileatomic.Replace(from, to, 2*time.Second) }
	}
	return &Book{dir: dir, opt: opt, by: map[string]*Store{}}
}

// Platform 取这个平台的表；没做的平台回 ErrPlatform
func (b *Book) Platform(platform string) (*Store, error) {
	if idPattern[platform] == nil {
		return nil, ErrPlatform
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.by[platform]; ok {
		return s, nil
	}
	s := &Store{path: filepath.Join(b.dir, platform+".json"), platform: platform, opt: b.opt}
	s.load()
	b.by[platform] = s
	return s, nil
}

// Store 是一个平台的表：整份读进内存，每次改动整份写回（先写临时文件再改名）。
// 改动在副本上做、写成功才换上，写不成功内存和文件都不变。
type Store struct {
	path     string
	platform string
	opt      Options

	mu      sync.RWMutex
	entries map[string]Entry
	full    bool // 已经为“满了”写过日志
	// locked：磁盘上有读不开的坏文件又没法改名保留，不覆盖它，这一趟只读
	locked bool
}

// load 读文件；没有就是空表；坏了（JSON 不对、版本或平台对不上）改名成 .bad-<毫秒> 按空表走，不覆盖
func (s *Store) load() {
	s.entries = map[string]Entry{}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.opt.Logf("画师表：读不开（%s），本次按空表且不写盘", reasonOf(err))
			s.locked = true
		}
		return
	}
	var d fileData
	if err := json.Unmarshal(b, &d); err == nil && d.Version == fileVersion && d.Platform == s.platform && d.Entries != nil && validEntries(s.platform, d.Entries) {
		s.entries = d.Entries
		return
	}
	bad := fmt.Sprintf("%s.bad-%d", s.path, s.opt.Now().UnixMilli())
	if err := s.opt.Rename(s.path, bad); err != nil {
		s.opt.Logf("画师表：文件坏了，改名保留也失败（%s），本次按空表且不写盘", reasonOf(err))
		s.locked = true
		return
	}
	s.opt.Logf("画师表：文件坏了，已改名保留，本次按空表")
}

func validEntries(platform string, m map[string]Entry) bool {
	for id, e := range m {
		if !ValidID(platform, id) {
			return false
		}
		switch e.Status {
		case StatusConfirmed, StatusPending, StatusDeclined:
		default:
			return false
		}
	}
	return len(m) <= MaxEntries
}

func reasonOf(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error() // 不带路径
	}
	return "出错"
}

// errNoChange 让 mutate 的函数说“没有要改的”：不写盘、不算错
var errNoChange = errors.New("没有变化")

// mutate 在副本上改，写盘成功才换上；fn 返回 errNoChange 时不写盘、也不回错
func (s *Store) mutate(fn func(m map[string]Entry) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked {
		return errors.New("画师表文件读不开又没法改名保留，这次运行里不改它")
	}
	cp := make(map[string]Entry, len(s.entries)+8)
	for k, v := range s.entries {
		cp[k] = v
	}
	if err := fn(cp); err != nil {
		if errors.Is(err, errNoChange) {
			return nil
		}
		return err
	}
	if err := s.save(cp); err != nil {
		return err
	}
	s.entries = cp
	return nil
}

func (s *Store) save(m map[string]Entry) error {
	b, err := json.Marshal(fileData{Version: fileVersion, Platform: s.platform, Entries: m})
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	if err := s.opt.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Lookup 取一个账号的行；ID 格式不对、没有都回 false
func (s *Store) Lookup(id string) (Entry, bool) {
	if !ValidID(s.platform, id) {
		return Entry{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[id]
	return e, ok
}

// Confirmed 说这个账号是不是用户确认过的画师（只有 confirmed 算）
func (s *Store) Confirmed(id string) bool {
	e, ok := s.Lookup(id)
	return ok && e.Status == StatusConfirmed
}

// handleKey 是比较 @用户名 用的写法：去掉开头的 @、去空白、小写（X 的用户名不分大小写）
func handleKey(h string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(h), "@"))
}

// ConfirmedHandle 按 @用户名 找确认过的画师（ADR-372：vx 线路不给数字 ID 时的兜底）。
// 只认 confirmed 行；有两行确认过的画师存着同一个用户名（旧记录没来得及更新）时算对不上，宁可漏认也不认错人。
func (s *Store) ConfirmedHandle(handle string) (string, bool) {
	k := handleKey(handle)
	if k == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	found := ""
	for id, e := range s.entries {
		if e.Status != StatusConfirmed || handleKey(e.Handle) != k {
			continue
		}
		if found != "" {
			return "", false
		}
		found = id
	}
	return found, found != ""
}

// RefreshHandle 用一次带数字 ID 的结果更新表里存的 @用户名（ADR-372）：这个 ID 有行、用户名变了才写；
// 同时把别的行里还存着这个用户名的清掉——X 的用户名同一时间只属于一个人，旧记录留着会让兜底认错人。
// 不动状态、批次和撤回用的 Prev（用户名只是匹配用的附带信息）。改了返回 true。
func (s *Store) RefreshHandle(id, handle string) (bool, error) {
	h := cleanText(strings.TrimPrefix(strings.TrimSpace(handle), "@"), textMax)
	if !ValidID(s.platform, id) || h == "" {
		return false, nil
	}
	changed := false
	err := s.mutate(func(m map[string]Entry) error {
		e, ok := m[id]
		if !ok {
			return errNoChange
		}
		k := handleKey(h)
		if handleKey(e.Handle) != k {
			e.Handle = h
			m[id] = e
			changed = true
		}
		for oid, o := range m {
			if oid != id && handleKey(o.Handle) == k {
				o.Handle = ""
				m[oid] = o
				changed = true
			}
		}
		if !changed {
			return errNoChange
		}
		return nil
	})
	return changed && err == nil, err
}

// Counts 是各状态的行数
func (s *Store) Counts() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]int{}
	for _, e := range s.entries {
		out[e.Status]++
	}
	return out
}

// List 列出一个状态的行（status 为 "all" 列全部），最近变化的在前；limit 为 0 不限
func (s *Store) List(status string, limit int) []Row {
	s.mu.RLock()
	rows := make([]Row, 0, len(s.entries))
	for id, e := range s.entries {
		if status == "all" || e.Status == status {
			rows = append(rows, Row{ID: id, Entry: e})
		}
	}
	s.mu.RUnlock()
	slices.SortFunc(rows, func(a, b Row) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// cleanText 去掉控制字符、换行和不可见的方向、零宽字符，再截到 n 个字
func cleanText(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0, r >= 0x200b && r <= 0x200f, r >= 0x2028 && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0xfeff, r == 0xfffc, r == utf8.RuneError:
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > n {
		out = string([]rune(out)[:n])
	}
	return out
}

// AddAuto 记录按需判断的结果：表里已有这一行就什么都不做（同一账号只判断一次）。
// 总分够线且待确认没满记 pending，否则记 declined；表满了不记。返回记下的状态，没记返回 ""。
func (s *Store) AddAuto(id, handle, name string, j Judgement) (string, error) {
	if !ValidID(s.platform, id) {
		return "", ErrBadID
	}
	status := ""
	err := s.mutate(func(m map[string]Entry) error {
		if _, ok := m[id]; ok {
			return errNoChange
		}
		if len(m) >= MaxEntries {
			if !s.full {
				s.full = true
				s.opt.Logf("画师表：满了（%d 行），不再新增", MaxEntries)
			}
			return errNoChange
		}
		e := Entry{Source: SourceAuto, Handle: cleanText(handle, textMax), Name: cleanText(name, textMax), At: s.opt.Now().UTC()}
		pending := 0
		for _, x := range m {
			if x.Status == StatusPending {
				pending++
			}
		}
		if j.Score >= rules.Thresholds.AutoPending && pending < MaxPending {
			e.Status, e.Score, e.Clues = StatusPending, j.Score, slices.Clone(j.Clues)
		} else {
			e.Status = StatusDeclined
			if j.Score >= rules.Thresholds.AutoPending {
				s.opt.Logf("画师表：待确认满了（%d 行），按需发现的先记成不是", MaxPending)
			}
		}
		status = e.Status
		m[id] = e
		return nil
	})
	return status, err
}

// ConfirmReport、RemoveReport 是 Confirm、Remove 的结果
type ConfirmReport struct{ Confirmed, Already int }
type RemoveReport struct{ Removed, Already int }

// checkIDs 查一批 ID：个数、格式、去重。出错整批不动。
func (s *Store) checkIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, errors.New("没有写账号 ID")
	}
	if len(ids) > MaxIDsPerCommand {
		return nil, fmt.Errorf("一次最多 %d 个账号", MaxIDsPerCommand)
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if !ValidID(s.platform, id) {
			return nil, fmt.Errorf("%w：%q", ErrBadID, cleanText(id, 24))
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// Confirm 把待确认或“不是”的账号改成生效（来源记 manual）。每个 ID 都要在表里，不凭空新增；有一个不对整批不动。
func (s *Store) Confirm(ids []string) (ConfirmReport, error) {
	ids, err := s.checkIDs(ids)
	if err != nil {
		return ConfirmReport{}, err
	}
	var rep ConfirmReport
	err = s.mutate(func(m map[string]Entry) error {
		rep = ConfirmReport{}
		for _, id := range ids {
			e, ok := m[id]
			if !ok {
				return fmt.Errorf("表里没有这个账号（%s），先导入或等它被发现", id)
			}
			if e.Status == StatusConfirmed {
				rep.Already++
				continue
			}
			e.Status, e.Source, e.Batch, e.Prev, e.At = StatusConfirmed, SourceManual, "", nil, s.opt.Now().UTC()
			m[id] = e
			rep.Confirmed++
		}
		if rep.Confirmed == 0 {
			return errNoChange
		}
		return nil
	})
	return rep, err
}

// Remove 把生效或待确认的账号改成“不是”（来源记 manual，以后不再提）。每个 ID 都要在表里；有一个不对整批不动。
func (s *Store) Remove(ids []string) (RemoveReport, error) {
	ids, err := s.checkIDs(ids)
	if err != nil {
		return RemoveReport{}, err
	}
	var rep RemoveReport
	err = s.mutate(func(m map[string]Entry) error {
		rep = RemoveReport{}
		for _, id := range ids {
			e, ok := m[id]
			if !ok {
				return fmt.Errorf("表里没有这个账号（%s）", id)
			}
			if e.Status == StatusDeclined {
				rep.Already++
				continue
			}
			e.Status, e.Source, e.Batch, e.Prev, e.At = StatusDeclined, SourceManual, "", nil, s.opt.Now().UTC()
			e.Score, e.Clues = 0, nil
			m[id] = e
			rep.Removed++
		}
		if rep.Removed == 0 {
			return errNoChange
		}
		return nil
	})
	return rep, err
}
