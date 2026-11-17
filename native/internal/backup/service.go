package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/fileatomic"
	"sumika-native/internal/paths"
	"sumika-native/internal/sqlitesnap"
)

// renameBudget：把做完的 .partial 改成正式名字时，被杀毒软件、预览程序短暂占着的最长等多久（Windows 上常见，几秒不够）
const renameBudget = 30 * time.Second

// ErrClosed：备份服务已经关了（程序在退出）
var ErrClosed = errors.New("备份服务已经关了")

// ErrBusy：已经有一份备份在做（每天的调度和手动备份共用同一把锁，第二个不排队）
var ErrBusy = errors.New("已经有一份备份在做")

// DBSource 是持有库连接的一方登记的快照：Rel 是库文件相对数据目录的路径（斜杠，由 paths 算出来），
// Snapshot 把一份一致的快照写到 dst（不存在的新文件）。快照怎么取由持有方定（一般是 sqlitesnap.Into 用它自己开着的连接）。
type DBSource struct {
	Rel      string
	Snapshot func(ctx context.Context, dst string) error
}

// StickerSource 说一个媒体目录里哪些文件是表情包和头像：从聊天库 DBRel 的副本里取哈希，文件名正好是哈希
type StickerSource struct {
	MediaRel string // 媒体目录（相对数据目录），如 go/media/qq
	DBRel    string // 对应的聊天库
	Hashes   func(ctx context.Context, db *sql.DB) ([]string, error)
}

// Contribution 是一方登记的东西。每次备份开始时才调登记的函数，晚开的库、换过的连接取的是最新的
type Contribution struct {
	DBs      []DBSource
	Stickers []StickerSource
	// WriteProbes 是各库的写锁探针：备份跑着时周期性地探，量备份期间入库要等多久（writewait.go，规格 10.10.6）
	WriteProbes []WriteProbe
}

// Options 是 Service 的配置
type Options struct {
	Paths    paths.Paths
	Dir      string  // backup.dir 原样；空用数据目录旁边的 Sumika-备份
	TempData bool    // SUMIKA_DATA_DIR 有值：一律放数据目录旁边
	Media    string  // backup.dailyMedia：none | stickers | all（空按 stickers）
	Daily    bool    // backup.daily
	AlertGB  float64 // backup.alertGB：总占用超过它记警告；<=0 不提醒
	Version  string  // 程序版本（提交号），写进清单
	// TempRoots 是“临时目录”的位置：数据目录的真实路径落在其中任何一个里面，就当临时数据目录，备份一律放在数据目录旁边，
	// 不管配置里的 backup.dir 写的是哪（-data 指到临时目录、配置却读到真实 backup.dir 时，临时数据不能混进真实的备份目录）。
	// nil = 系统临时目录（os.TempDir()）；空切片 = 没有（只给测试用）。TempData 为真（SUMIKA_DATA_DIR 有值）时不看这个
	TempRoots []string
	Source    string // 备份归属的数据目录标识；空 = 数据目录的真实路径（Windows 上小写）。别的数据目录写进同一个备份目录的备份，不参与轮换、“最近一份”和硬链接

	Now        func() time.Time
	Logf       func(format string, args ...any)
	Warnf      func(format string, args ...any)
	Busy       func() bool   // 现在有没有角色正在回复；nil 当没有
	StartDelay time.Duration // 启动后多久第一次看；0 = 2 分钟，负数 = 不等
	Every      time.Duration // 之后多久看一次；0 = 1 小时
	ProbeEvery time.Duration // 备份跑着时多久探一次写锁等待；0 = 250 毫秒
	Drill      bool          // 每周抽验（规格 10.10.4）：调度里每周把最新一份好备份恢复到临时目录核对一遍；装配时跟着 backup.daily

	RunMax       time.Duration // 一份备份最多做多久，超过就取消并记失败；0 = 6 小时（卡在一个读不动的文件上不能一直占着）
	CloseWait    time.Duration // Close 最多等正在做的那份收尾多久；0 = 30 秒
	MinFreeBytes int64         // 备份前目标盘至少要剩多少；还要至少是预计大小的 2 倍，取大的。0 = 5 GiB，负数不查
}

