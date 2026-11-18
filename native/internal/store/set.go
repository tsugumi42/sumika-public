package store

// 分平台的一套库（ADR-199、规格-聊天记录按平台分库 第二、三节）：每个平台一个聊天库，运行记录 trace 共用一个。
// 每个平台那一份仍然是一个 *Store，写库的方法一个都不用改，上层只把"用哪个 Store"从写死换成了按渠道取。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"sumika-native/internal/dbguard"
	"sumika-native/internal/paths"
)

// Set 是一组按平台分开的库
type Set struct {
	byName map[string]*Store // 平台名 → 那个平台的库
	names  []string          // 打开了的平台名，排好序
	// unavailable：登记了、库却打不开（*dbguard.Unavailable）的平台。这些平台不进 names / byName，
	// Each 和 Platforms 看不到，For 给出“库没打开：原因”，整个平台停用（规格第 8 条）。
	unavailable map[string]*dbguard.Unavailable
	dbs         []DBFileStatus // 打开时记下的每个库的版本、状态和快照，给 sumika-ctl db status 用
	trace       *sql.DB        // 共用的运行记录库，每个成员的 Trace 都指着它
	traceS      *Store         // 只有 Trace 的那一份（TraceStore 给出去的）
	reg         Registry       // 打开时用的登记：For、ForConversation 按它从渠道、会话找平台
	// closed：关了以后 For、ForConversation、Platform 不再给出库（给出去的也是关掉的连接）。
	// 关机时可能还有协程在取库，所以用原子量，不是普通的 bool
	closed atomic.Bool
}

// OpenGo 打开（没有就建）一组库：共用的 go/trace.db 和每个平台一个 go/chat/<平台>.db。
// 每个平台库先跑 migrations/chat（通用表）；migrations/ 下有同名目录的平台（local、qq）再跑它自己的表，
// 用自己的版本表（goose_db_version_<平台>），和通用迁移的版本分开记，编号不会撞。
// 以后的新平台没有自己的目录，就只跑通用迁移。渠道对照用默认登记
func OpenGo(p paths.Paths, platforms ...string) (*Set, error) {
	return openSet(p, DefaultRegistry, platforms)
}

// OpenGoWith 打开（没有就建）登记里的全部平台，渠道对照用这份登记（规格-多平台装配第 2 步）
func OpenGoWith(p paths.Paths, reg Registry) (*Set, error) {
	return OpenGoOpts(p, reg, OpenOptions{})
}

func openSet(p paths.Paths, reg Registry, platforms []string) (*Set, error) {
	return openSetOpts(p, reg, platforms, OpenOptions{})
}

func openSetOpts(p paths.Paths, reg Registry, platforms []string, opt OpenOptions) (*Set, error) {
	if len(platforms) == 0 {
		return nil, errors.New("OpenGo 要至少一个平台")
	}
	s := &Set{byName: map[string]*Store{}, unavailable: map[string]*dbguard.Unavailable{}, reg: reg}
	seen := map[string]bool{}
	var requested []string
	for _, name := range platforms {
		if !ValidPlatform(name) {
			return nil, fmt.Errorf("平台名 %q 不能用：只认小写字母、数字、连字符，1 到 32 个字", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("平台 %s 给了两遍", name)
		}
		seen[name] = true
		requested = append(requested, name)
	}
	sort.Strings(requested)
	if err := os.MkdirAll(p.ChatDir(), 0o755); err != nil {
		return nil, err
	}
	traceTr, err := traceTracks()
	if err != nil {
		return nil, err
	}
	trace, traceRep, err := openGuarded(p.GoTraceDB(), traceTr, p.UpgradeSnapshotDir(), relToData(p, p.GoTraceDB()))
	if err != nil {
		return nil, err
	}
	s.trace = trace
	s.traceS = &Store{Trace: trace, shared: true}
	s.noteDB("trace", p.GoTraceDB(), traceTr, traceRep, trace, nil)
	logNewer(opt.Logf, p.GoTraceDB(), traceRep.Newer)
	readOnly := map[string]bool{}
	for _, name := range opt.ReadOnly {
		readOnly[name] = true
		for _, pl := range reg.list {
			if pl.Name == name {
				s.traceS.foreignChannels = append(s.traceS.foreignChannels, pl.Channel)
			}
		}
	}
	for _, name := range requested {
		st := &Store{Trace: trace, shared: true, outcomeOwner: s.traceS, readOnly: readOnly[name]}
		var ev *sql.DB
		var err error
		var rep dbguard.Report
		var tracks []dbguard.Track
		if readOnly[name] {
			ev, err = openReadOnlyPlatform(p, name)
		} else {
			var terr error
			tracks, terr = platformTracks(name)
			if terr == nil {
				ev, rep, err = openGuarded(p.ChatDB(name), tracks, p.UpgradeSnapshotDir(), relToData(p, p.ChatDB(name)))
			} else {
				err = terr
			}
		}
		var u *dbguard.Unavailable
		if errors.As(err, &u) && !readOnly[name] {
			// 这个平台的库打不开：只停这个平台。原因、快照记在 Set 上，自检和 db status 从那里读。
			s.unavailable[name] = u
			s.noteDB(name, p.ChatDB(name), tracks, rep, nil, u)
			continue
		}
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("平台 %s：%w", name, err)
		}
		st.Events = ev
		s.byName[name] = st
		s.names = append(s.names, name)
		s.noteDB(name, p.ChatDB(name), tracks, rep, ev, nil)
		logNewer(opt.Logf, p.ChatDB(name), rep.Newer)
	}
	return s, nil
}

