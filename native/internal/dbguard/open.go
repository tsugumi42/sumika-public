package dbguard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pressly/goose/v3"

	"sumika-native/internal/sqlitesnap"
)

// Options 是 Open 从调用方拿的外部依赖。可写连接、剩余空间、时钟和改名都能注入，
// 生产接 store 时传真实实现，测试里换成会失败 / 会被拒的版本，不往生产代码里加开关。
type Options struct {
	SnapshotDir string // 快照目录（规格第 5 条：<数据目录>/go/upgrade-snapshots）
	RelName     string // 库相对数据目录的路径，用来起快照文件名；设了 SnapshotDir 就必填
	// OpenDB 打开可写连接。fresh 为真表示这次按新库处理（文件不存在、0 字节或一张表都没有）。
	OpenDB     func(file string, fresh bool) (*sql.DB, error)
	FreeSpace  func(dir string) (uint64, error) // 空 = 系统实现
	Now        func() time.Time                 // 空 = time.Now
	MigratedBy string                           // 空 = MigratedBy()
	Keep       int                              // 每个库留几份快照；0 = 3
	Rename     func(old, new string) error      // 空 = 带重试的 os.Rename
	Remove     func(path string) error          // 空 = 带重试的 os.Remove
}

// TrackReport 是 Report 里一条轨这次开库前后的版本。
type TrackReport struct {
	Track string
	From  int64
	To    int64
}

// Report 说明这次开库走了哪条路、每条轨从几到几、快照在哪、清掉了哪些旧快照。
type Report struct {
	File     string
	Action   Action
	Tracks   []TrackReport
	Newer    []NewerTrack // 走“较新能读”时填，调用方可以日志一次
	Snapshot string       // 这次拍下的快照（迁移失败是 -failed，换回失败是 -restorefailed 的路径）
	Removed  []string     // 迁移成功后清掉的旧快照完整路径
}

// Open 按规格第 3 条打开（没有就新建）一个库文件：只读探查 → 判定 → 新建 / 太新 / 较新能读 /
// 无待跑（补写版本表）/ 有待跑（空间、快照、逐轨迁移、写版本表；失败关连接、换回）。
//
// 有待跑迁移时先拍一份快照再迁；任何一条轨失败都把整个文件换回快照并返回 *Unavailable
// （ReasonMigrateFailed，换回本身失败是 ReasonRestoreFailed）。太新时绝不调用 OpenDB，
// 保证一个字节都不写。返回的 *sql.DB 已经走完护栏，调用方拿到就能用。
func Open(ctx context.Context, file string, tracks []Track, opts Options) (*sql.DB, Report, error) {
	o := opts.withDefaults()
	rep := Report{File: file}
	if o.SnapshotDir != "" && o.RelName == "" {
		return nil, rep, fmt.Errorf("dbguard 打开 %s：设了 Options.SnapshotDir 就必须给 Options.RelName（快照文件名靠它区分同名的库）", file)
	}
	if o.OpenDB == nil {
		return nil, rep, fmt.Errorf("dbguard 打开 %s：Options.OpenDB 没给", file)
	}
	unlock, err := lockFile(ctx, file)
	if err != nil {
		return nil, rep, err
	}
	defer unlock()

	_, statErr := os.Stat(file)
	existed := statErr == nil

	before, dec, err := probeStateAndDecision(ctx, file, tracks)
	if err != nil {
		return nil, rep, err
	}
	rep.Action = dec.Action

	switch dec.Action {
	case ActionCreate:
		return o.create(ctx, file, tracks, rep, existed)

	case ActionTooNew:
		rep.Tracks = observedTracks(before, tracks)
		return nil, rep, tooNewError(file, dec, before)

	case ActionUnknownSchema:
		rep.Tracks = observedTracks(before, tracks)
		return nil, rep, unknownSchemaError(file)

	case ActionReadNewer:
		rep.Tracks = observedTracks(before, tracks)
		rep.Newer = dec.Newer
		db, err := o.OpenDB(file, false)
		if err != nil {
			return nil, rep, err
		}
		return db, rep, nil

	case ActionOpen:
		rep.Tracks = observedTracks(before, tracks)
		db, err := o.OpenDB(file, false)
		if err != nil {
			return nil, rep, err
		}
		if dec.NeedSchema {
			if err := WriteSchema(ctx, db, tracks, o.MigratedBy, o.Now()); err != nil {
				db.Close()
				return nil, rep, fmt.Errorf("给 %s 补写 %s：%w", file, SchemaTable, err)
			}
		}
		return db, rep, nil

	case ActionMigrate:
		return o.migrateFile(ctx, file, tracks, dec, before, rep)
	}
	return nil, rep, fmt.Errorf("dbguard 不认识的开库路线 %v", dec.Action)
}