// Request 是一次备份的要求
type Request struct {
	Reason Reason
	Label  string // 改动前备份的标签（只认字母数字、中文、连字符）
	Media  string // 空用配置里的
}

// Result 是一次备份的结果
type Result struct {
	Name     string
	Folder   string
	Status   Status
	Reason   Reason
	Bytes    int64
	Files    int
	Linked   int
	Problems []string
}

// Service 是备份服务：宿主装配一个，持有库的一方登记快照，调度和 ctl 都调它
type Service struct {
	opt Options

	regMu sync.Mutex
	reg   []func() Contribution

	run          sync.Mutex // 一次只做一份
	staging      string     // 正在做的这份的 .partial 文件夹（受 mu 保护）：panic 以后要把它清掉
	overrideOnce sync.Once
	closed       atomic.Bool

	mu    sync.Mutex
	state State
	// 抽验记录（最近 drillKeep 条，旧的在前；受 mu 保护）：第一次 refresh 时从备份目录里的 .drill.json 读进来，之后只在内存里加、写回文件
	drills       []DrillRecord
	drillsLoaded bool
	sweepOnce    sync.Once // 进程里第一次 Tick 清一次上次留下的抽验副本

	life   sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	runCtx context.CancelFunc // 正在做的那份的取消

	// 测试可换：建硬链接、复制文件、写清单、改名
	link          func(oldname, newname string) error
	copyFile      func(ctx context.Context, src, dst string) (int64, string, error)
	writeManifest func(folder string, m Manifest) error
	rename        func(from, to string) error
	replace       func(from, to string, budget time.Duration) error // 改名用的底层函数（测试可换，看传进去的重试上限）
	onWait        func(d time.Duration)                             // 测试用：Close、Stop 每次决定等多久时调一下
	inspect       func(ctx context.Context, file string) (sqlitesnap.Report, error)
	// 恢复和抽验（restore.go、drill.go）用的，测试可换
	restoreCopy func(ctx context.Context, src, dst string) (int64, string, error) // 恢复时复制一个文件，交回大小和源端哈希
	free        func(path string) (int64, bool)                                   // 目标盘剩余空间
	probeStop   time.Duration                                                     // 备份做完后最多等写锁探测的协程收尾多久；0 = 2 秒（writewait.go）
	maxPathLen  int                                                               // 恢复出的路径最长多少个字符；0 = 不限（Windows 默认 259）
}

// State 是调度器缓存的状态（自检、状态行读它，不扫目录）
type State struct {
	Enabled      bool      // backup.daily
	Checked      bool      // 调度器已经看过一次备份目录
	Running      bool      // 现在有一份在做
	RunningSince time.Time // 这份是什么时候开始做的（自检看它：做得太久记异常）
	Deferred     bool      // 到点了，但有角色在回复，先等着（最多等到 BusyCap）
	HaveGood     bool      // 有没有校验通过的每天备份（时间不晚于现在的）
	LastGood     time.Time // 其中最新的一份的时间
	LastSkipped  int       // 其中最新的一份跳过了几个媒体（链接、读不了）
	LastFailure  string    // 这个进程里最近一次每天备份失败的原因（之后成功了就清空）
	FailedAt     time.Time
	Started      time.Time     // 服务建好的时间
	Drill        DrillState    // 每周抽验的状态
	RunMax       time.Duration // 一份备份最多做多久（Options.RunMax，配置 backup.runMaxHours），自检按它算“做太久”
}

