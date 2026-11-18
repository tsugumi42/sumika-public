package storagestat

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/paths"
)

// liveBudget 是打开页面时现量一个小类别最多花的时间，超了就退回快照的数字
const liveBudget = 1500 * time.Millisecond

// ErrScanning 是已经有一次统计在跑
var ErrScanning = errors.New("已经有一次统计在进行")

// Options 是 Service 的选项，零值可用
type Options struct {
	Store Store            // nil：不存快照（只给测试和一次性量）
	Extra func() Extra     // 数据目录之外的位置（配置里的），nil 当没有
	Now   func() time.Time // nil 用 time.Now
	Watch bool             // 为真就登记媒体新增字节的观察者（长期运行的后台用）；一次性只读实例不要开
}

type delta struct {
	bytes, files int64
	plat         map[string][2]int64
}

// Service 统计一个数据目录的占用。可以同时被几处用
type Service struct {
	p    paths.Paths
	opt  Options
	live bool

	mu       sync.Mutex
	loaded   bool
	snap     *Snapshot
	delta    delta
	resolved string // 数据根解开联接后的真实路径
	cancelFn context.CancelFunc
	done     chan struct{}
	unwatch  func()
	closed   bool

	beforeSave func() // 只给测试：走完目录、检查取消之前插一手

	running atomic.Bool
	entries atomic.Int64
	startMS atomic.Int64
}

// New 建服务。Watch 为真时开始累计媒体增量，之后要 Close（把"快照加增量"写回）
func New(p paths.Paths, o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Service{p: p, opt: o, live: o.Watch}
	if r, err := realPath(p.Root); err == nil {
		s.resolved = r
	} else {
		s.resolved = p.Root
	}
	if o.Watch {
		s.unwatch = mediastore.OnNewFile(s.onNewFile)
	}
	return s
}

func (s *Service) sameRoot(data string) bool {
	k := lowerKey(data)
	if k == lowerKey(s.p.Root) || k == lowerKey(s.resolved) {
		return true
	}
	if r, err := realPath(data); err == nil {
		return lowerKey(r) == lowerKey(s.resolved)
	}
	return false
}

func (s *Service) onNewFile(n mediastore.NewFile) {
	if n.Bytes <= 0 || !s.sameRoot(n.Data) {
		return
	}
	s.AddMedia(n.Platform, n.Bytes, 1)
}

// AddMedia 记一次媒体新增（Put 落下新文件时由观察者调；同哈希已存在的不会走到这里）
func (s *Service) AddMedia(platform string, bytes, files int64) { s.bump(platform, bytes, files) }

// SubMedia 记一次媒体减少（清理动作删了文件时调，第 6 步用）
func (s *Service) SubMedia(platform string, bytes, files int64) { s.bump(platform, -bytes, -files) }

func (s *Service) bump(platform string, bytes, files int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delta.bytes += bytes
	s.delta.files += files
	if s.delta.plat == nil {
		s.delta.plat = map[string][2]int64{}
	}
	v := s.delta.plat[platform]
	s.delta.plat[platform] = [2]int64{v[0] + bytes, v[1] + files}
}

func (d delta) clone() delta {
	c := delta{bytes: d.bytes, files: d.files}
	if d.plat != nil {
		c.plat = make(map[string][2]int64, len(d.plat))
		for k, v := range d.plat {
			c.plat[k] = v
		}
	}
	return c
}

// sub 减去一份旧的基线（扫描开始时的增量，已经被这次扫描量进去了）
func (d *delta) sub(b delta) {
	d.bytes -= b.bytes
	d.files -= b.files
	for k, v := range b.plat {
		c := d.plat[k]
		d.plat[k] = [2]int64{c[0] - v[0], c[1] - v[1]}
	}
}

func clamp0(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// loadSnap 第一次要用时从 Store 读；读不到当没有（错误不挡住现量的部分）
func (s *Service) loadSnap(ctx context.Context) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded && s.opt.Store != nil {
		if sn, err := s.opt.Store.Latest(ctx); err == nil {
			s.snap = sn
			s.loaded = true
		}
	}
	return s.snap
}

func (s *Service) extra() Extra {
	var x Extra
	if s.opt.Extra != nil {
		x = s.opt.Extra()
	}
	if x.BackupDir == "" {
		x.BackupDir = s.p.BackupRoot()
	}
	return x
}

// plan 是这一次量的位置：类别表、每类的根、被认领的路径
type plan struct {
	rp      paths.Paths
	cats    []category
	roots   [][]string
	claimed map[string]bool
}

func (s *Service) plan() plan {
	rp := paths.Paths{Root: s.resolved}
	x := s.extra()
	cats := categories()
	pl := plan{rp: rp, cats: cats, roots: make([][]string, len(cats)), claimed: map[string]bool{}}
	inside := lowerKey(rp.Root) + string(filepath.Separator)
	for i, c := range cats {
		for _, r := range c.roots(rp, x) {
			if !strings.HasPrefix(lowerKey(r)+string(filepath.Separator), inside) {
				// 数据目录之外的位置是用户配的根，先解开联接再量；解不开就照原样（量的时候会发现它不在）
				if real, err := realPath(r); err == nil {
					r = real
				}
			}
			pl.roots[i] = append(pl.roots[i], r)
			pl.claimed[lowerKey(r)] = true
		}
	}
	return pl
}