// Inspect 只读看一眼库文件并给出判定，不开可写连接、不动文件。
// 太新返回 *Unavailable（too_new），认不出返回 unknown_schema，探查失败返回 probe_failed；
// 文件不存在或一张表都没有时返回 ActionCreate。给归档只读打开和角色记忆库开库前的检查用。
func Inspect(ctx context.Context, file string, tracks []Track) (Decision, error) {
	st, dec, err := probeStateAndDecision(ctx, file, tracks)
	if err != nil {
		return dec, err
	}
	switch dec.Action {
	case ActionTooNew:
		return dec, tooNewError(file, dec, st)
	case ActionUnknownSchema:
		return dec, unknownSchemaError(file)
	}
	return dec, nil
}

// probeStateAndDecision 是 Open 和 Inspect 共用的一步：只读探查后判定。
// 探查失败包成 *Unavailable（probe_failed），底层错误留在 Err；ctx 取消返回取消错误本身。
func probeStateAndDecision(ctx context.Context, file string, tracks []Track) (State, Decision, error) {
	before, err := Probe(ctx, file, tracks)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return before, Decision{}, fmt.Errorf("探查 %s 时取消：%w", file, cerr)
		}
		return before, Decision{}, &Unavailable{
			File: file, Reason: ReasonProbeFailed, Detail: "只读探查失败：" + err.Error(), Err: err,
		}
	}
	dec, err := Decide(before, tracks)
	if err != nil {
		return before, Decision{}, err
	}
	return before, dec, nil
}

func unknownSchemaError(file string) *Unavailable {
	return &Unavailable{
		File: file, Reason: ReasonUnknownSchema,
		Detail: "库里有用户表，却没有一条迁移轨的 goose 版本表；可能是别的程序的库，不能当 0 版迁移",
	}
}

// lockFile 让同一进程里对同一个文件的 Open 互斥（规格第 3 条第 7 步），整个 Open 期间持有。
// 键是绝对路径 + Clean，Windows 上再转小写（文件名不区分大小写）；跨进程靠数据目录锁。
// 等锁期间看 ctx：取消就不拿锁、返回取消错误本身，不把调用方卡在一个已经放弃的 Open 上。
// 没有等待者时把锁从表里删掉，不给每个开过的库永久留一个条目。
func lockFile(ctx context.Context, file string) (func(), error) {
	key := lockKey(file)
	openLocksMu.Lock()
	l := openLocks[key]
	if l == nil {
		l = &openLock{held: make(chan struct{}, 1)}
		l.held <- struct{}{}
		openLocks[key] = l
	}
	l.refs++
	openLocksMu.Unlock()

	dropRef := func() {
		openLocksMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(openLocks, key)
		}
		openLocksMu.Unlock()
	}
	release := func() {
		l.held <- struct{}{}
		dropRef()
	}

	select {
	case <-ctx.Done():
		dropRef()
		return nil, fmt.Errorf("等待库 %s 的开库锁：%w", file, ctx.Err())
	case <-l.held:
	}
	// 拿到锁的同时 ctx 也可能刚好取消：取消优先，把锁放回去。
	if err := ctx.Err(); err != nil {
		release()
		return nil, fmt.Errorf("等待库 %s 的开库锁：%w", file, err)
	}
	return release, nil
}

func lockKey(file string) string {
	if abs, err := filepath.Abs(file); err == nil {
		file = abs
	}
	file = filepath.Clean(file)
	if runtime.GOOS == "windows" {
		file = strings.ToLower(file)
	}
	return file
}

type openLock struct {
	held chan struct{}
	refs int
}

var (
	openLocksMu sync.Mutex
	openLocks   = map[string]*openLock{}
)

func (o Options) withDefaults() Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Keep <= 0 {
		o.Keep = 3
	}
	if o.MigratedBy == "" {
		o.MigratedBy = MigratedBy()
	}
	if o.FreeSpace == nil {
		o.FreeSpace = systemFreeSpace
	}
	if o.Rename == nil {
		o.Rename = os.Rename
	}
	if o.Remove == nil {
		o.Remove = os.Remove
	}
	return o
}

// rename 走调用方给的改名函数，Windows 上被临时拒绝时照 secrets 的 robustio 写法重试。
func (o Options) rename(old, new string) error {
	return renameReplacing(o.Rename, old, new)
}