// New 建服务。不起任何协程；调度用 Start
func New(opt Options) *Service {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.Warnf == nil {
		opt.Warnf = opt.Logf
	}
	if opt.Media == "" {
		opt.Media = MediaStickers
	}
	if opt.RunMax <= 0 {
		opt.RunMax = 6 * time.Hour
	}
	if opt.CloseWait <= 0 {
		opt.CloseWait = 30 * time.Second
	}
	if opt.MinFreeBytes == 0 {
		opt.MinFreeBytes = 5 << 30
	}
	s := &Service{opt: opt, link: os.Link, copyFile: copyVerified, writeManifest: writeManifest, inspect: sqlitesnap.Inspect,
		replace: fileatomic.Replace, restoreCopy: copyHashed, free: freeSpace, probeStop: defaultProbeStopWait}
	if runtime.GOOS == "windows" {
		s.maxPathLen = 259
	}
	s.rename = func(from, to string) error { return s.replace(from, to, renameBudget) }
	s.state.Enabled = opt.Daily
	s.state.RunMax = opt.RunMax
	s.state.Started = opt.Now()
	return s
}

// Limits 是这个服务实际用的几个限制（配置 backup.runMaxHours、closeWaitSeconds、minFreeGB 经装配传下来的结果），装配测试看它
func (s *Service) Limits() (runMax, closeWait time.Duration, minFreeBytes int64) {
	return s.opt.RunMax, s.opt.CloseWait, s.opt.MinFreeBytes
}

// Register 登记一方的快照和表情包来源
func (s *Service) Register(f func() Contribution) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	s.reg = append(s.reg, f)
}

func (s *Service) contributions() Contribution {
	s.regMu.Lock()
	fs := append([]func() Contribution(nil), s.reg...)
	s.regMu.Unlock()
	var all Contribution
	for _, f := range fs {
		c := f()
		all.DBs = append(all.DBs, c.DBs...)
		all.Stickers = append(all.Stickers, c.Stickers...)
		all.WriteProbes = append(all.WriteProbes, c.WriteProbes...)
	}
	return all
}

// dataIsTemp：数据目录的真实路径在某个临时目录里面
func (s *Service) dataIsTemp() bool {
	roots := s.opt.TempRoots
	if roots == nil {
		roots = []string{os.TempDir()}
	}
	data, err := realExisting(s.opt.Paths.Root)
	if err != nil {
		return false
	}
	for _, r := range roots {
		if rr, err := realExisting(r); err == nil && within(rr, data) {
			return true
		}
	}
	return false
}

// Dir 是现在的备份目录（ResolveDir 的结果）：数据目录是临时目录（SUMIKA_DATA_DIR 有值，或真实路径在系统临时目录下）时，
// 配置里的 backup.dir 被盖掉、改放数据目录旁边，这时记一条 Warn（只记一次），免得主人以为备份去了配置里写的地方
func (s *Service) Dir() (string, error) {
	temp := s.opt.TempData || s.dataIsTemp()
	dir, err := ResolveDir(s.opt.Paths, s.opt.Dir, temp)
	if err == nil && temp && strings.TrimSpace(s.opt.Dir) != "" {
		s.overrideOnce.Do(func() {
			s.opt.Warnf("数据目录是临时目录（SUMIKA_DATA_DIR 有值，或在系统临时目录下）：备份放在数据目录旁边的 %s，不用配置里的 backup.dir（%s）", dir, s.opt.Dir)
		})
	}
	return dir, err
}

// Status 是调度器缓存的状态
func (s *Service) Status() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// source 是这个服务写进清单、也只认这个值的数据目录标识
func (s *Service) source() string {
	if s.opt.Source != "" {
		return s.opt.Source
	}
	if real, err := paths.RealPath(s.opt.Paths.Root); err == nil {
		return lower(filepath.Clean(real))
	}
	return lower(filepath.Clean(s.opt.Paths.Root))
}