func (pl plan) measure(ctx context.Context, w *walker, i int) (CatSnap, error) {
	c := pl.cats[i]
	var out walkOut
	rootsAbsent := 0
	for _, r := range pl.roots[i] {
		o, err := w.walk(ctx, r, pl.claimed, c.itemOf)
		if err != nil {
			return CatSnap{}, err
		}
		if o.absent {
			rootsAbsent++
		}
		out.merge(o)
	}
	return CatSnap{Bytes: out.bytes, Files: out.files, Links: out.links, Errors: out.errs,
		Absent: rootsAbsent == len(pl.roots[i]), Items: out.itemList()}, nil
}

func (pl plan) measureOther(ctx context.Context, w *walker) (CatSnap, error) {
	o, err := w.walk(ctx, pl.rp.Root, pl.claimed, nil)
	if err != nil {
		return CatSnap{}, err
	}
	return CatSnap{Bytes: o.bytes, Files: o.files, Links: o.links, Errors: o.errs, Absent: o.absent}, nil
}

// Storage 给存储标签的数据：小类别现量（超时就退回快照），大类别用上次后台统计的快照，媒体再加上之后的新增。
// 不触发扫描，不遍历大目录。没统计过的大类别 Source 是 none，报告的 NeverScanned 为真
func (s *Service) Storage(ctx context.Context) (ctlproto.StorageReport, error) {
	snap := s.loadSnap(ctx)
	pl := s.plan()
	s.mu.Lock()
	d := s.delta.clone()
	s.mu.Unlock()
	now := s.opt.Now().UnixMilli()
	rep := ctlproto.StorageReport{NeverScanned: snap == nil, Live: s.live, Categories: make([]ctlproto.StorageCategory, 0, len(pl.cats)+1)}
	if snap != nil {
		rep.ScannedAtMS, rep.ScanMs = snap.ScannedAtMS, snap.ScanMs
	}
	rep.Scan = s.scanState()
	for i, c := range pl.cats {
		cat := ctlproto.StorageCategory{Key: c.key, Label: c.label, Rule: c.rule, Auto: c.auto, Source: ctlproto.SourceNone}
		var have CatSnap
		haveSnap := false
		if snap != nil {
			have, haveSnap = snap.Cats[c.key]
		}
		fromSnap := func() {
			cat.Bytes, cat.Files, cat.Links, cat.Errors, cat.Absent, cat.Items = have.Bytes, have.Files, have.Links, have.Errors, have.Absent, have.Items
			cat.Source, cat.AsOfMS, cat.Approx = ctlproto.SourceSnapshot, snap.ScannedAtMS, true
		}
		if !c.big {
			lctx, cancel := context.WithTimeout(ctx, liveBudget)
			cs, err := pl.measure(lctx, &walker{}, i)
			cancel()
			switch {
			case err == nil:
				cat.Bytes, cat.Files, cat.Links, cat.Errors, cat.Absent, cat.Items = cs.Bytes, cs.Files, cs.Links, cs.Errors, cs.Absent, cs.Items
				cat.Source, cat.AsOfMS = ctlproto.SourceLive, now
			case haveSnap:
				fromSnap()
			default:
				cat.Errors = 1
			}
		} else if haveSnap {
			fromSnap()
			if c.key == ctlproto.StoreMedia && (d.bytes != 0 || d.files != 0) {
				cat.Bytes = clamp0(cat.Bytes + d.bytes)
				cat.Files = clamp0(cat.Files + d.files)
				cat.Items = foldItems(cat.Items, d.plat)
				cat.Source = ctlproto.SourceDelta
				rep.DeltaBytes = d.bytes
			}
		}
		rep.Categories = append(rep.Categories, cat)
	}
	other := ctlproto.StorageCategory{Key: ctlproto.StoreOther, Label: otherLabel, Rule: otherRule, Source: ctlproto.SourceNone}
	if snap != nil {
		if o, ok := snap.Cats[ctlproto.StoreOther]; ok {
			other.Bytes, other.Files, other.Links, other.Errors = o.Bytes, o.Files, o.Links, o.Errors
			other.Source, other.AsOfMS, other.Approx = ctlproto.SourceSnapshot, snap.ScannedAtMS, true
		}
	}
	rep.Categories = append(rep.Categories, other)
	for _, c := range rep.Categories {
		rep.Total += c.Bytes
	}
	return rep, nil
}

func foldItems(items []ctlproto.StorageItem, plat map[string][2]int64) []ctlproto.StorageItem {
	out := make([]ctlproto.StorageItem, len(items))
	copy(out, items)
	for name, v := range plat {
		found := false
		for i := range out {
			if out[i].Name == name {
				out[i].Bytes, out[i].Files = clamp0(out[i].Bytes+v[0]), clamp0(out[i].Files+v[1])
				found = true
			}
		}
		if !found && (v[0] > 0 || v[1] > 0) {
			out = append(out, ctlproto.StorageItem{Name: name, Bytes: clamp0(v[0]), Files: clamp0(v[1])})
		}
	}
	return out
}

