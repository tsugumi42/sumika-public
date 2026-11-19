package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"sumika-native/internal/paths"
)

// 每周抽验（规格-数据目录与备份 10.10.4）：拿最新一份校验通过的备份，真的恢复到备份目录旁边的临时目录、跑同一套核对、
// 删掉这个临时目录、把结果记下来。备份没演练过恢复就不能算有备份。

const (
	// DrillEvery：最近一次通过的抽验在这么久以内就不再做
	DrillEvery = 7 * 24 * time.Hour
	// DrillRetryAfter：最近一次尝试失败以后至少隔这么久才再试（每小时复制几个 GB 的盘不能这样用）
	DrillRetryAfter = 24 * time.Hour
	// DrillBusyCap：有角色在回复时先等；距上一次通过超过这么久就不再等（和每天备份的 BusyCap 同一个道理）
	DrillBusyCap = 9 * 24 * time.Hour
	// DrillRootName：抽验的临时目录放在备份目录的上一级里这个名字的目录下，每次一个 <YYYYMMDD-HHMMSS> 子目录
	DrillRootName = "Sumika-抽验"

	drillKeep       = 20 // 记录里留最近几条
	drillSweepAfter = 24 * time.Hour
	drillFile       = ".drill.json" // 备份目录里的小文件，和 .usage.json 同一个待遇：不在任何备份文件夹里，轮换和 Scan 不碰
)

// 抽验的来源
const (
	DrillWeekly = "weekly"
	DrillManual = "manual"
)

// ErrNoDrillBackup：没有可以抽验的备份（一份校验通过的都没有）
var ErrNoDrillBackup = errors.New("没有可以抽验的备份")

// DrillRecord 是一次抽验的结果
type DrillRecord struct {
	At           time.Time `json:"at"`
	Kind         string    `json:"kind"` // weekly | manual
	Backup       string    `json:"backup"`
	BackupTime   time.Time `json:"backupTime"`
	OK           bool      `json:"ok"`
	Problems     []string  `json:"problems,omitempty"`
	DurationMs   int64     `json:"durationMs"`
	Files        int       `json:"files"`
	Bytes        int64     `json:"bytes"`
	DBs          int       `json:"dbs"`
	RowsRecorded bool      `json:"rowsRecorded"`
	// Counts 为真才算“上次通过”（让每周抽验推迟、让 S12 转绿）：只有本数据目录的、抽验当时最新一份好备份才计入。
	// 别的数据目录的、旧的备份手动抽验照样核对、记录、打印，但不计入（NotCountedWhy 写原因）
	Counts        bool   `json:"counts"`
	NotCountedWhy string `json:"notCountedWhy,omitempty"`
}

// DrillState 是抽验的状态（调度器缓存的，自检读它，不扫盘）
type DrillState struct {
	Checked        bool        // 已经读过记录文件（读不到时不置，自检当“没读过”）
	HaveBackup     bool        // 备份目录里有可以抽验的备份（属于这个数据目录的、校验通过的、不是没做完的）
	Have           bool        // 有过抽验记录
	Last           DrillRecord // 最近一次计入的抽验（影响自检和每周抽验什么时候做）
	HaveAny        bool        // 有过任何抽验记录（含不计入的）
	LastAny        DrillRecord // 最近一次抽验，不管计不计入（backup list 显示）
	HavePass       bool
	LastPassAt     time.Time
	LastPassBackup string
	Running        bool
	RunningSince   time.Time
}

type drillFileData struct {
	Program string        `json:"program"`
	Source  string        `json:"source"`
	Records []DrillRecord `json:"records"`
}

// DrillResult 是一次抽验的结果和核对报告（给命令行打印各库的行数）
type DrillResult struct {
	Record DrillRecord
	Report RestoreReport
}

// DrillHistory 是 DrillDue 要看的东西
type DrillHistory struct {
	Have       bool
	LastAt     time.Time // 最近一次尝试
	LastOK     bool
	HavePass   bool
	LastPassAt time.Time
	Started    time.Time // 服务建好的时间：从没通过过时，有角色在回复的等待从这里算起
}