// ours 留下属于这个数据目录的备份。备份目录是配置里写的，临时数据目录的副本带着真实的 backup.dir 跑起来时，
// 它写进去的备份不能拿来当“最近一份”，更不能被它的轮换删掉真实的备份
func (s *Service) ours(es []Entry) []Entry {
	src := s.source()
	out := es[:0:0]
	for _, e := range es {
		if e.Manifest.Source == src {
			out = append(out, e)
		}
	}
	return out
}

// List 列出备份目录里属于这个数据目录的备份（含没做完的），新的在前
func (s *Service) List() ([]Entry, error) {
	dir, err := s.Dir()
	if err != nil {
		return nil, err
	}
	es, err := Scan(dir)
	return s.ours(es), err
}

func (s *Service) setRunning(v bool) {
	s.mu.Lock()
	s.state.Running = v
	if v {
		s.state.RunningSince = s.opt.Now()
	} else {
		s.state.RunningSince = time.Time{}
	}
	s.mu.Unlock()
}

// Run 做一份备份。同一时刻只有一份：已经有一份在做就返回 ErrBusy，不排队。
// 失败的备份不改成正式名字也不算好的；被取消的（关机）整个删掉。备份通过后跑轮换。
func (s *Service) Run(ctx context.Context, req Request) (Result, error) {
	if s.closed.Load() {
		return Result{}, ErrClosed
	}
	if !s.run.TryLock() {
		return Result{}, ErrBusy
	}
	defer s.run.Unlock()
	return s.runLocked(ctx, req)
}

// Outcome 是后台做完的一份备份的结果
type Outcome struct {
	Result Result
	Err    error
}

// Go 在后台做一份备份，立刻返回：已经有一份在做返回 ErrBusy。备份不跟调用方的 context 走（命令行断了它也该做完），关机由 Close 取消
func (s *Service) Go(req Request) (<-chan Outcome, error) {
	if s.closed.Load() {
		return nil, ErrClosed
	}
	if !s.run.TryLock() {
		return nil, ErrBusy
	}
	ch := make(chan Outcome, 1)
	go func() {
		defer s.run.Unlock()
		res, err := s.runLocked(context.Background(), req)
		ch <- Outcome{res, err}
	}()
	return ch, nil
}

func (s *Service) runLocked(ctx context.Context, req Request) (res Result, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.opt.RunMax)
	s.life.Lock()
	s.runCtx = cancel
	// Close 先记 closed 再取消正在做的那份：这里登记完取消函数再看一眼 closed，两边总有一边看得到另一边，不会有一份漏过去没被取消
	if s.closed.Load() {
		cancel()
	}
	s.life.Unlock()
	defer func() {
		s.life.Lock()
		s.runCtx = nil
		s.life.Unlock()
		cancel()
	}()
	s.setRunning(true)
	defer s.setRunning(false)

	func() {
		defer func() {
			if r := recover(); r != nil { // 备份里出了意外（库驱动、解析……）：记成这次备份失败，不能带垮整个后台；做到一半的 .partial 整份清掉
				err = fmt.Errorf("备份时出了意外：%v", r)
				s.mu.Lock()
				st := s.staging
				s.mu.Unlock()
				if st != "" {
					_ = removeTree(st)
				}
			}
		}()
		s.mu.Lock()
		s.staging = ""
		s.mu.Unlock()
		res, err = s.doRun(ctx, req)
	}()
	if errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("这份备份做了超过 %v 还没做完，已经取消（卡在读不动的文件上？）：%w", s.opt.RunMax, err)
	}
	s.mu.Lock()
	if req.Reason == ReasonDaily {
		if err != nil {
			s.state.LastFailure, s.state.FailedAt = err.Error(), s.opt.Now()
		} else {
			s.state.LastFailure, s.state.FailedAt = "", time.Time{}
		}
	}
	s.mu.Unlock()
	s.refresh()
	return res, err
}

func validLabel(l string) bool {
	if l == "" || len([]rune(l)) > 40 {
		return false
	}
	for _, r := range l {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r >= 0x4e00 && r <= 0x9fff:
		default:
			return false
		}
	}
	return true
}