// remove 走调用方给的删除函数，和改名共用一套 Windows 重试；文件已经不在不算错。
func (o Options) remove(path string) error {
	return retryEphemeral(func() error {
		err := o.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
}

// discardNewFile 删掉这次新建失败的文件和旁文件。文件原来是这次调用建的才走这里。
func (o Options) discardNewFile(file string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = o.remove(file + suffix)
	}
}

func (o Options) create(ctx context.Context, file string, tracks []Track, rep Report, existed bool) (*sql.DB, Report, error) {
	db, err := o.OpenDB(file, true)
	if err != nil && db != nil {
		db.Close()
		db = nil
	}
	if err == nil {
		failed, failErr := runTracks(ctx, db, tracks, nil)
		if failErr == nil {
			if werr := WriteSchema(ctx, db, tracks, o.MigratedBy, o.Now()); werr != nil {
				failErr = fmt.Errorf("写 %s：%w", SchemaTable, werr)
			}
		}
		if failErr == nil {
			rep.Tracks = createdTracks(tracks)
			return db, rep, nil
		}
		db.Close()
		if len(failed.Track) > 0 {
			err = errors.New(migrationFailureDetail(failed, failErr))
		} else {
			err = failErr
		}
	}
	if !existed {
		o.discardNewFile(file)
	}
	if cerr := ctx.Err(); cerr != nil {
		return nil, rep, fmt.Errorf("新建 %s 时取消：%w", file, cerr)
	}
	return nil, rep, &Unavailable{File: file, Reason: ReasonCreateFailed, Detail: "新建 " + file + " 失败：" + err.Error(), Err: err}
}

func (o Options) migrateFile(ctx context.Context, file string, tracks []Track, dec Decision, before State, rep Report) (*sql.DB, Report, error) {
	if o.SnapshotDir == "" {
		return nil, rep, &Unavailable{File: file, Reason: ReasonSnapshotFailed, Detail: "没有配置快照目录，不能拍迁移前快照"}
	}
	// 快照目录可能第一次用到，先建出来：剩余空间要按它所在盘查，FreeSpace 需要一个存在的目录。
	if err := os.MkdirAll(o.SnapshotDir, 0o755); err != nil {
		return nil, rep, &Unavailable{
			File: file, Reason: ReasonSnapshotFailed,
			Detail: "建快照目录 " + o.SnapshotDir + "：" + err.Error(), Err: err,
		}
	}

	size, err := dbFootprint(file)
	if err != nil {
		return nil, rep, &Unavailable{File: file, Reason: ReasonNoSpace, Detail: "算库文件大小失败：" + err.Error(), Err: err}
	}
	// 规格第 5 条：库所在盘和快照目录所在盘的剩余空间都要够。写成 size > free/2 避免 2× 溢出。
	for _, place := range []struct {
		label, dir string
	}{
		{"库所在盘", filepath.Dir(file)},
		{"快照目录所在盘", o.SnapshotDir},
	} {
		free, err := o.FreeSpace(place.dir)
		if err != nil {
			return nil, rep, &Unavailable{
				File: file, Reason: ReasonNoSpace,
				Detail: fmt.Sprintf("查%s（%s）剩余空间失败：%v", place.label, place.dir, err), Err: err,
			}
		}
		if size > free/2 {
			return nil, rep, &Unavailable{
				File: file, Reason: ReasonNoSpace,
				Detail: fmt.Sprintf("拍迁移前快照至少要 %d 字节，%s（%s）剩余 %d 字节", 2*size, place.label, place.dir, free),
			}
		}
	}

	snap, err := o.takeSnapshot(ctx, file, dec.Pending, before, tracks)
	if err != nil {
		return nil, rep, &Unavailable{File: file, Reason: ReasonSnapshotFailed, Detail: err.Error(), Err: err}
	}
	rep.Snapshot = snap

	db, err := o.OpenDB(file, false)
	if err != nil {
		return nil, rep, err
	}
	pending := make(map[string]PendingTrack, len(dec.Pending))
	for _, p := range dec.Pending {
		pending[p.Track] = p
	}
	failed, failErr := runTracks(ctx, db, tracks, pending)
	if failErr != nil {
		db.Close()
		// 换回前先把 Report 写成“回到迁移前”的样子，调用方拿到的数字和磁盘一致。
		rep.Tracks = migratedTracks(before, tracks, nil)
		// 换回和验证不受取消影响（规格第 3 条第 6 步第 4 小点）：迁移途中取消也要把库换回去。
		if rerr := o.restore(ctx, file, snap, tracks, before); rerr != nil {
			failedSnap, keepErr := o.markRestoreFailed(snap)
			o.pruneFailedSnapshots()
			rep.Snapshot = failedSnap
			detail := fmt.Sprintf("%s；换回也失败：%v；原库 %s、快照 %s 都留给人处理",
				migrationFailureDetail(failed, failErr), rerr, absPath(file), absPath(failedSnap))
			if keepErr != nil {
				detail += fmt.Sprintf("；快照没能标记，请手动保留 %s", absPath(failedSnap))
			}
			return nil, rep, &Unavailable{
				File: file, Track: failed.Track, Reason: ReasonRestoreFailed,
				Detail: detail, Snapshot: failedSnap, Err: rerr,
			}
		}
		failedSnap := o.markFailed(snap)
		o.pruneFailedSnapshots()
		rep.Snapshot = failedSnap
		if cerr := ctx.Err(); cerr != nil {
			return nil, rep, fmt.Errorf("%s；库已换回迁移前（快照在 %s）：%w",
				migrationFailureDetail(failed, failErr), failedSnap, cerr)
		}
		return nil, rep, &Unavailable{
			File: file, Track: failed.Track, Reason: ReasonMigrateFailed,
			Detail:   migrationFailureDetail(failed, failErr) + "；库已换回迁移前",
			Snapshot: failedSnap, Err: failErr,
		}
	}

	if err := WriteSchema(ctx, db, tracks, o.MigratedBy, o.Now()); err != nil {
		db.Close()
		return nil, rep, fmt.Errorf("迁移完写 %s（%s）：%w", SchemaTable, file, err)
	}
	rep.Tracks = migratedTracks(before, tracks, pending)
	rep.Removed = o.pruneSnapshots()
	return db, rep, nil
}

// runTracks 按轨依次跑 goose；pending 不为 nil 时只跑里面的轨。
// FS 为 nil 的轨没有迁移文件（只有版本号），不跑 goose，版本表由 WriteSchema 写。
// 返回失败的那条轨（含待跑的号）。
func runTracks(ctx context.Context, db *sql.DB, tracks []Track, pending map[string]PendingTrack) (PendingTrack, error) {
	for _, t := range tracks {
		if t.FS == nil {
			continue
		}
		failed := PendingTrack{Track: t.Name}
		if pending != nil {
			p, ok := pending[t.Name]
			if !ok {
				continue
			}
			failed = p
		}
		opts := []goose.ProviderOption{}
		if t.Table != "" {
			opts = append(opts, goose.WithTableName(t.Table))
		}
		pv, err := goose.NewProvider(goose.DialectSQLite3, db, t.FS, opts...)
		if err != nil {
			return failed, err
		}
		if _, err := pv.Up(ctx); err != nil {
			return failed, err
		}
	}
	return PendingTrack{}, nil
}

// migrationFailureDetail 写失败详情。goose 的 *PartialError 带失败的那个迁移号（Failed.Source.Version），
// 取得到就写“第 N 号迁移失败”，规格第 3 条第 6 步第 4 小点要这个号。
func migrationFailureDetail(failed PendingTrack, err error) string {
	var pe *goose.PartialError
	if errors.As(err, &pe) && pe.Failed != nil && pe.Failed.Source != nil {
		return fmt.Sprintf("第 %d 号迁移失败：%v", pe.Failed.Source.Version, err)
	}
	return fmt.Sprintf("迁移轨 %s 从 %d 到 %d 失败：%v", failed.Track, failed.From, failed.To, err)
}

// takeSnapshot 拍迁移前快照：先写 .partial，改名成 .sqlite，再用 Inspect 和 Probe 核对。
// 任何一步失败都删掉这次没通过的文件，不把它当快照留下。
func (o Options) takeSnapshot(ctx context.Context, file string, pending []PendingTrack, before State, tracks []Track) (string, error) {
	// 规格第 5 条：拍之前清掉这个库残留的 .partial。清不掉不挡这次快照（新名字唯一，残留只是垃圾）。
	o.removeStalePartials()
	final := uniqueSnapshotPath(o.SnapshotDir, snapshotBase(o.RelName, pending, o.Now()))
	partial := final + ".partial"
	_ = os.Remove(partial) // 上次崩掉留下的半份，不是能用的快照
	if err := sqlitesnap.IntoFile(ctx, file, partial); err != nil {
		return "", fmt.Errorf("拍快照：%w", err)
	}
	if err := o.rename(partial, final); err != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("快照改名：%w", err)
	}
	discard := func(err error) (string, error) {
		_ = os.Remove(final)
		return "", err
	}

	inspected, err := sqlitesnap.Inspect(ctx, final)
	if err != nil {
		return discard(fmt.Errorf("检查快照：%w", err))
	}
	if !inspected.OK() {
		return discard(fmt.Errorf("快照 integrity_check 不通过：%s", inspected.Integrity))
	}
	snapState, err := Probe(ctx, final, tracks)
	if err != nil {
		return discard(fmt.Errorf("核对快照：%w", err))
	}
	want := make(map[string]int64, len(before.Tracks))
	for _, ts := range before.Tracks {
		want[ts.Name] = ts.Applied
	}
	for _, ts := range snapState.Tracks {
		if got, ok := want[ts.Name]; ok && ts.Applied != got {
			return discard(fmt.Errorf("快照里迁移轨 %s 是 %d，迁移前是 %d", ts.Name, ts.Applied, got))
		}
	}
	return final, nil
}

