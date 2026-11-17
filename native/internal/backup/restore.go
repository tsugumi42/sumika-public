package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"sumika-native/internal/paths"
)

// 恢复成新目录（规格-数据目录与备份 10.10）：把一份校验通过的备份原样放进一个不存在或为空的新位置，逐文件按清单核对，
// 再把每个库打开跑 integrity_check、数各表行数和清单比。只读备份、只复制不建硬链接、不碰现在的数据目录、不改配置、不切换。

var (
	// ErrBadBackupName：备份名写得不对（空、带路径分隔符或 ..）
	ErrBadBackupName = errors.New("备份名不对")
	// ErrBackupNotFound：备份目录里没有这个名字的备份
	ErrBackupNotFound = errors.New("没有这份备份")
	// ErrBackupUnusable：有这份备份，但还在做、没做完或没通过校验，不能拿来恢复
	ErrBackupUnusable = errors.New("这份备份不能用来恢复")
	// ErrManifestUnsafe：清单里的东西不可信（路径逃出目录、重复、哈希格式不对……），一个文件都没写就拒绝
	ErrManifestUnsafe = errors.New("备份清单不可信")
	// ErrTargetUnsafe：恢复的目标目录不合适（规格 10.10.3）
	ErrTargetUnsafe = errors.New("恢复目标不合适")
	// ErrNoSpace：目标盘剩余空间不够
	ErrNoSpace = errors.New("目标盘剩余空间不够")
	// ErrRestoreFailed：恢复开始了，但复制或核对没通过；已写出的留着，目录里的未完成标记没删
	ErrRestoreFailed = errors.New("恢复没有通过核对")
)

// 恢复记录里的文件名（都在 paths.RestoreDir 下）
const (
	restoreReportFile = "report.json"
	drillMarkerFile   = "drill.json"
)

// 恢复的目的：恢复成新目录，还是抽验（恢复到临时目录再删）
const (
	PurposeRestore = "restore"
	PurposeDrill   = "drill"
)

// RowsState 是一个库的行数核对结果
type RowsState string

const (
	RowsMatch      RowsState = "match"      // 各表行数和清单一致
	RowsMismatch   RowsState = "mismatch"   // 有表对不上
	RowsUnrecorded RowsState = "unrecorded" // 这份备份没记行数（旧备份）：只核对了文件和完整性
)

// DBCheck 是一个库的核对结果
type DBCheck struct {
	Path      string    `json:"path"`
	Integrity string    `json:"integrity"` // integrity_check 的结果，"ok" 才算过
	Rows      RowsState `json:"rows"`
	Tables    int       `json:"tables"`
	Mismatch  []string  `json:"mismatch,omitempty"`
}

// RestoreReport 是一次恢复的核对报告（写在新目录的 .restore/report.json，命令行也打印）
type RestoreReport struct {
	Program      string    `json:"program"`
	Purpose      string    `json:"purpose"`
	Backup       string    `json:"backup"`
	BackupTime   time.Time `json:"backupTime"`
	BackupSource string    `json:"backupSource,omitempty"`
	OtherSource  bool      `json:"otherSource,omitempty"` // 这份备份属于别的数据目录
	Target       string    `json:"target"`
	StartedAt    time.Time `json:"startedAt"`
	DurationMs   int64     `json:"durationMs"`
	Files        int       `json:"files"`
	Bytes        int64     `json:"bytes"`
	DBs          []DBCheck `json:"dbs"`
	RowsRecorded bool      `json:"rowsRecorded"` // 清单里每个库都记了行数
	OK           bool      `json:"ok"`
	Problems     []string  `json:"problems,omitempty"`
	Notes        []string  `json:"notes,omitempty"`
}

// RestoreOutcome 是后台做完的一次恢复
type RestoreOutcome struct {
	Report RestoreReport
	Err    error
}

// incompleteMarker 是 .restore/incomplete.json 的内容
type incompleteMarker struct {
	Program string    `json:"program"`
	State   string    `json:"state"` // restoring | failed | cancelled
	Backup  string    `json:"backup"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason,omitempty"`
}