var reasonWord = map[Reason]string{ReasonDaily: "每天", ReasonManual: "手动", ReasonPreChange: "改动前"}

func (s *Service) doRun(ctx context.Context, req Request) (Result, error) {
	now := s.opt.Now()
	if _, ok := reasonWord[req.Reason]; !ok {
		return Result{}, fmt.Errorf("不认识的备份原因 %q", req.Reason)
	}
	if req.Reason == ReasonPreChange && !validLabel(req.Label) {
		return Result{}, fmt.Errorf("改动前备份要带标签（字母、数字、中文、连字符），收到 %q", req.Label)
	}
	media := req.Media
	if media == "" {
		media = s.opt.Media
	}
	if !ValidMedia(media) {
		return Result{}, fmt.Errorf("媒体档 %q 不认识（none、stickers、all）", media)
	}
	dir, err := s.Dir()
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("备份目录建不了或不可写：%w", err)
	}
	root, err := paths.RealPath(s.opt.Paths.Root)
	if err != nil {
		return Result{}, fmt.Errorf("数据目录解不开：%w", err)
	}
	name, staging, err := newFolder(dir, req, now)
	if err != nil {
		return Result{}, fmt.Errorf("备份目录不可写：%w", err)
	}
	s.mu.Lock()
	s.staging = staging
	s.mu.Unlock()
	m := Manifest{Program: Program, Format: FormatVersion, Version: s.opt.Version, Source: s.source(), Name: name, Time: now, Reason: req.Reason, Label: req.Label,
		Status: StatusPartial, Media: media, Classes: []Class{}, DBs: []DBEntry{}, Files: []FileEntry{}}
	if err := s.writeManifest(staging, m); err != nil {
		_ = removeTree(staging)
		return Result{}, fmt.Errorf("备份目录不可写：%w", err)
	}
	b := &builder{s: s, ctx: ctx, dir: dir, root: root, staging: staging, m: &m, classes: map[Class]bool{}}
	b.build(media)

	if ctx.Err() != nil { // 关机或被取消：没做完的整个删掉，不留半成品
		_ = removeTree(staging)
		return Result{}, ctx.Err()
	}
	m.Problems = b.problems
	m.Notes = b.notes
	m.WriteWait = b.writeWait
	if line := writeWaitLog(b.writeWait); line != "" {
		s.opt.Logf("%s", line)
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	sort.Slice(m.DBs, func(i, j int) bool { return m.DBs[i].Path < m.DBs[j].Path })
	for _, f := range m.Files {
		m.Bytes += f.Size
		if f.Linked {
			m.Linked++
			m.SharedBytes += f.Size
		}
	}
	for _, c := range []Class{ClassSelf, ClassMaterial, ClassMedia} {
		if b.classes[c] {
			m.Classes = append(m.Classes, c)
		}
	}
	m.Status = StatusOK
	if len(m.Problems) > 0 {
		m.Status = StatusBad
		// 没通过校验的这份不算数，副本留着只会占地方（磁盘满时它自己就堵着下一次备份）：只留清单，写明原因
		if err := removeTree(filepath.Join(staging, "data")); err == nil {
			m.Notes = append(m.Notes, "没通过校验，备份的副本已经清掉，只留这份清单")
		}
	}
	if err := s.writeManifest(staging, m); err != nil {
		_ = removeTree(staging) // 写不了清单（多半是盘满了）：整份清掉，不留一份整库的拷贝等着堵下一次
		return Result{}, fmt.Errorf("写不了清单：%w", err)
	}
	final := filepath.Join(dir, name)
	if err := s.rename(staging, final); err != nil {
		_ = removeTree(staging) // 同上；删不掉的留给下面的清理，1 天后清掉
		return Result{}, fmt.Errorf("备份做完了但改不了名，这份已经清掉，不算数：%w", err)
	}
	res := Result{Name: name, Folder: final, Status: m.Status, Reason: m.Reason, Bytes: m.Bytes, Files: len(m.Files), Linked: m.Linked, Problems: m.Problems}

	// 轮换：只在新备份校验通过之后才清旧的；没通过也要清坏的和 .partial（不然一直失败时它们越积越多）
	if r, err := RotateCtx(ctx, dir, s.opt.Now(), m.Status != StatusOK, s.source()); err != nil {
		s.opt.Warnf("备份轮换读不了备份目录：%v", err)
	} else {
		if len(r.Deleted) > 0 {
			s.opt.Logf("备份轮换：删了 %d 份旧的（%s）", len(r.Deleted), strings.Join(r.Deleted, "、"))
		}
		for _, f := range r.Failed {
			s.opt.Warnf("备份轮换：删不掉 %s", f)
		}
	}
	s.checkUsage(ctx, dir)
	if m.Status != StatusOK {
		return res, fmt.Errorf("备份 %s 没通过校验，不算数：%s", name, strings.Join(m.Problems, "；"))
	}
	return res, nil
}