var snapshotStamp = regexp.MustCompile(`-(\d{8}-\d{6})(?:-\d+)?\.sqlite$`)

// snapshotPrefix 是这个库所有快照（含失败和 .partial）共用的文件名前缀。
// 调用方必须给 RelName：每个角色的库都叫 memory.db，只用文件名会互相清。
func snapshotPrefix(rel string) string {
	return strings.ReplaceAll(filepath.ToSlash(rel), "/", "-") + "-"
}

// removeStalePartials 清掉这个库上次没拍完的 .partial；别的库的、已经改名的、带 .keep 标记的都不动。
func (o Options) removeStalePartials() {
	entries, err := os.ReadDir(o.SnapshotDir)
	if err != nil {
		return
	}
	prefix := snapshotPrefix(o.RelName)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(o.SnapshotDir, name)
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".partial") {
			continue
		}
		// 保留标记写在快照旁（X.sqlite.keep），不写在 .partial 旁；查 .partial.keep 永远对不上。
		if hasKeepMarker(strings.TrimSuffix(path, ".partial")) {
			continue
		}
		_ = o.remove(path)
	}
}

// snapshotBase 照规格第 5 条起名：<库相对路径，斜杠换横线>-<轨>-v<旧>-to-v<新>-<YYYYMMDD-HHMMSS>.sqlite。
// 一次迁移拍到多条轨时轨名用 + 连起来，旧号取最小的、新号取最大的。
func snapshotBase(rel string, pending []PendingTrack, now time.Time) string {
	prefix := strings.ReplaceAll(filepath.ToSlash(rel), "/", "-")
	names := make([]string, 0, len(pending))
	from, to := pending[0].From, pending[0].To
	for _, p := range pending {
		names = append(names, p.Track)
		if p.From < from {
			from = p.From
		}
		if p.To > to {
			to = p.To
		}
	}
	return fmt.Sprintf("%s-%s-v%d-to-v%d-%s.sqlite",
		prefix, strings.Join(names, "+"), from, to, now.UTC().Format("20060102-150405"))
}