var sha256Re = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// acquire 拿备份、恢复、抽验共用的那把锁（拿不到回 ErrBusy，不排队），并把取消函数挂进 runCtx，关机时 Close 能取消它。
// 不设 state.Running：自检 S9 不能把恢复当成“正在备份”
func (s *Service) acquire(parent context.Context) (context.Context, func(), error) {
	if s.closed.Load() {
		return nil, nil, ErrClosed
	}
	if !s.run.TryLock() {
		return nil, nil, fmt.Errorf("%w（正在做备份、恢复或抽验）", ErrBusy)
	}
	ctx, cancel := context.WithTimeout(parent, s.opt.RunMax)
	s.life.Lock()
	s.runCtx = cancel
	if s.closed.Load() {
		cancel()
	}
	s.life.Unlock()
	return ctx, func() {
		s.life.Lock()
		s.runCtx = nil
		s.life.Unlock()
		cancel()
		s.run.Unlock()
	}, nil
}

// Restore 把名为 name 的备份恢复成新目录 to（同步；要后台做用 GoRestore）
func (s *Service) Restore(ctx context.Context, name, to string) (RestoreReport, error) {
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return RestoreReport{}, err
	}
	defer release()
	return s.restoreLocked(ctx, name, to)
}

// GoRestore 在后台做一次恢复，立刻返回：锁被占着返回 ErrBusy。恢复不跟调用方的 context 走（命令行断了它也该做完），关机由 Close 取消
func (s *Service) GoRestore(name, to string) (<-chan RestoreOutcome, error) {
	ctx, release, err := s.acquire(context.Background())
	if err != nil {
		return nil, err
	}
	ch := make(chan RestoreOutcome, 1)
	go func() {
		defer release()
		var out RestoreOutcome
		func() {
			defer func() {
				if r := recover(); r != nil {
					out.Err = fmt.Errorf("恢复时出了意外：%v", r)
				}
			}()
			out.Report, out.Err = s.restoreLocked(ctx, name, to)
		}()
		ch <- out
	}()
	return ch, nil
}

func (s *Service) restoreLocked(ctx context.Context, name, to string) (RestoreReport, error) {
	dir, err := s.Dir()
	if err != nil {
		return RestoreReport{}, err
	}
	entry, m, err := s.openBackupFor(dir, name)
	if err != nil {
		return RestoreReport{}, err
	}
	root, existed, err := s.checkRestoreTarget(dir, to)
	if err != nil {
		return RestoreReport{}, err
	}
	if err := s.checkRestoreRoom(root, m); err != nil {
		return RestoreReport{}, err
	}
	return s.restoreCore(ctx, entry, m, restoreOpts{purpose: PurposeRestore, root: root, existed: existed})
}