func (s *Service) scanState() ctlproto.StorageScan {
	if !s.running.Load() {
		return ctlproto.StorageScan{}
	}
	return ctlproto.StorageScan{Running: true, Entries: s.entries.Load(), StartMS: s.startMS.Load()}
}

// Rescan 在后台开始一次完整统计，马上返回；已经在统计返回 ErrScanning。
// 统计不跟着调用方的 ctx 走（离开页面不取消），要停用 CancelScan 或 Close
func (s *Service) Rescan(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("存储统计已经关闭")
	}
	if s.running.Load() {
		s.mu.Unlock()
		return ErrScanning
	}
	sctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.cancelFn, s.done = cancel, done
	s.running.Store(true)
	s.mu.Unlock()
	go func() {
		defer close(done)
		defer cancel()
		_ = s.scan(sctx)
	}()
	return nil
}

// CancelScan 取消正在跑的统计；取消后不写任何行
func (s *Service) CancelScan() {
	s.mu.Lock()
	c := s.cancelFn
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

// Scan 在调用方的协程里做一次完整统计（后台协程和测试都走它）。ctx 取消就返回 ctx 的错误，什么都不写
func (s *Service) Scan(ctx context.Context) error {
	s.mu.Lock()
	if s.running.Load() {
		s.mu.Unlock()
		return ErrScanning
	}
	s.running.Store(true)
	s.mu.Unlock()
	return s.scan(ctx)
}

// scan 要求调用前 running 已经置位
func (s *Service) scan(ctx context.Context) error {
	defer s.running.Store(false)
	restore := lowerPriority()
	defer restore()
	start := s.opt.Now()
	s.startMS.Store(start.UnixMilli())
	s.entries.Store(0)
	s.mu.Lock()
	base := s.delta.clone()
	s.mu.Unlock()
	pl := s.plan()
	w := &walker{progress: &s.entries}
	snap := Snapshot{Kind: KindScan, ScannedAtMS: start.UnixMilli(), Cats: map[string]CatSnap{}}
	for i, c := range pl.cats {
		cs, err := pl.measure(ctx, w, i)
		if err != nil {
			return err
		}
		snap.Cats[c.key] = cs
	}
	other, err := pl.measureOther(ctx, w)
	if err != nil {
		return err
	}
	snap.Cats[ctlproto.StoreOther] = other
	ms := s.opt.Now().Sub(start).Milliseconds()
	snap.ScanMs = ms
	if s.beforeSave != nil {
		s.beforeSave()
	}
	if err := ctx.Err(); err != nil { // 取消在写之前：不写
		return err
	}
	if s.opt.Store != nil {
		// 开始写以后就让它写完（一条 INSERT，不会有半行），不跟着 ctx 半路取消
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err := s.opt.Store.Save(wctx, snap, start.UnixMilli(), ms)
		cancel()
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.snap, s.loaded = &snap, true
	if s.delta.plat == nil {
		s.delta.plat = map[string][2]int64{}
	}
	s.delta.sub(base) // 扫描开始前的增量已经量进去了；扫描期间新落的留着（可能重复计一次，下次重扫修正）
	s.mu.Unlock()
	return nil
}

// flush 把"快照加增量"写成新的一行并清零增量。没有快照（从没统计过）时增量没有意义，什么都不写
func (s *Service) flush(ctx context.Context) error {
	s.mu.Lock()
	snap, d := s.snap, s.delta.clone()
	s.mu.Unlock()
	if snap == nil || s.opt.Store == nil || (d.bytes == 0 && d.files == 0) {
		return nil
	}
	folded := Snapshot{Kind: KindFlush, ScannedAtMS: snap.ScannedAtMS, ScanMs: snap.ScanMs, Cats: make(map[string]CatSnap, len(snap.Cats))}
	for k, v := range snap.Cats {
		folded.Cats[k] = v
	}
	m := folded.Cats[ctlproto.StoreMedia]
	m.Bytes, m.Files = clamp0(m.Bytes+d.bytes), clamp0(m.Files+d.files)
	m.Items = foldItems(m.Items, d.plat)
	m.Absent = false
	folded.Cats[ctlproto.StoreMedia] = m
	if err := s.opt.Store.Save(ctx, folded, s.opt.Now().UnixMilli(), 0); err != nil {
		return err
	}
	s.mu.Lock()
	s.snap = &folded
	s.delta.sub(d)
	s.mu.Unlock()
	return nil
}

// Flush 现在就把"快照加增量"写回（退出时和重扫后会自动调）
func (s *Service) Flush(ctx context.Context) error { return s.flush(ctx) }

// Close 取消正在跑的统计、等它收尾、把"快照加增量"写回、撤掉观察者
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	c, done, unwatch := s.cancelFn, s.done, s.unwatch
	s.mu.Unlock()
	if unwatch != nil {
		unwatch()
	}
	if c != nil {
		c()
	}
	if done != nil {
		<-done
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.flush(ctx)
}