// uniqueSnapshotPath 在同一秒里连开两次时给文件名加 -2、-3，不覆盖也不让 IntoFile 报“已存在”。
// 一个名字算被占用，只要它的五种落点里有任何一个存在：快照本身、拍摄中的 .partial、
// 迁移失败的 -failed、换回失败的 -restorefailed，以及保留标记 .sqlite.keep。只看 .sqlite 会让
// 同一秒的第二份拍到刚改名成 -restorefailed 的那份身上。
func uniqueSnapshotPath(dir, base string) string {
	stem := strings.TrimSuffix(base, ".sqlite")
	for i := 1; ; i++ {
		name := stem
		if i > 1 {
			name = fmt.Sprintf("%s-%d", stem, i)
		}
		if !snapshotNameTaken(dir, name) {
			return filepath.Join(dir, name+".sqlite")
		}
	}
}

// snapshotNameTaken 报告快照 stem（不含 .sqlite）的五个落点有没有文件。
func snapshotNameTaken(dir, stem string) bool {
	for _, suffix := range []string{".sqlite", ".sqlite.partial", "-failed.sqlite", "-restorefailed.sqlite", ".sqlite.keep"} {
		if _, err := os.Lstat(filepath.Join(dir, stem+suffix)); err == nil {
			return true
		}
	}
	return false
}