// openBackupFor 按名字找到一份能用来恢复的备份并读出清单、检查清单里的每一条。名字只和 Scan 列出来的逐个比，不拿去拼路径。
// 任何一条不过都还没写任何东西
func (s *Service) openBackupFor(dir, name string) (Entry, Manifest, error) {
	if strings.TrimSpace(name) == "" {
		return Entry{}, Manifest{}, fmt.Errorf("%w：没写备份名", ErrBadBackupName)
	}
	if strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") || filepath.IsAbs(name) {
		return Entry{}, Manifest{}, fmt.Errorf("%w：%q 不是一个备份名（只写 backup list 里的名字，不带路径）", ErrBadBackupName, ctlSafe(name))
	}
	entries, err := Scan(dir)
	if err != nil {
		return Entry{}, Manifest{}, fmt.Errorf("读不了备份目录：%w", err)
	}
	var found, partial *Entry
	for i := range entries {
		if lower(entries[i].Name) != lower(name) {
			continue
		}
		if entries[i].Partial {
			partial = &entries[i]
		} else {
			found = &entries[i]
		}
	}
	if found == nil && partial != nil {
		return Entry{}, Manifest{}, fmt.Errorf("%w：%s 还在做或没做完", ErrBackupUnusable, partial.Name)
	}
	if found == nil {
		var have []string
		for _, e := range entries {
			if len(have) < 5 {
				have = append(have, e.Name)
			}
		}
		hint := "备份目录里一份都没有"
		if len(have) > 0 {
			hint = "现有的有：" + strings.Join(have, "、") + "……（sumika-ctl backup list 看全部）"
		}
		return Entry{}, Manifest{}, fmt.Errorf("%w：%q。%s", ErrBackupNotFound, ctlSafe(name), hint)
	}
	if found.Manifest.Status != StatusOK {
		return Entry{}, Manifest{}, fmt.Errorf("%w：%s 的状态是 %s（%s）", ErrBackupUnusable, found.Name, found.Manifest.Status, strings.Join(found.Manifest.Problems, "；"))
	}
	m, err := ReadManifest(found.Folder)
	if err != nil {
		return Entry{}, Manifest{}, fmt.Errorf("%w：读不了 %s 的清单：%v", ErrBackupUnusable, found.Name, err)
	}
	if err := validateManifest(m); err != nil {
		return Entry{}, Manifest{}, err
	}
	data := filepath.Join(found.Folder, "data")
	if fi, err := os.Lstat(data); err != nil || !fi.IsDir() || isLinkEntry(data) {
		return Entry{}, Manifest{}, fmt.Errorf("%w：%s 里的 data 不是普通目录（缺了、是文件或是联接）", ErrBackupUnusable, found.Name)
	}
	return *found, m, nil
}

// ctlSafe 把读来的、带控制字符的文字压成一行能放进错误信息的样子（不依赖 ctlproto：备份包是叶子包）
func ctlSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

var winReserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