// checkUsage 每次备份（和轮换）结束时算一次实际占用（硬链接只算一次，要逐个文件取身份，备份很多时要几秒），
// 写进备份目录里的小文件 .usage.json，backup list 只读它，不再现算；超过 alertGB 记警告
func (s *Service) checkUsage(ctx context.Context, dir string) {
	used := UsageCtx(ctx, dir)
	if ctx.Err() != nil {
		return // 被取消了：这次算的不完整，不写
	}
	if err := writeUsage(dir, used, s.opt.Now(), s.source()); err != nil {
		s.opt.Warnf("备份占用写不进 %s：%v（backup list 会退回按清单估算）", usageFile, err)
	}
	if s.opt.AlertGB <= 0 {
		return
	}
	if gb := float64(used) / (1 << 30); gb > s.opt.AlertGB {
		s.opt.Warnf("备份目录占用 %.1f GB，超过 backup.alertGB（%.1f GB）：硬链接共用的文件只算一次；手动备份不会自动删，要的话自己清理", gb, s.opt.AlertGB)
	}
}

// newFolder 起一个没被占用的文件夹名，建好 <名字>.partial
func newFolder(dir string, req Request, now time.Time) (name, staging string, err error) {
	base := reasonWord[req.Reason]
	if req.Reason == ReasonPreChange {
		base += "-" + req.Label
	}
	base += "-" + now.Format("20060102-1504")
	for n := 1; n < 1000; n++ {
		name = base
		if n > 1 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			continue
		}
		staging = filepath.Join(dir, name+".partial")
		if err := os.Mkdir(staging, 0o755); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", "", err
		}
		return name, staging, nil
	}
	return "", "", errors.New("同一分钟的备份太多，起不出名字")
}

// builder 是一次备份的工作现场
type builder struct {
	s        *Service
	ctx      context.Context
	dir      string // 备份目录
	root     string // 数据目录的真实路径
	staging  string
	m        *Manifest
	classes  map[Class]bool
	prev     *linkFrom // 上一份好备份（媒体硬链接的来源）；prevDone 为真才是找过了
	prevDone bool
	problems []string
	notes    []string
	// writeWait 是备份期间各库的写锁等待统计（build 结束时填）
	writeWait []WriteWaitEntry
}

const maxProblems = 20

func (b *builder) problem(format string, args ...any) {
	if len(b.problems) < maxProblems {
		b.problems = append(b.problems, fmt.Sprintf(format, args...))
	}
}

func (b *builder) failed() bool { return b.ctx.Err() != nil || len(b.problems) >= maxProblems }

func (b *builder) dst(rel string) string {
	return filepath.Join(b.staging, "data", filepath.FromSlash(rel))
}