// pruneSnapshots 只清这个库（同一 RelName 前缀）、不带 -failed / -restorefailed、也没有 .keep 标记的旧快照，
// 按文件名里的时间留最近 Keep 份。
func (o Options) pruneSnapshots() []string {
	entries, err := os.ReadDir(o.SnapshotDir)
	if err != nil {
		return nil
	}
	prefix := snapshotPrefix(o.RelName)
	type snapshot struct {
		name, stamp string
	}
	var have []snapshot
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".sqlite") ||
			strings.HasSuffix(name, "-failed.sqlite") || strings.HasSuffix(name, "-restorefailed.sqlite") {
			continue
		}
		if hasKeepMarker(filepath.Join(o.SnapshotDir, name)) {
			continue
		}
		m := snapshotStamp.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		have = append(have, snapshot{name, m[1]})
	}
	sort.Slice(have, func(i, j int) bool {
		if have[i].stamp != have[j].stamp {
			return have[i].stamp > have[j].stamp
		}
		return have[i].name > have[j].name
	})
	var removed []string
	for i := o.Keep; i < len(have); i++ {
		path := filepath.Join(o.SnapshotDir, have[i].name)
		if err := os.Remove(path); err == nil {
			removed = append(removed, path)
		}
	}
	sort.Strings(removed)
	return removed
}

// markFailed 给迁移失败的那份快照加 -failed；目标名字被占用或改名失败就保持原名（快照无论如何不删）。
func (o Options) markFailed(snap string) string {
	if strings.HasSuffix(snap, "-failed.sqlite") {
		return snap
	}
	failed, err := markedSnapshotPath(snap, "-failed")
	if err != nil {
		return snap
	}
	if err := o.rename(snap, failed); err != nil {
		return snap
	}
	return failed
}

// markRestoreFailed 给换回失败的快照加 -restorefailed；普通保留和失败快照清理都不碰这个后缀。
// 目标名字被占用就换序号，不覆盖已有的 -restorefailed；改名失败就保持原名，并在快照旁写 .keep
// 保留标记，让所有清理跳过这份迁移前唯一的好数据；标记也写失败时返回错误，调用方把路径写进
// 给人看的详情，请人手动保留。
func (o Options) markRestoreFailed(snap string) (string, error) {
	if strings.HasSuffix(snap, "-restorefailed.sqlite") {
		return snap, nil
	}
	failed, err := markedSnapshotPath(snap, "-restorefailed")
	if err == nil {
		if err = o.rename(snap, failed); err == nil {
			return failed, nil
		}
	}
	return snap, o.writeKeepMarker(snap)
}

// markedSnapshotPath 给快照找加后缀后的新名字：<快照名><mark>.sqlite；目标已存在就换下一个序号
// （Windows 上 os.Rename 碰到已存在的目标会失败，Unix 上会直接覆盖，所以两个平台都先查）。
// 快照名自带 -2 序号时从它往后排；1 到 maxSnapshotSeq 都占满返回错误，由调用方按改名失败处理。
func markedSnapshotPath(snap, mark string) (string, error) {
	dir := filepath.Dir(snap)
	stem := strings.TrimSuffix(filepath.Base(snap), ".sqlite")
	base, seq := stem, 1
	if m := snapshotSeq.FindStringSubmatch(stem); m != nil {
		base = m[1]
		if m[2] != "" {
			seq, _ = strconv.Atoi(m[2])
		}
	}
	for ; seq <= maxSnapshotSeq; seq++ {
		name := base + mark + ".sqlite"
		if seq > 1 {
			name = fmt.Sprintf("%s-%d%s.sqlite", base, seq, mark)
		}
		if _, err := os.Lstat(filepath.Join(dir, name)); errors.Is(err, os.ErrNotExist) {
			return filepath.Join(dir, name), nil
		}
	}
	return "", fmt.Errorf("快照 %s 改名成 %s 时 1 到 %d 的序号都被占用", filepath.Base(snap), mark, maxSnapshotSeq)
}

const maxSnapshotSeq = 100

var snapshotSeq = regexp.MustCompile(`^(.*-\d{8}-\d{6})(?:-(\d+))?$`)

const keepMarkerSuffix = ".keep"

// writeKeepMarker 在快照旁写一行保留标记，写也走和改名、删除同一套 Windows 重试。
func (o Options) writeKeepMarker(snap string) error {
	line := o.Now().UTC().Format(time.RFC3339) + " 换回失败，迁移前唯一的好数据，不要删\n"
	return retryEphemeral(func() error {
		return os.WriteFile(keepMarkerPath(snap), []byte(line), 0o600)
	})
}