// reservedName：Windows 保留的设备名（CON、NUL、COM1…，带不带扩展名都算）。清单里的路径所有系统都按这一条查：以后会在 Windows 上被用到
func reservedName(comp string) bool {
	base := strings.ToUpper(comp)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, " ")
	if winReserved[base] {
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

// validRel 查清单里的一个相对路径：不是相对的、会逃出目录、带 :（NTFS 备用数据流）、不规整、分量以点或空格结尾、是保留名、碰 .restore 的都拒绝
func validRel(rel string) error {
	switch {
	case rel == "":
		return errors.New("路径是空的")
	case strings.ContainsAny(rel, `\:`):
		return errors.New("路径里有反斜杠或冒号")
	case strings.IndexFunc(rel, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return errors.New("路径里有控制字符")
	case strings.HasPrefix(rel, "/") || path.IsAbs(rel):
		return errors.New("路径是绝对的")
	case path.Clean(rel) != rel:
		return errors.New("路径不规整（有 ..、. 或重复的斜杠）")
	case !filepath.IsLocal(filepath.FromSlash(rel)):
		return errors.New("路径会逃出目录")
	}
	for _, comp := range strings.Split(rel, "/") {
		if strings.HasSuffix(comp, ".") || strings.HasSuffix(comp, " ") {
			return errors.New("路径里有以点或空格结尾的名字")
		}
		if reservedName(comp) {
			return errors.New("路径里有 Windows 保留的设备名")
		}
	}
	if strings.EqualFold(strings.Split(rel, "/")[0], paths.RestoreDir) {
		return errors.New("路径落在恢复记录目录里")
	}
	return nil
}

// validateManifest 查清单里的每一条（规格 10.10.1 第 3 条）：路径、大小、哈希格式、重复、库都在文件列表里
func validateManifest(m Manifest) error {
	if len(m.Files) == 0 {
		return fmt.Errorf("%w：清单里没有任何文件", ErrManifestUnsafe)
	}
	seen := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		if err := validRel(f.Path); err != nil {
			return fmt.Errorf("%w：%q：%v", ErrManifestUnsafe, ctlSafe(f.Path), err)
		}
		if f.Size < 0 || !sha256Re.MatchString(f.SHA256) {
			return fmt.Errorf("%w：%s 的大小或哈希格式不对", ErrManifestUnsafe, f.Path)
		}
		k := strings.ToLower(f.Path) // 恢复出的目录以后可能搬到不分大小写的系统上：折叠后重名的也拒绝
		if seen[k] {
			return fmt.Errorf("%w：%s 在清单里重复（或只有大小写不同）", ErrManifestUnsafe, f.Path)
		}
		seen[k] = true
	}
	for _, d := range m.DBs {
		if !seen[strings.ToLower(d.Path)] {
			return fmt.Errorf("%w：清单里的库 %s 不在文件列表里", ErrManifestUnsafe, d.Path)
		}
	}
	return nil
}

// checkRestoreTarget 查恢复的目标目录（规格 10.10.3；恢复和抽验同一个函数）。返回解到真实路径的目标和它是不是已经存在（空目录）。
// 先位置、后空不空：测位置的规则不能靠“目录非空”碰巧拦住
func (s *Service) checkRestoreTarget(backupDir, to string) (root string, existed bool, err error) {
	bad := func(format string, args ...any) (string, bool, error) {
		return "", false, fmt.Errorf("%w：%s", ErrTargetUnsafe, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(to) == "" {
		to = ""
	}
	to = paths.StripDevicePrefix(to) // 不去掉首尾空白：Windows 会悄悄去掉末尾的空格，写了就当错
	switch {
	case to == "":
		return bad("没写目标目录（--to 要写一个绝对路径）")
	case !filepath.IsAbs(to):
		return bad("%q 不是绝对路径（命令行的相对路径按谁的工作目录算不清，请写绝对路径）", ctlSafe(to))
	case strings.HasPrefix(to, `\\`) || strings.HasPrefix(to, "//"):
		return bad("%q 是网络路径或设备路径：SQLite 库放在网络盘上容易坏，而且解析它会真的连过去；请用本机的盘", ctlSafe(to))
	}
	clean := filepath.Clean(to)
	if runtime.GOOS == "windows" {
		rest := clean[len(filepath.VolumeName(clean)):]
		for _, comp := range strings.Split(rest, `\`) {
			switch {
			case comp == "":
			case strings.ContainsRune(comp, ':'):
				return bad("路径里有冒号：%q", ctlSafe(comp))
			case strings.HasSuffix(comp, ".") || strings.HasSuffix(comp, " "):
				return bad("路径里有以点或空格结尾的名字：%q（Windows 会悄悄去掉它们）", ctlSafe(comp))
			case reservedName(comp):
				return bad("路径里有 Windows 保留的设备名：%q", ctlSafe(comp))
			}
		}
	}
	real, err := realExisting(clean)
	if err != nil {
		return bad("%s 解不开：%v", ctlSafe(clean), err)
	}
	dataReal, err := realExisting(s.opt.Paths.Root)
	if err != nil {
		return bad("数据目录解不开：%v", err)
	}
	backupReal, err := realExisting(backupDir)
	if err != nil {
		return bad("备份目录解不开：%v", err)
	}
	if msg := positionProblem(dataReal, backupReal, real); msg != "" {
		return bad("%s", msg)
	}
	fi, err := os.Lstat(clean)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return real, false, nil
	case err != nil:
		return bad("%s 查不了：%v", ctlSafe(clean), err)
	case isLinkEntry(clean):
		return bad("%s 是联接或符号链接（哪怕它链到一个空目录也不行）", ctlSafe(clean))
	case !fi.IsDir():
		return bad("%s 已经存在，是个文件", ctlSafe(clean))
	}
	ents, err := os.ReadDir(clean)
	if err != nil {
		return bad("%s 读不了：%v", ctlSafe(clean), err)
	}
	if len(ents) > 0 {
		return bad("%s 不是空的（里面已经有 %d 项）；写一个还不存在或为空的目录", ctlSafe(clean), len(ents))
	}
	return real, true, nil
}

// pathUnits 是路径占几个 UTF-16 单位：Windows 的路径上限按这个数（一个中文字算 1，不是 UTF-8 的 3 个字节）
func pathUnits(s string) int { return len(utf16.Encode([]rune(s))) }

// positionProblem：real（已经解到真实路径）和数据目录、备份目录的位置关系对不对；对就返回空串
func positionProblem(dataReal, backupReal, real string) string {
	switch {
	case filepath.Dir(real) == real:
		return fmt.Sprintf("%s 是盘符根", real)
	case within(dataReal, real):
		return fmt.Sprintf("%s 就是数据目录，或在数据目录 %s 里面（覆盖现有数据的恢复不做，只能恢复成新目录）", real, dataReal)
	case within(real, dataReal):
		return fmt.Sprintf("%s 是数据目录 %s 的上级", real, dataReal)
	case within(backupReal, real):
		return fmt.Sprintf("%s 就是备份目录，或在备份目录 %s 里面", real, backupReal)
	case within(real, backupReal):
		return fmt.Sprintf("%s 是备份目录 %s 的上级", real, backupReal)
	}
	return ""
}

// recheckRoot 在目标目录建好以后、写第一个标记以前再核对一遍：检查到建立之间有人把某一级换成联接，
// 解开以后就不是原来那个位置（或落进了数据目录、备份目录里），这时一个字都不写
func (s *Service) recheckRoot(root string) error {
	backupDir, err := s.Dir()
	if err != nil {
		return err
	}
	real, err := realExisting(root)
	if err != nil {
		return fmt.Errorf("%w：建好以后解不开 %s：%v", ErrTargetUnsafe, root, err)
	}
	if lower(filepath.Clean(real)) != lower(filepath.Clean(root)) {
		return fmt.Errorf("%w：检查到建立之间目标变了（%s 现在指向 %s）", ErrTargetUnsafe, root, real)
	}
	dataReal, err := realExisting(s.opt.Paths.Root)
	if err != nil {
		return fmt.Errorf("%w：数据目录解不开：%v", ErrTargetUnsafe, err)
	}
	backupReal, err := realExisting(backupDir)
	if err != nil {
		return fmt.Errorf("%w：备份目录解不开：%v", ErrTargetUnsafe, err)
	}
	if msg := positionProblem(dataReal, backupReal, real); msg != "" {
		return fmt.Errorf("%w：%s", ErrTargetUnsafe, msg)
	}
	return nil
}

// checkRestoreRoom 开工前查路径长度和目标盘的剩余空间
func (s *Service) checkRestoreRoom(root string, m Manifest) error {
	limit := s.maxPathLen
	var total int64
	for _, f := range m.Files {
		total += f.Size
		if n := pathUnits(root) + 1 + pathUnits(f.Path); limit > 0 && n > limit {
			return fmt.Errorf("%w：%s 加上目标目录 %s 一共 %d 个字符，超过 %d（Windows 路径上限）；换一个更短的目标目录", ErrTargetUnsafe, f.Path, root, n, limit)
		}
	}
	need := total + total/10 + 64<<20
	if free, ok := s.free(nearestExisting(root)); ok && free < need {
		return fmt.Errorf("%w：剩 %.1f GB，要 %.1f GB（备份共 %.1f GB 的 1.1 倍再加 64 MB）；先腾地方或换一个盘", ErrNoSpace, float64(free)/(1<<30), float64(need)/(1<<30), float64(total)/(1<<30))
	}
	return nil
}

// restoreOpts 是 restoreCore 的参数：root 是已经通过 checkRestoreTarget 的目标（真实路径）
type restoreOpts struct {
	purpose string
	root    string
	existed bool // 目标已经是个空目录（不用建）
}

func (s *Service) markerPath(root string) string {
	return filepath.Join(root, paths.RestoreDir, paths.RestoreIncompleteFile)
}

// writeJSONFile 先写同目录临时文件、落盘、再改名：写坏一半不会留下读不懂的文件
func writeJSONFile(file string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := file + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	serr := syncFile(f)
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// restoreCore 做恢复本身：写标记 → 按清单复制 → 独立核对 → 写报告 → 核对通过才删标记。调用方已经拿了锁、查过备份和目标
func (s *Service) restoreCore(ctx context.Context, e Entry, m Manifest, o restoreOpts) (RestoreReport, error) {
	started := time.Now()
	rep := RestoreReport{Program: Program, Purpose: o.purpose, Backup: m.Name, BackupTime: m.Time, BackupSource: m.Source, Target: o.root, StartedAt: s.opt.Now(), DBs: []DBCheck{}}
	if m.Source != s.source() {
		rep.OtherSource = true
		rep.Notes = append(rep.Notes, "这份备份属于别的数据目录（"+ctlSafe(m.Source)+"）：换电脑恢复时是正常的；如果不是换电脑，确认一下是不是拿错了备份")
	}
	marker := func(state, reason string) {
		_ = writeJSONFile(s.markerPath(o.root), incompleteMarker{Program: Program, State: state, Backup: m.Name, At: s.opt.Now(), Reason: reason})
	}
	finish := func(problems []string, state string) (RestoreReport, error) {
		rep.DurationMs = time.Since(started).Milliseconds()
		rep.Problems = problems
		rep.OK = len(problems) == 0
		if rep.OK {
			if err := writeJSONFile(filepath.Join(o.root, paths.RestoreDir, restoreReportFile), rep); err != nil {
				rep.OK = false
				rep.Problems = append(rep.Problems, "核对通过了，但写不了核对报告："+err.Error())
				marker("failed", rep.Problems[0])
				return rep, fmt.Errorf("%w：%s", ErrRestoreFailed, rep.Problems[0])
			}
			if err := os.Remove(s.markerPath(o.root)); err != nil && !errors.Is(err, os.ErrNotExist) {
				rep.OK = false
				rep.Problems = append(rep.Problems, "核对通过了，但删不掉未完成标记："+err.Error())
				return rep, fmt.Errorf("%w：%s", ErrRestoreFailed, rep.Problems[0])
			}
			return rep, nil
		}
		marker(state, problems[0])
		_ = writeJSONFile(filepath.Join(o.root, paths.RestoreDir, restoreReportFile), rep)
		if state == "cancelled" {
			return rep, fmt.Errorf("恢复被取消：%w", ctx.Err())
		}
		return rep, fmt.Errorf("%w：%s", ErrRestoreFailed, problems[0])
	}

	if !o.existed {
		if err := os.MkdirAll(o.root, 0o755); err != nil {
			return rep, fmt.Errorf("%w：建不了目标目录 %s：%v", ErrTargetUnsafe, o.root, err)
		}
	}
	if err := s.recheckRoot(o.root); err != nil {
		return rep, err
	}
	// 标记第一个写：之后不管是哪一步断了（取消、盘满、进程被杀、断电），这个目录都不会被当成能用的数据目录
	if err := writeJSONFile(s.markerPath(o.root), incompleteMarker{Program: Program, State: "restoring", Backup: m.Name, At: rep.StartedAt}); err != nil {
		return rep, fmt.Errorf("目标目录写不进东西：%w", err)
	}

	srcRoot := filepath.Join(e.Folder, "data")
	files := append([]FileEntry(nil), m.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	checked := map[string]bool{srcRoot: true} // 查过不是联接的源目录
	for _, f := range files {
		if ctx.Err() != nil {
			return finish([]string{"恢复被取消，已写出的留在 " + o.root}, "cancelled")
		}
		src := filepath.Join(srcRoot, filepath.FromSlash(f.Path))
		if err := checkSource(srcRoot, src, f, checked); err != nil {
			return finish([]string{err.Error()}, "failed")
		}
		dst := filepath.Join(o.root, filepath.FromSlash(f.Path))
		n, sum, err := s.restoreCopy(ctx, src, dst)
		if err != nil {
			if ctx.Err() != nil {
				return finish([]string{"恢复被取消，已写出的留在 " + o.root}, "cancelled")
			}
			return finish([]string{fmt.Sprintf("复制 %s 失败：%v", f.Path, err)}, "failed")
		}
		if n != f.Size || !strings.EqualFold(sum, f.SHA256) {
			_ = os.Remove(dst)
			return finish([]string{fmt.Sprintf("%s 和清单对不上：清单写大小 %d、哈希 %s，备份里实际大小 %d、哈希 %s（这份备份可能已经坏了，这个文件没有写出）", f.Path, f.Size, short(f.SHA256), n, short(sum))}, "failed")
		}
	}
	if n := countExtra(ctx, srcRoot, m); n > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("备份文件夹里有 %d 个清单之外的文件，没有恢复（恢复只按清单）", n))
	}
	problems := s.verifyRestored(ctx, o.root, m, &rep)
	if ctx.Err() != nil {
		return finish([]string{"核对被取消，已写出的留在 " + o.root}, "cancelled")
	}
	return finish(problems, "failed")
}

// countExtra 数备份文件夹的 data/ 里清单之外的文件（只数、不跟进联接）
func countExtra(ctx context.Context, srcRoot string, m Manifest) int {
	want := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		want[strings.ToLower(f.Path)] = true
	}
	n := 0
	_ = filepath.WalkDir(srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(srcRoot, p)
		if rerr == nil && !want[strings.ToLower(filepath.ToSlash(rel))] {
			n++
		}
		return nil
	})
	return n
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// checkSource 查备份里的一个源文件：路径上每一级目录不是联接（查过的缓存起来），文件本身是普通文件、不是联接、大小和清单一致
func checkSource(srcRoot, src string, f FileEntry, checked map[string]bool) error {
	var chain []string
	for d := filepath.Dir(src); d != srcRoot && !checked[d]; d = filepath.Dir(d) {
		chain = append(chain, d)
		if len(chain) > 64 {
			return fmt.Errorf("%s 的路径太深", f.Path)
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		fi, err := os.Lstat(chain[i])
		if err != nil {
			return fmt.Errorf("备份里缺了 %s（清单里有，备份里没有）：%v", f.Path, err)
		}
		if !fi.IsDir() || isLinkEntry(chain[i]) {
			return fmt.Errorf("备份里 %s 的上级目录不是普通目录（是联接或文件），不跟进去", f.Path)
		}
		checked[chain[i]] = true
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("备份里缺了 %s（清单里有，备份里没有）：%v", f.Path, err)
	}
	if !fi.Mode().IsRegular() || linkOnDisk(src) {
		return fmt.Errorf("备份里的 %s 不是普通文件（是联接或目录），不跟进去", f.Path)
	}
	if fi.Size() != f.Size {
		return fmt.Errorf("%s 的大小和清单对不上：清单写 %d，备份里是 %d", f.Path, f.Size, fi.Size())
	}
	return nil
}

// copyHashed 复制 src 到 dst（目标不许已经存在），边读边算源端的 sha256，落盘后才返回。写出的东西读回来核对放在 verifyRestored 里，
// 不在这里再读一遍（一份大备份读两遍是白费的）
func copyHashed(ctx context.Context, src, dst string) (int64, string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, cerr := io.Copy(io.MultiWriter(out, h), ctxReader{ctx, in})
	serr := syncFile(out)
	clerr := out.Close()
	if err := errors.Join(cerr, serr, clerr); err != nil {
		_ = os.Remove(dst)
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// verifyRestored 从目标目录重新读一遍核对（独立于复制那一遍）：清单里每个文件都在、是普通文件、大小和哈希都对；目标里没有清单之外的文件；
// 每个库打开跑 integrity_check、各表行数和清单比。返回问题清单（最多 maxProblems 条），其余写进 rep
func (s *Service) verifyRestored(ctx context.Context, root string, m Manifest, rep *RestoreReport) []string {
	var problems []string
	add := func(format string, args ...any) {
		if len(problems) < maxProblems {
			problems = append(problems, fmt.Sprintf(format, args...))
		}
	}
	want := make(map[string]bool, len(m.Files))
	for _, f := range m.Files {
		want[strings.ToLower(f.Path)] = true
		if ctx.Err() != nil {
			return problems
		}
		dst := filepath.Join(root, filepath.FromSlash(f.Path))
		fi, err := os.Lstat(dst)
		switch {
		case err != nil:
			add("恢复出的目录里缺了 %s", f.Path)
			continue
		case !fi.Mode().IsRegular() || linkOnDisk(dst):
			add("恢复出的 %s 不是普通文件", f.Path)
			continue
		case fi.Size() != f.Size:
			add("恢复出的 %s 大小不对：清单 %d，实际 %d", f.Path, f.Size, fi.Size())
			continue
		}
		n, sum, err := sha256FileCtx(ctx, dst)
		if err != nil {
			if ctx.Err() == nil {
				add("读不了恢复出的 %s：%v", f.Path, err)
			}
			continue
		}
		if !strings.EqualFold(sum, f.SHA256) {
			add("恢复出的 %s 哈希不对：清单 %s，实际 %s", f.Path, short(f.SHA256), short(sum))
			continue
		}
		rep.Files++
		rep.Bytes += n
	}
	// 目标里清单之外的文件（.restore 除外）：只会是别的东西写进来的，报出来
	extra := 0
	var extraNames []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			if strings.EqualFold(rel, paths.RestoreDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !want[strings.ToLower(filepath.ToSlash(rel))] {
			extra++
			if len(extraNames) < 3 {
				extraNames = append(extraNames, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if extra > 0 {
		add("恢复出的目录里有 %d 个清单之外的文件（比如 %s）", extra, strings.Join(extraNames, "、"))
	}
	// 库
	recorded := 0
	for _, d := range m.DBs {
		if ctx.Err() != nil {
			return problems
		}
		chk := DBCheck{Path: d.Path, Rows: RowsUnrecorded}
		dst := filepath.Join(root, filepath.FromSlash(d.Path))
		r, err := s.inspect(ctx, dst)
		if err != nil {
			if ctx.Err() == nil {
				add("恢复出的库 %s 打不开：%v", d.Path, err)
				chk.Integrity = "打不开"
			}
			rep.DBs = append(rep.DBs, chk)
			continue
		}
		chk.Integrity, chk.Tables = r.Integrity, len(r.Tables)
		if !r.OK() {
			add("恢复出的库 %s 没通过 integrity_check：%s", d.Path, r.Integrity)
		}
		if d.Tables != nil {
			recorded++
			chk.Rows = RowsMatch
			names := make([]string, 0, len(d.Tables))
			for n := range d.Tables {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				got, ok := r.Tables[n]
				switch {
				case !ok:
					chk.Mismatch = append(chk.Mismatch, fmt.Sprintf("表 %s：清单 %d 行，恢复出的库里没有这张表", n, d.Tables[n]))
				case got != d.Tables[n]:
					chk.Mismatch = append(chk.Mismatch, fmt.Sprintf("表 %s：清单 %d 行，恢复出的库里 %d 行", n, d.Tables[n], got))
				}
			}
			for n := range r.Tables {
				if _, ok := d.Tables[n]; !ok {
					chk.Mismatch = append(chk.Mismatch, fmt.Sprintf("表 %s：清单里没有，恢复出的库里多了 %d 行", n, r.Tables[n]))
				}
			}
			sort.Strings(chk.Mismatch)
			if len(chk.Mismatch) > 0 {
				chk.Rows = RowsMismatch
				add("恢复出的库 %s 的行数和清单对不上：%s", d.Path, chk.Mismatch[0])
			}
		}
		rep.DBs = append(rep.DBs, chk)
	}
	rep.RowsRecorded = len(m.DBs) > 0 && recorded == len(m.DBs)
	switch {
	case len(m.DBs) == 0:
		rep.Notes = append(rep.Notes, "清单里没有库，没做行数核对")
	case recorded < len(m.DBs):
		rep.Notes = append(rep.Notes, "这份备份没记行数，只核对了文件和完整性")
	}
	return problems
}