func (b *builder) build(media string) {
	items, skips, notes, err := collect(b.ctx, b.root)
	if err != nil {
		if b.ctx.Err() == nil {
			b.problem("走数据目录出错：%v", err)
		}
		return
	}
	b.notes = append(b.notes, notes...)
	if !b.enoughSpace(items, media) {
		return
	}
	for _, sk := range skips {
		if sk.media {
			b.m.SkippedMedia++
			if b.m.SkippedMedia <= 20 {
				b.notes = append(b.notes, "媒体 "+sk.rel+" 没带："+sk.why)
			}
			continue
		}
		// 属于她本人、素材、库的路径没法带：这份备份不完整，不能记 ok
		b.problem("%s 没法带进备份：%s", sk.rel, sk.why)
	}
	contrib := b.s.contributions()
	stopProbes := b.s.startWriteSampler(b.ctx, contrib.WriteProbes)
	defer func() { b.writeWait = stopProbes() }()
	holders := map[string]DBSource{}
	for _, d := range contrib.DBs {
		holders[lower(d.Rel)] = d
	}

	// 1. 库：取快照、校验
	for _, it := range items {
		if b.failed() {
			return
		}
		if it.kind != kindDB {
			continue
		}
		b.snapshotDB(it, holders)
	}
	// 2. 普通文件
	for _, it := range items {
		if b.failed() {
			return
		}
		if it.kind != kindFile {
			continue
		}
		b.copyOne(it)
	}
	// 3. 媒体
	b.media(items, media, contrib.Stickers)
}

// enoughSpace：备份前查目标盘剩余空间：至少要剩 MinFreeBytes，也至少是预计大小（库、文件，带全部媒体时加媒体）的 2 倍，取大的。
// 不够就不开工：写一半盘满了，留下的是一份整库的拷贝堵着下一次。查不出剩余空间（不认识的文件系统）就不拦
// estimateBytes 预计这份要新占多少盘：库、文件，带全部媒体时加媒体——能从上一份好备份里硬链接复用的媒体（同名同大小、哈希名）不占新空间，
// 不扣的话带全部媒体时第二天起每天都按全量算 2 倍，天天误报“空间不够”
func estimateBytes(items []item, media string, prev *linkFrom) int64 {
	var est int64
	for _, it := range items {
		switch {
		case it.kind == kindFile || it.kind == kindDB:
			est += it.size
		case it.kind == kindMedia && media == MediaAll:
			if prev != nil && hashName.MatchString(path.Base(it.rel)) {
				if pf, ok := prev.files[it.rel]; ok && pf.Size == it.size {
					continue
				}
			}
			est += it.size
		}
	}
	return est
}

// prevBackup 上一份校验通过的备份里的媒体文件表，一次备份里只找一次（估大小和建硬链接共用）
func (b *builder) prevBackup() *linkFrom {
	if !b.prevDone {
		b.prev, b.prevDone = b.linkSource(), true
	}
	return b.prev
}

func (b *builder) enoughSpace(items []item, media string) bool {
	min := b.s.opt.MinFreeBytes
	if min < 0 {
		return true
	}
	var prev *linkFrom
	if media == MediaAll {
		prev = b.prevBackup()
	}
	est := estimateBytes(items, media, prev)
	need := min
	if 2*est > need {
		need = 2 * est
	}
	free, ok := freeSpace(nearestExisting(b.dir))
	if !ok || free >= need {
		return true
	}
	b.problem("备份目标盘剩余空间不够：剩 %.1f GB，至少要 %.1f GB（预计这份 %.1f GB 的 2 倍，或下限 %.1f GB，取大的）；先腾地方或改 backup.dir",
		float64(free)/(1<<30), float64(need)/(1<<30), float64(est)/(1<<30), float64(min)/(1<<30))
	return false
}

func sha256File(file string) (int64, string, error) { return sha256FileCtx(context.Background(), file) }