// DrillDue 是纯判断：现在要不要做一次每周抽验。
//   - 最近一次通过的在 7 天内 → 不到点；
//   - 最近一次尝试失败的，隔 24 小时才再试（退避）；
//   - 有角色在回复先等，等到距上一次通过（从没通过过就从服务建好算起）超过 9 天就不再等；
//   - 时钟往回拨过（记录比现在还晚）的不当“刚做过”。
func DrillDue(now time.Time, h DrillHistory, busy bool) bool {
	if h.Have && !h.LastOK && !now.Before(h.LastAt) && now.Sub(h.LastAt) < DrillRetryAfter {
		return false
	}
	if h.HavePass && !now.Before(h.LastPassAt) && now.Sub(h.LastPassAt) < DrillEvery {
		return false
	}
	ref := h.LastPassAt
	if !h.HavePass {
		ref = h.Started
	}
	if busy && now.Sub(ref) < DrillBusyCap {
		return false
	}
	return true
}

func drillHistory(st DrillState, started time.Time) DrillHistory {
	return DrillHistory{Have: st.Have, LastAt: st.Last.At, LastOK: st.Last.OK, HavePass: st.HavePass, LastPassAt: st.LastPassAt, Started: started}
}

// DrillRoot 是抽验的临时目录放的地方：备份目录的上一级里的 Sumika-抽验（和备份目录同盘，不在数据目录里，也不在备份目录里）
func (s *Service) DrillRoot() (string, error) {
	dir, err := s.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(dir), DrillRootName), nil
}

// 抽验记录文件（备份目录里的 .drill.json）