// keepMarkerPath 是快照旁保留标记的路径；标记是文件不是快照，清理从不把它当目标。
func keepMarkerPath(snap string) string {
	return snap + keepMarkerSuffix
}

// hasKeepMarker 报告快照旁有没有保留标记：有标记的快照普通保留、失败快照清理和 .partial 清理都不动。
func hasKeepMarker(path string) bool {
	_, err := os.Lstat(keepMarkerPath(path))
	return err == nil
}

var failedSnapshotName = regexp.MustCompile(`^(.*)-(\d{8}-\d{6})(?:-(\d+))?-failed\.sqlite$`)
var snapshotSpan = regexp.MustCompile(`-v(\d+)-to-v(\d+)$`)

// pruneFailedSnapshots 同 RelName、同轨组合、同“从几到几”的失败快照只留最新一份；
// 轨组合不同的（例如双轨库失败过一次、后来只剩一条轨又失败）是不同段，不能互删。
// 带 .keep 标记的失败快照不清；普通快照仍由 pruneSnapshots 按 Keep 留，换回失败的 -restorefailed 谁都不清。
func (o Options) pruneFailedSnapshots() []string {
	entries, err := os.ReadDir(o.SnapshotDir)
	if err != nil {
		return nil
	}
	prefix := snapshotPrefix(o.RelName)
	type failedSnap struct {
		name, stamp string
		seq         int
	}
	bySpan := map[string][]failedSnap{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, "-failed.sqlite") ||
			strings.HasSuffix(name, "-restorefailed.sqlite") {
			continue
		}
		if hasKeepMarker(filepath.Join(o.SnapshotDir, name)) {
			continue
		}
		m := failedSnapshotName.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		span := snapshotSpan.FindStringSubmatch(m[1])
		if span == nil {
			continue
		}
		seq := 0
		if m[3] != "" {
			seq, _ = strconv.Atoi(m[3])
		}
		// 分组键带轨名组合：m[1] 去掉“-v几-to-v几”剩下的 <前缀><轨名> 就是这一段的身份。
		tracks := strings.TrimSuffix(m[1], span[0])
		key := tracks + "|v" + span[1] + "-to-v" + span[2]
		bySpan[key] = append(bySpan[key], failedSnap{name: name, stamp: m[2], seq: seq})
	}
	var removed []string
	for _, snaps := range bySpan {
		sort.Slice(snaps, func(i, j int) bool {
			if snaps[i].stamp != snaps[j].stamp {
				return snaps[i].stamp > snaps[j].stamp
			}
			if snaps[i].seq != snaps[j].seq {
				return snaps[i].seq > snaps[j].seq
			}
			return snaps[i].name > snaps[j].name
		})
		for _, s := range snaps[1:] {
			path := filepath.Join(o.SnapshotDir, s.name)
			if err := o.remove(path); err == nil {
				removed = append(removed, path)
			}
		}
	}
	sort.Strings(removed)
	return removed
}

// restore 照规格第 4 条换回：快照复制到同目录临时文件、落盘、改名覆盖，删掉 -wal/-shm/-journal，
// 再用只读连接验 integrity_check、每条轨的 goose 号、sumika_schema 和迁移前一致。
func (o Options) restore(ctx context.Context, file, snap string, tracks []Track, before State) error {
	ctx = context.WithoutCancel(ctx) // 换回和验证不受取消影响（规格第 3 条第 6 步第 4 小点）
	tmp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".restore-*")
	if err != nil {
		return fmt.Errorf("建换回临时文件：%w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // 改名成功后这里是空操作

	in, err := os.Open(snap)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("打开快照：%w", err)
	}
	_, copyErr := io.Copy(tmp, in)
	in.Close()
	if copyErr == nil {
		copyErr = tmp.Sync()
	}
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return fmt.Errorf("复制快照：%w", copyErr)
	}
	if err := o.rename(tmpPath, file); err != nil {
		return fmt.Errorf("改名覆盖原库：%w", err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		path := file + suffix
		if err := o.remove(path); err != nil {
			return fmt.Errorf("主文件已换回、旁文件 %s 残留：%w", absPath(path), err)
		}
	}
	return verifyRestored(ctx, file, tracks, before)
}