// logNewer 在“库比程序新、但还在最低可读范围内”时记一行（规格第 3 条第 4 步）：每个库一次，不逐轨重复。
func logNewer(logf func(string, ...any), file string, newer []dbguard.NewerTrack) {
	if logf == nil || len(newer) == 0 {
		return
	}
	if len(newer) == 1 {
		logf("%s 比程序新（库 %d、程序 %d），按兼容版本打开", file, newer[0].Version, newer[0].Latest)
		return
	}
	parts := make([]string, 0, len(newer))
	for _, n := range newer {
		parts = append(parts, fmt.Sprintf("%s 轨库 %d、程序 %d", n.Track, n.Version, n.Latest))
	}
	logf("%s 比程序新（%s），按兼容版本打开", file, strings.Join(parts, "；"))
}

// OpenGoReadOnly 照默认登记打开已有的桌面历史库组，见 OpenGoReadOnlyWith
func OpenGoReadOnly(ctx context.Context, p paths.Paths) (*Set, error) {
	return OpenGoReadOnlyWith(ctx, p, DefaultRegistry)
}

// OpenGoReadOnlyWith 打开已有的桌面历史库组，不跑迁移、不做维护、不建派生索引。
// 运行记录库不可用（太新、认不出、探查失败）整组失败；平台库不可用时只停这个平台：
// 不进平台列表、原因记在 Set 上，db status 能看到。登记里“必须有”的平台库缺了或不可读都报错；
// 其余登记过的平台库不存在就跳过（登记了新平台以后，老用户没有那个库也照样能看历史），在就照样打开
func OpenGoReadOnlyWith(ctx context.Context, p paths.Paths, reg Registry) (*Set, error) {
	return OpenGoReadOnlyOpts(ctx, p, reg, OpenOptions{})
}