// readDrillsFile 读记录文件：文件不存在、读不懂、不是本程序写的、属于别的数据目录都当“没有”（valid 为假、ioErr 为空）；
// 盘暂时读不了（休眠、掉线）是 ioErr：这时不能当成“没有记录”，更不能之后把真实的文件覆盖掉
func readDrillsFile(dir, source string) (recs []DrillRecord, valid bool, ioErr error) {
	b, err := os.ReadFile(filepath.Join(dir, drillFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(b) > 1<<20 {
		return nil, false, nil
	}
	var d drillFileData
	if json.Unmarshal(b, &d) != nil || d.Program != Program || d.Source != source {
		return nil, false, nil
	}
	return d.Records, true, nil
}

func readDrills(dir, source string) ([]DrillRecord, bool) {
	recs, ok, ioErr := readDrillsFile(dir, source)
	return recs, ok && ioErr == nil
}

func writeDrills(dir, source string, recs []DrillRecord) error {
	return writeJSONFile(filepath.Join(dir, drillFile), drillFileData{Program: Program, Source: source, Records: recs})
}

// loadDrills 把记录文件读进内存。读盘在锁外做（Status、DrillStatus 给自检用，要求不扫盘、不等盘；备份盘休眠或掉线时不能把它们一起卡住）。
// 读不懂、不是本程序写的、属于别的数据目录的当没有；盘暂时读不了就不算读过，下次再读，这期间不写回文件（不能拿“只有这几条”覆盖真实的历史）
func (s *Service) loadDrills(dir string) {
	s.mu.Lock()
	done := s.drillsLoaded
	s.mu.Unlock()
	if done {
		return
	}
	recs, _, ioErr := readDrillsFile(dir, s.source())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drillsLoaded {
		return
	}
	if ioErr == nil {
		s.drillsLoaded = true
		s.drills = append(append([]DrillRecord(nil), recs...), s.drills...) // 文件里的在前，读之前已经记在内存里的在后
		if len(s.drills) > drillKeep {
			s.drills = append([]DrillRecord(nil), s.drills[len(s.drills)-drillKeep:]...)
		}
	}
	s.recomputeDrillLocked()
}

func (s *Service) recomputeDrillLocked() {
	d := &s.state.Drill
	d.Checked = s.drillsLoaded
	d.Have, d.HavePass, d.HaveAny = false, false, false
	d.Last, d.LastAny, d.LastPassAt, d.LastPassBackup = DrillRecord{}, DrillRecord{}, time.Time{}, ""
	for _, r := range s.drills { // 旧的在前
		if !d.HaveAny || r.At.After(d.LastAny.At) {
			d.HaveAny, d.LastAny = true, r
		}
		if !r.Counts {
			continue // 不计入的（别的数据目录的、旧的备份）：不影响自检和每周抽验什么时候做
		}
		if !d.Have || r.At.After(d.Last.At) {
			d.Have, d.Last = true, r
		}
		if r.OK && (!d.HavePass || r.At.After(d.LastPassAt)) {
			d.HavePass, d.LastPassAt, d.LastPassBackup = true, r.At, r.Backup
		}
	}
}

// recordDrill 记下一次抽验：内存里留着，写进备份目录的小文件；写不进去只记 Warn（自检读内存里的）
func (s *Service) recordDrill(rec DrillRecord) {
	s.mu.Lock()
	s.drills = append(s.drills, rec)
	if len(s.drills) > drillKeep {
		s.drills = append([]DrillRecord(nil), s.drills[len(s.drills)-drillKeep:]...)
	}
	recs := append([]DrillRecord(nil), s.drills...)
	loaded := s.drillsLoaded
	s.recomputeDrillLocked()
	s.mu.Unlock()
	dir, err := s.Dir()
	if err == nil && !loaded {
		err = errors.New("记录文件暂时读不了，不拿只有这几条的内存列表去覆盖它；读得到以后下一次抽验一并写回")
	} else if err == nil {
		err = writeDrills(dir, s.source(), recs)
	}
	if err != nil {
		s.opt.Warnf("抽验结果写不进备份目录里的 %s（只在内存里留着，重启后会丢）：%v", drillFile, err)
	}
}

// DrillStatus 是抽验的状态
func (s *Service) DrillStatus() DrillState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Drill
}

var drillRunName = regexp.MustCompile(`^\d{8}-\d{6}(-\d+)?$`)

// drillMarker 是抽验目录里 .restore/drill.json：证明这个目录是抽验自己建的（删之前要核对）
type drillMarker struct {
	Program   string    `json:"program"`
	Path      string    `json:"path"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"createdAt"`
}

func drillMarkerPath(run string) string {
	return filepath.Join(run, paths.RestoreDir, drillMarkerFile)
}

// ownDrillRun 判断 run 是不是抽验自己建的、能删的目录（规格 10.10.4“删的规矩”）：路径重新算一遍，每一条都满足才返回 nil
func (s *Service) ownDrillRun(run string) (drillMarker, error) {
	var mk drillMarker
	backupDir, err := s.Dir()
	if err != nil {
		return mk, err
	}
	workspace := filepath.Join(filepath.Dir(backupDir), DrillRootName)
	run = filepath.Clean(run)
	switch {
	case lower(filepath.Dir(run)) != lower(workspace):
		return mk, fmt.Errorf("%s 不在 %s 直接下面", run, workspace)
	case !drillRunName.MatchString(filepath.Base(run)):
		return mk, fmt.Errorf("%s 的名字不是抽验建的格式", run)
	case isLinkEntry(workspace):
		return mk, fmt.Errorf("%s 是联接或读不了", workspace)
	case isLinkEntry(run):
		return mk, fmt.Errorf("%s 是联接或读不了", run)
	}
	if fi, err := os.Lstat(run); err != nil || !fi.IsDir() {
		return mk, fmt.Errorf("%s 不是目录", run)
	}
	realRun, err := paths.RealPath(run)
	if err != nil {
		return mk, err
	}
	realWork, err := paths.RealPath(workspace)
	if err != nil {
		return mk, err
	}
	dataReal, err := realExisting(s.opt.Paths.Root)
	if err != nil {
		return mk, err
	}
	backupReal, err := realExisting(backupDir)
	if err != nil {
		return mk, err
	}
	switch {
	case lower(filepath.Dir(realRun)) != lower(realWork):
		return mk, fmt.Errorf("%s 解开以后不在 %s 下面", run, realWork)
	case within(dataReal, realRun) || within(realRun, dataReal):
		return mk, fmt.Errorf("%s 和数据目录有包含关系", realRun)
	case within(backupReal, realRun) || within(realRun, backupReal):
		return mk, fmt.Errorf("%s 和备份目录有包含关系", realRun)
	}
	b, err := os.ReadFile(drillMarkerPath(run))
	if err != nil || len(b) > 4096 {
		return mk, fmt.Errorf("%s 里没有抽验的标记", run)
	}
	if json.Unmarshal(b, &mk) != nil || mk.Program != Program || lower(filepath.Clean(mk.Path)) != lower(run) {
		return mk, fmt.Errorf("%s 里的标记不是抽验写的，或记的路径和它对不上", run)
	}
	return mk, nil
}

// removeDrillRun 只删自己建的抽验目录；不满足规矩的不删、删不干净的也回错（抽验把它记成失败，S12 能看到），同时记一条 Warn
func (s *Service) removeDrillRun(run string) error {
	if _, err := s.ownDrillRun(run); err != nil {
		s.opt.Warnf("抽验临时目录不删（不能确认是抽验自己建的）：%v", err)
		return fmt.Errorf("不能确认 %s 是抽验自己建的所以没删：%w", run, err)
	}
	if err := removeTree(run); err != nil {
		s.opt.Warnf("抽验临时目录没删干净（下次抽验前会再清）：%v", err)
		return fmt.Errorf("%s 没删干净：%w", run, err)
	}
	return nil
}

// sweepDrills 清掉 Sumika-抽验 里上次进程被杀留下的旧子目录：同样要通过 ownDrillRun 的全部检查，
// 而且是本进程启动以前建的（本进程一定没有在做它）或超过 1 天（别的进程可能还在做）；别的东西一概不碰
func (s *Service) sweepDrills(ctx context.Context) {
	root, err := s.DrillRoot()
	if err != nil {
		return
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	started := s.Status().Started
	for _, e := range ents {
		if ctx.Err() != nil {
			return
		}
		if !e.IsDir() || !drillRunName.MatchString(e.Name()) {
			continue
		}
		run := filepath.Join(root, e.Name())
		mk, err := s.ownDrillRun(run)
		if err != nil {
			continue // 不是我们建的：不碰，也不记（别人的东西不归我们管）
		}
		if age := s.opt.Now().Sub(mk.CreatedAt); !mk.CreatedAt.Before(started) && age < drillSweepAfter {
			continue // 本进程启动以后建的、还不到 1 天（或时钟往回拨过）：留着
		}
		if s.removeDrillRun(run) == nil {
			s.opt.Logf("抽验：清掉了上次没清干净的临时目录 %s", e.Name())
		}
	}
}

// pickDrillBackup 挑抽哪一份：属于这个数据目录的、校验通过的最新一份每天备份；一份每天备份都没有时退到最新一份校验通过的（任何原因）。
// 不抽手动备份：带全部媒体的手动备份可能有几十 GB
func (s *Service) pickDrillBackup() (string, error) {
	entries, err := s.List()
	if err != nil {
		return "", err
	}
	now := s.opt.Now()
	var any string
	for _, e := range entries { // 新的在前
		if e.Partial || e.Manifest.Status != StatusOK || e.Manifest.Time.After(now) {
			continue
		}
		if e.Manifest.Reason == ReasonDaily {
			return e.Name, nil
		}
		if any == "" {
			any = e.Name
		}
	}
	if any == "" {
		return "", ErrNoDrillBackup
	}
	return any, nil
}

// Verify 抽验一份备份：恢复到 Sumika-抽验 下的临时目录、核对、删掉、记录。name 为空按 pickDrillBackup 挑；kind 是 DrillWeekly 或 DrillManual。
// 同步做，持着备份、恢复共用的那把锁；拿不到锁回 ErrBusy。恢复本身失败（核对不过）不是错误：结果在返回值里，OK 为假
func (s *Service) Verify(ctx context.Context, name, kind string) (DrillResult, error) {
	ctx, release, err := s.acquire(ctx)
	if err != nil {
		return DrillResult{}, err
	}
	defer release()
	return s.drillLocked(ctx, name, kind)
}

// GoVerify 同 Verify，在后台做，立刻返回
func (s *Service) GoVerify(name, kind string) (<-chan DrillOutcome, error) {
	ctx, release, err := s.acquire(context.Background())
	if err != nil {
		return nil, err
	}
	ch := make(chan DrillOutcome, 1)
	go func() {
		defer release()
		var out DrillOutcome
		func() {
			defer func() {
				if r := recover(); r != nil {
					out.Err = fmt.Errorf("抽验时出了意外：%v", r)
				}
			}()
			out.Result, out.Err = s.drillLocked(ctx, name, kind)
		}()
		ch <- out
	}()
	return ch, nil
}

// DrillOutcome 是后台做完的一次抽验
type DrillOutcome struct {
	Result DrillResult
	Err    error
}

func pathExists(p string) bool { _, err := os.Lstat(p); return err == nil }

func (s *Service) setDrillRunning(v bool) {
	s.mu.Lock()
	s.state.Drill.Running = v
	if v {
		s.state.Drill.RunningSince = s.opt.Now()
	} else {
		s.state.Drill.RunningSince = time.Time{}
	}
	s.mu.Unlock()
}

func (s *Service) drillLocked(ctx context.Context, name, kind string) (res DrillResult, err error) {
	dir, err := s.Dir()
	if err != nil {
		return DrillResult{}, err
	}
	s.loadDrills(dir) // 先读记录文件：不先读就写，会拿“只有这一条”的内存列表覆盖掉真实的历史
	s.setDrillRunning(true)
	defer s.setDrillRunning(false)
	started := time.Now()
	rec := DrillRecord{At: s.opt.Now(), Kind: kind}
	// 记下这次抽验：找到了备份以后不管怎么结束（通过、没通过、备份本身不能用、开工前就被拒绝、超时）都留一条；
	// 没有备份可抽、手动写错了名字、被取消（关机）的不记。超时（DeadlineExceeded）不是关机取消：记成失败，走退避，不然每个整点再做 6 小时
	record := false
	defer func() {
		if !record || (err != nil && errors.Is(ctx.Err(), context.Canceled)) {
			return
		}
		rec.DurationMs = time.Since(started).Milliseconds()
		res.Record = rec
		s.recordDrill(rec)
	}()
	fail := func(format string, args ...any) (DrillResult, error) {
		rec.Problems = []string{fmt.Sprintf(format, args...)}
		return DrillResult{Record: rec}, nil
	}

	s.sweepDrills(ctx)
	// “上次通过”的规则：只有本数据目录的、抽验当时最新一份好备份（pickDrillBackup 会挑的那份）抽验通过才计入
	picked, perr := s.pickDrillBackup()
	if strings.TrimSpace(name) == "" {
		if perr != nil {
			return DrillResult{}, perr // 没有可抽的备份：不记成失败（备份本身有没有由自检 S9 管）
		}
		name = picked
	}
	rec.Counts = perr == nil && lower(picked) == lower(name)
	entry, m, err := s.openBackupFor(dir, name)
	if err != nil {
		if errors.Is(err, ErrBadBackupName) || errors.Is(err, ErrBackupNotFound) {
			return DrillResult{}, err // 手动写错了名字：不是抽验的结果
		}
		rec.Backup, record = name, true
		if !rec.Counts {
			rec.NotCountedWhy = "不是最新一份好的每天备份"
		}
		return fail("%v", err)
	}
	record = true
	rec.Backup, rec.BackupTime = m.Name, m.Time
	switch {
	case m.Source != s.source():
		rec.Counts, rec.NotCountedWhy = false, "别的数据目录的备份"
	case !rec.Counts && perr == nil:
		rec.NotCountedWhy = "不是最新一份好的每天备份（最新的是 " + picked + "）"
	case !rec.Counts:
		rec.NotCountedWhy = "不是最新一份好的每天备份"
	}

	root, err := s.DrillRoot()
	if err != nil {
		return fail("%v", err)
	}
	// 抽验目录本身是联接：副本会写到联接另一头、而我们不删联接里的东西，一整份数据副本就留在那里没人知道。建任何东西之前就拒绝
	if pathExists(root) && isLinkEntry(root) {
		return fail("抽验目录 %s 是联接（或读不了）：不在联接里抽验；把它改成真实目录（或删掉联接让程序自己建）", root)
	}
	run := filepath.Join(root, rec.At.Format("20060102-150405"))
	for n := 2; pathExists(run) && n < 100; n++ {
		run = filepath.Join(root, fmt.Sprintf("%s-%d", rec.At.Format("20060102-150405"), n))
	}
	real, _, err := s.checkRestoreTarget(dir, run)
	if err != nil {
		return fail("%v", err)
	}
	if err := s.checkRestoreRoom(real, m); err != nil {
		return fail("%v", err)
	}
	if err := os.MkdirAll(run, 0o755); err != nil {
		return fail("建不了抽验的临时目录：%v", err)
	}
	// 先写“这是我建的”标记，再开始恢复：之后不管是哪一步断了，这个目录都能被认出来、被清掉
	if err := writeJSONFile(drillMarkerPath(run), drillMarker{Program: Program, Path: run, Source: s.source(), CreatedAt: rec.At}); err != nil {
		_ = os.Remove(run) // 还什么都没写进去：只可能是空目录
		return fail("抽验的临时目录写不进东西：%v", err)
	}
	real, err = paths.RealPath(run)
	if err != nil {
		_ = s.removeDrillRun(run)
		return fail("%v", err)
	}
	// 建完以后再核对一遍：解开以后必须还是直接在 Sumika-抽验 的真实位置下面（检查到建立之间有人把某一级换成联接就停）
	if realWork, werr := paths.RealPath(root); werr != nil || lower(filepath.Dir(real)) != lower(realWork) {
		_ = s.removeDrillRun(run)
		return fail("抽验的临时目录建好以后解到了 %s，不在 %s 的真实位置下面（建的时候被换成了联接？），已经停下", real, root)
	}
	rep, rerr := s.restoreCore(ctx, entry, m, restoreOpts{purpose: PurposeDrill, root: real, existed: true})
	rmErr := s.removeDrillRun(run)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if ctx.Err() != nil && !timedOut {
		return DrillResult{Report: rep}, fmt.Errorf("抽验被取消：%w", ctx.Err())
	}
	rec.OK = rerr == nil && rep.OK && !timedOut
	rec.Problems = rep.Problems
	if timedOut {
		rec.Problems = []string{fmt.Sprintf("这次抽验做了超过 %v 还没做完，已经取消（盘太慢或备份太大？）", s.opt.RunMax)}
	} else if !rec.OK && len(rec.Problems) == 0 && rerr != nil {
		rec.Problems = []string{rerr.Error()}
	}
	if rmErr != nil { // 删不掉不能记成通过：副本留在盘上，S12 要能看到
		rec.OK = false
		rec.Problems = append(rec.Problems, "临时目录没删掉："+rmErr.Error())
	}
	if len(rec.Problems) > maxProblems {
		rec.Problems = rec.Problems[:maxProblems]
	}
	rec.Files, rec.Bytes, rec.DBs, rec.RowsRecorded = rep.Files, rep.Bytes, len(rep.DBs), rep.RowsRecorded
	return DrillResult{Record: rec, Report: rep}, nil
}

// tickDrill 是 Tick 里每天备份那段之后的每周抽验：这次刚做过每天备份就跳过（和每天备份错开，下个小时再看）；拿不到锁（备份、恢复在做）也跳过，下次补
func (s *Service) tickDrill(ctx context.Context, ranDaily bool) {
	if !s.opt.Drill || ctx.Err() != nil {
		return
	}
	s.sweepOnce.Do(func() { s.sweepDrills(ctx) }) // 进程第一次 Tick 先清上次被杀、关机留下的副本（建在本进程启动以前的）
	if ranDaily {
		return
	}
	st := s.DrillStatus()
	busy := s.opt.Busy != nil && s.opt.Busy()
	if !DrillDue(s.opt.Now(), drillHistory(st, s.Status().Started), busy) {
		return
	}
	s.opt.Logf("每周抽验：开始")
	res, err := s.Verify(ctx, "", DrillWeekly)
	switch {
	case errors.Is(err, ErrBusy):
		s.opt.Logf("每周抽验：有备份、恢复或别的抽验在做，这次先跳过，下个小时再看")
	case errors.Is(err, ErrNoDrillBackup):
		s.opt.Logf("每周抽验：还没有可以抽的备份，先跳过")
	case err != nil:
		if ctx.Err() != nil {
			s.opt.Logf("每周抽验：被打断，下次再做")
		} else {
			s.opt.Warnf("每周抽验：没做成：%v", err)
		}
	case res.Record.OK:
		s.opt.Logf("每周抽验：通过（%s，%d 个文件，%d 个库，用了 %d 毫秒）", res.Record.Backup, res.Record.Files, res.Record.DBs, res.Record.DurationMs)
	default:
		s.opt.Warnf("每周抽验：没通过（%s）：%s", res.Record.Backup, strings.Join(res.Record.Problems, "；"))
	}
}