// verifyRestored 用 immutable 只读连接验换回后的库：不拿锁、不建旁文件。
func verifyRestored(ctx context.Context, file string, tracks []Track, before State) error {
	db, err := openImmutable(file)
	if err != nil {
		return fmt.Errorf("打开换回后的库：%w", err)
	}
	defer db.Close()

	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("换回后 integrity_check：%w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("换回后 integrity_check = %q", integrity)
	}
	after, err := probeConn(ctx, file, db, tracks)
	if err != nil {
		return fmt.Errorf("换回后探查：%w", err)
	}
	if before.HasSchema != after.HasSchema {
		return fmt.Errorf("换回后 %s 在不在和迁移前不同", SchemaTable)
	}
	if !reflect.DeepEqual(before.SchemaRows, after.SchemaRows) {
		return fmt.Errorf("换回后的 %s 和迁移前不同", SchemaTable)
	}
	want := make(map[string]int64, len(before.Tracks))
	for _, ts := range before.Tracks {
		want[ts.Name] = ts.Applied
	}
	for _, ts := range after.Tracks {
		if got, ok := want[ts.Name]; ok && ts.Applied != got {
			return fmt.Errorf("换回后迁移轨 %s 是 %d，迁移前是 %d", ts.Name, ts.Applied, got)
		}
	}
	return nil
}

func dbFootprint(file string) (uint64, error) {
	fi, err := os.Stat(file)
	if err != nil {
		return 0, err
	}
	size := uint64(fi.Size())
	wi, err := os.Stat(file + "-wal")
	if err == nil {
		size += uint64(wi.Size())
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	return size, nil
}

func observedTracks(st State, tracks []Track) []TrackReport {
	byName := make(map[string]TrackState, len(st.Tracks))
	for _, ts := range st.Tracks {
		byName[ts.Name] = ts
	}
	out := make([]TrackReport, 0, len(tracks))
	for _, t := range tracks {
		ts := byName[t.Name]
		out = append(out, TrackReport{Track: t.Name, From: ts.Applied, To: ts.Applied})
	}
	return out
}

func createdTracks(tracks []Track) []TrackReport {
	out := make([]TrackReport, 0, len(tracks))
	for _, t := range tracks {
		latest, err := t.Latest()
		if err != nil {
			continue
		}
		out = append(out, TrackReport{Track: t.Name, From: 0, To: latest})
	}
	return out
}

func migratedTracks(st State, tracks []Track, pending map[string]PendingTrack) []TrackReport {
	byName := make(map[string]TrackState, len(st.Tracks))
	for _, ts := range st.Tracks {
		byName[ts.Name] = ts
	}
	out := make([]TrackReport, 0, len(tracks))
	for _, t := range tracks {
		from := byName[t.Name].Applied
		to := from
		if p, ok := pending[t.Name]; ok {
			to = p.To
		}
		out = append(out, TrackReport{Track: t.Name, From: from, To: to})
	}
	return out
}

func tooNewError(file string, dec Decision, st State) *Unavailable {
	var details []string
	gooseAhead := false
	for _, tn := range dec.TooNew {
		if tn.GooseAhead {
			gooseAhead = true
			details = append(details, fmt.Sprintf(
				"迁移轨 %s 的 goose 版本 %d 比版本表记的 %d 和本程序认识的 %d 都新",
				tn.Track, tn.Applied, tn.SchemaVersion, tn.Latest))
			continue
		}
		details = append(details, fmt.Sprintf(
			"迁移轨 %s 要求程序至少到 %d，本程序只认识 %d", tn.Track, tn.MinReader, tn.Latest))
	}
	var by string
	for _, ts := range st.Tracks {
		if ts.Schema != nil && ts.Schema.MigratedBy != "" {
			by = ts.Schema.MigratedBy
			break
		}
	}
	prefix := "这个库比本程序新，请用新版本打开；"
	if gooseAhead {
		prefix = "版本表落后于 goose 的迁移号（很可能较新的 Sumika 迁完后没来得及写版本表），请用新版本打开；"
	} else if by != "" {
		prefix = fmt.Sprintf("这个库被较新的 Sumika（%q）升级过，请用新版本打开；", migratedByForMessage(by))
	}
	return &Unavailable{File: file, Track: dec.TooNew[0].Track, Reason: ReasonTooNew, Detail: prefix + strings.Join(details, "；")}
}

// migratedByForMessage 把库里的 migrated_by 放进给人看的消息前截到 64 个字符，
// 再由调用方用 %q 转义：这一列来自别的程序写的库，不能原样拼进日志。
func migratedByForMessage(by string) string {
	const limit = 64
	r := []rune(by)
	if len(r) > limit {
		r = r[:limit]
	}
	return string(r)
}

func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