// OpenGoReadOnlyOpts 和 OpenGoReadOnlyWith 一样，只是能记“库比程序新、按兼容版本打开”的日志。
func OpenGoReadOnlyOpts(ctx context.Context, p paths.Paths, reg Registry, opt OpenOptions) (*Set, error) {
	traceFile := p.GoTraceDB()
	traceTr, err := traceTracks()
	if err != nil {
		return nil, err
	}
	if err := checkHistoryFile(traceFile); err != nil {
		return nil, err
	}
	traceDec, err := dbguard.Inspect(ctx, traceFile, traceTr)
	if err != nil {
		return nil, fmt.Errorf("%s：%w", traceFile, err)
	}
	// 所有平台库都先看过再打开 trace：只有“这个平台不可用”能跳过，别的检查错误整组失败。
	type historyDB struct {
		name   string
		file   string
		tracks []dbguard.Track
		dec    dbguard.Decision
		u      *dbguard.Unavailable
	}
	var history []historyDB
	for _, pl := range reg.list {
		file := p.ChatDB(pl.Name)
		if !pl.Required {
			if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		if err := checkHistoryFile(file); err != nil {
			return nil, err
		}
		tracks, err := platformTracks(pl.Name)
		if err != nil {
			return nil, err
		}
		dec, err := dbguard.Inspect(ctx, file, tracks)
		var u *dbguard.Unavailable
		if err != nil && !errors.As(err, &u) {
			return nil, fmt.Errorf("%s：%w", file, err)
		}
		history = append(history, historyDB{name: pl.Name, file: file, tracks: tracks, dec: dec, u: u})
	}
	sort.Slice(history, func(i, j int) bool { return history[i].name < history[j].name })

	s := &Set{byName: map[string]*Store{}, unavailable: map[string]*dbguard.Unavailable{}, reg: reg}
	s.trace, err = OpenReadOnlyFile(traceFile)
	if err != nil {
		return nil, err
	}
	s.traceS = &Store{Trace: s.trace, shared: true}
	s.noteDB("trace", traceFile, traceTr, reportFromDecision(traceDec, traceTr), s.trace, nil)
	logNewer(opt.Logf, traceFile, traceDec.Newer)
	for _, h := range history {
		if h.u != nil {
			s.unavailable[h.name] = h.u
			s.noteDB(h.name, h.file, h.tracks, reportFromDecision(h.dec, h.tracks), nil, h.u)
			continue
		}
		ev, err := OpenReadOnlyFile(h.file)
		if err != nil {
			s.Close()
			return nil, err
		}
		// 试读已有表结构，不创建或迁移任何东西。
		rows, err := ev.QueryContext(ctx, `SELECT seq, id, conversation, created_at FROM events LIMIT 0`)
		if err != nil {
			ev.Close()
			s.Close()
			return nil, fmt.Errorf("Go 历史 %s 表结构不可读：%w", h.name, err)
		}
		rows.Close()
		s.byName[h.name] = &Store{Events: ev, Trace: s.trace, shared: true, outcomeOwner: s.traceS}
		s.names = append(s.names, h.name)
		s.noteDB(h.name, h.file, h.tracks, reportFromDecision(h.dec, h.tracks), ev, nil)
		logNewer(opt.Logf, h.file, h.dec.Newer)
	}
	if err := ctx.Err(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// checkHistoryFile 查一个归档只读要打开的库在不在、是不是普通文件。
func checkHistoryFile(file string) error {
	info, err := os.Stat(file)
	if err != nil {
		return fmt.Errorf("已有 Go 库缺失或不可读 %s：%w", file, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("已有 Go 库不是普通文件：%s", file)
	}
	return nil
}

// reportFromDecision 把只读探查的判定转成 noteDB 要的版本来源：没有版本表的旧库也能在 db status 里显示看到的号。
func reportFromDecision(dec dbguard.Decision, tracks []dbguard.Track) dbguard.Report {
	rep := dbguard.Report{}
	version := func(name string) (int64, bool) {
		for _, p := range dec.Pending {
			if p.Track == name {
				return p.From, true
			}
		}
		for _, n := range dec.Newer {
			if n.Track == name {
				return n.Version, true
			}
		}
		for _, t := range dec.TooNew {
			if t.Track == name {
				return t.Applied, true
			}
		}
		if dec.Action == dbguard.ActionOpen {
			for _, t := range tracks {
				if t.Name == name {
					latest, err := t.Latest()
					if err == nil {
						return latest, true
					}
				}
			}
		}
		return 0, false
	}
	for _, t := range tracks {
		v, _ := version(t.Name)
		rep.Tracks = append(rep.Tracks, dbguard.TrackReport{Track: t.Name, From: v})
	}
	return rep
}

// hasMigrations 查 migrations/ 下有没有这个平台的目录（只有通用表的平台没有）
func hasMigrations(platform string) bool {
	entries, err := fs.ReadDir(migrations, "migrations/"+platform)
	return err == nil && len(entries) > 0
}

// For 按渠道取那个平台的库；没登记的渠道、这组里没打开的平台都报错
func (s *Set) For(channel string) (*Store, error) {
	name, err := s.reg.PlatformOf(channel)
	if err != nil {
		return nil, err
	}
	return s.platform(name)
}

// ForConversation 按会话串取（只有会话串的入口用它，别自己拆串）
func (s *Set) ForConversation(conversation string) (*Store, error) {
	name, err := s.reg.PlatformOfConversation(conversation)
	if err != nil {
		return nil, err
	}
	return s.platform(name)
}

func (s *Set) platform(name string) (*Store, error) {
	if s.closed.Load() {
		return nil, errors.New("这组库已经关了")
	}
	if st := s.byName[name]; st != nil {
		return st, nil
	}
	if u := s.unavailable[name]; u != nil {
		// 和“没登记的平台”分开：登记过、库却打不开，原因是 *dbguard.Unavailable，整个平台已经停用。
		return nil, fmt.Errorf("平台 %s 的库没打开：%w", name, u)
	}
	return nil, fmt.Errorf("平台 %s 的库没打开（这组有 %s）", name, strings.Join(s.names, "、"))
}

// Platform 按平台名取，没有或者这组库已经关了 ok 为假
func (s *Set) Platform(name string) (*Store, bool) {
	if s.closed.Load() {
		return nil, false
	}
	st, ok := s.byName[name]
	return st, ok && st != nil
}

// Each 按平台名排好序给出每个平台的库：出站的 Sweep 每个库扫一遍、记忆跨库查再合并要用。
// 关了以后是空的
func (s *Set) Each() []Named {
	if s.closed.Load() {
		return nil
	}
	out := make([]Named, 0, len(s.names))
	for _, name := range s.names {
		out = append(out, Named{Platform: name, Store: s.byName[name]})
	}
	return out
}

// TraceStore 是只用来调运行记录（trace）那些方法的一份：回合、步骤、模型调用、闸门状态、SweepInterrupted。
// 它的 Events 是空的，调聊天库的方法会 panic；内核拿 trace 从这里拿，不借某个平台的库（不然 Hub 就依赖那个平台一定打开）。
// SweepInterrupted 整个进程只扫一次（规格第六节），这里不替调用方守
func (s *Set) TraceStore() *Store {
	return s.traceS
}

// Platforms 是这组里打开的平台，排好序
func (s *Set) Platforms() []string {
	return append([]string(nil), s.names...)
}

// Trace 是共用的运行记录库
func (s *Set) Trace() *sql.DB {
	return s.trace
}

// Close 关掉每个平台的库和共用的 trace，各关一次；调两次不报错
func (s *Set) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if s.traceS != nil {
		s.traceS.outcomeMu.Lock()
		s.traceS.outcomesSealed = true
		s.traceS.outcomeMu.Unlock()
	}
	var errs []error
	for _, name := range s.names {
		st := s.byName[name]
		if st == nil || st.Events == nil {
			continue
		}
		if err := st.Events.Close(); err != nil {
			errs = append(errs, fmt.Errorf("关 %s 库：%w", name, err))
		}
	}
	if s.trace != nil {
		if err := s.trace.Close(); err != nil {
			errs = append(errs, fmt.Errorf("关 trace 库：%w", err))
		}
	}
	return errors.Join(errs...)
}

// Stores 是"按渠道取库"的东西（渠道中间件和内核拿它，不自己拆会话串选库）
type Stores interface {
	For(channel string) (*Store, error)
	ForConversation(conversation string) (*Store, error)
	Each() []Named
	TraceStore() *Store
}

// Named 是一个平台的库和它的平台名（跨库合并时同一毫秒按平台名排，要带着名字）
type Named struct {
	Platform string
	Store    *Store
}

var _ Stores = (*Set)(nil)

// Single 把一个库包成 Stores：登记过的渠道、会话都给这一个库，没登记的照样报错（和 Set 一样经 PlatformOf）。
// 只给测试和还没切到分库的老调用方用；生产装配用 OpenGo。
// 它证明不了"选对了库"：所有平台都是同一个库，要验"各库不混"得用 OpenGo 开真的两个库
func Single(st *Store) Stores {
	return single{st}
}

type single struct{ st *Store }

func (s single) For(channel string) (*Store, error) {
	if _, err := PlatformOf(channel); err != nil {
		return nil, err
	}
	return s.st, nil
}

func (s single) ForConversation(conversation string) (*Store, error) {
	if _, err := PlatformOfConversation(conversation); err != nil {
		return nil, err
	}
	return s.st, nil
}

// Each 只有一份，平台名叫 "single"（不是登记过的平台，只用来排序）
func (s single) Each() []Named { return []Named{{Platform: "single", Store: s.st}} }

// TraceStore 就是这一个库（它自己带着 trace）
func (s single) TraceStore() *Store { return s.st }