// ctxReader 每次读之前看一眼 ctx：读很大的文件时，关机或超时能马上停下来
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func sha256FileCtx(ctx context.Context, file string) (int64, string, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, ctxReader{ctx, f})
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func (b *builder) snapshotDB(it item, holders map[string]DBSource) {
	dst := b.dst(it.rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		b.problem("建不了 %s 的目录：%v", it.rel, err)
		return
	}
	method := MethodFallback
	var err error
	if h, ok := holders[lower(it.rel)]; ok && h.Snapshot != nil {
		method = MethodHolder
		err = h.Snapshot(b.ctx, dst)
	} else {
		err = sqlitesnap.IntoFile(b.ctx, it.abs, dst)
	}
	if err != nil {
		if b.ctx.Err() == nil {
			b.problem("库 %s 取快照失败：%v", it.rel, err)
		}
		return
	}
	if method == MethodFallback {
		b.notes = append(b.notes, "库 "+it.rel+" 没有人登记快照，用只读连接兜底取的")
	}
	rep, err := b.s.inspect(b.ctx, dst)
	if err != nil {
		b.problem("库 %s 的副本打不开：%v", it.rel, err)
		return
	}
	if !rep.OK() {
		b.problem("库 %s 的副本没通过 integrity_check：%s", it.rel, rep.Integrity)
	}
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(dst + side); err == nil {
			b.problem("库 %s 的副本旁边多出了 %s", it.rel, path.Base(it.rel)+side)
		}
	}
	// 校验连接已经关了，现在才算哈希
	size, sum, err := sha256FileCtx(b.ctx, dst)
	if err != nil {
		b.problem("读不了 %s 的副本：%v", it.rel, err)
		return
	}
	b.m.DBs = append(b.m.DBs, DBEntry{Path: it.rel, Method: method, Integrity: rep.Integrity, Tables: rep.Tables})
	b.m.Files = append(b.m.Files, FileEntry{Path: it.rel, Size: size, SHA256: sum, Class: it.class})
	b.classes[it.class] = true
}

func (b *builder) copyOne(it item) {
	dst := b.dst(it.rel)
	size, sum, err := b.s.copyFile(b.ctx, it.abs, dst)
	if err != nil {
		if b.ctx.Err() == nil {
			b.problem("复制 %s 失败：%v", it.rel, err)
		}
		return
	}
	b.m.Files = append(b.m.Files, FileEntry{Path: it.rel, Size: size, SHA256: sum, Class: it.class})
	b.classes[it.class] = true
}

// copyVerified 复制 src 到 dst（目标不许已经存在），边读边算源端的 sha256；写完再读一遍副本算 sha256，两边一致才算数
func copyVerified(ctx context.Context, src, dst string) (int64, string, error) {
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
	want := hex.EncodeToString(h.Sum(nil))
	gotSize, got, err := sha256FileCtx(ctx, dst)
	if err != nil {
		_ = os.Remove(dst)
		return 0, "", err
	}
	if got != want || gotSize != n {
		_ = os.Remove(dst)
		return 0, "", fmt.Errorf("写下去以后读回来对不上（源 %s、副本 %s）", want[:12], got[:12])
	}
	return n, want, nil
}

// Close 取消正在做的那份（没做完的整个删掉）并停调度
func (s *Service) Close() {
	s.closed.Store(true)
	deadline := time.Now().Add(s.opt.CloseWait) // 停调度和等这份收尾共用一个期限，Close 总共最多等 CloseWait
	s.stopWithin(time.Until(deadline))
	s.life.Lock()
	c := s.runCtx
	s.life.Unlock()
	if c != nil {
		c()
	}
	// 等它收完尾，但不无限等：它卡在一个读不动的文件上时，退出不能跟着卡死。和停调度共用一个期限
	remain := max(time.Until(deadline), 0)
	if s.onWait != nil {
		s.onWait(remain)
	}
	done := make(chan struct{})
	go func() {
		s.run.Lock()
		s.run.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(remain):
		s.opt.Warnf("备份没能在 %v 内收尾（可能卡在读不动的文件上），不再等它", s.opt.CloseWait)
	}
}
