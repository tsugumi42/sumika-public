package app

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"sumika-native/internal/applog"
	"sumika-native/internal/backup"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/paths"
	"sumika-native/internal/projectcontext"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/sqlitesnap"
)

// 备份（规格-数据目录与备份 第十节，R-386）：宿主在这里装配 backup.Service，持有库连接的一方登记快照，
// 调度、sumika-ctl backup、自检都读同一个 Service。路径只在 paths 和 backup 的布局表里算，这里不拼。
// 两种模式都建（archive 也能手动备份）；调度只在接收模式（Run）里开：archive 是只读历史，没有新数据要备份。

// buildRevision 是写进备份清单的程序版本：编译时带进来的提交号，没有就写“未知”
func buildRevision() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "未知"
	}
	rev, dirty := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "未知"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "+改动"
	}
	return rev
}

// openBackup 建备份服务并登记持有方的快照。可重复调用无害：只建一次
func (a *App) openBackup() {
	if a.backup != nil {
		return
	}
	rel := func(file string) string {
		r, err := filepath.Rel(a.paths.Root, file)
		if err != nil {
			return filepath.ToSlash(file)
		}
		return filepath.ToSlash(r)
	}
	svc := backup.New(backup.Options{
		Paths:     a.paths,
		Dir:       a.cfg.Backup.Dir,
		TempData:  os.Getenv("SUMIKA_DATA_DIR") != "", // 指向临时数据目录（测试、冒烟检查）：一律放旁边
		TempRoots: a.opt.BackupTempRoots,              // 数据目录在系统临时目录下时同样一律放旁边（-data 指到临时目录、配置却是真实 backup.dir 的情况）
		Media:     a.cfg.Backup.DailyMedia,
		Daily:     a.cfg.Backup.Daily,
		AlertGB:   a.cfg.Backup.AlertGB,
		// 配置里 0 = 不查剩余空间；Options 里 0 是默认下限、负数才是不查，所以要换算
		MinFreeBytes: minFreeBytes(a.cfg.Backup.MinFreeGB),
		RunMax:       time.Duration(a.cfg.Backup.RunMaxHours * float64(time.Hour)),
		CloseWait:    time.Duration(a.cfg.Backup.CloseWaitSeconds * float64(time.Second)),
		Version:      buildRevision(),
		Drill:        a.cfg.Backup.Daily, // 每周抽验跟着每天备份：关着每日备份就没有调度，抽验也不自动做（sumika-ctl backup verify 仍可手动）
		Now:          a.now,
		Logf:         a.logfFor(applog.Backup),
		Warnf:        a.warnFor(applog.Backup),
		Busy: func() bool { // 有角色正在回复：每天备份先等（超过 36 小时不再等）
			return a.hub != nil && a.hub.RunningTurns() > 0
		},
	})
	svc.Register(func() backup.Contribution {
		var c backup.Contribution
		if a.set != nil {
			for _, n := range a.set.Each() {
				if n.Store == nil || n.Store.Events == nil {
					continue
				}
				db := n.Store.Events
				dbRel := rel(a.paths.ChatDB(n.Platform))
				c.DBs = append(c.DBs, backup.DBSource{Rel: dbRel, Snapshot: func(ctx context.Context, dst string) error { return sqlitesnap.Into(ctx, db, dst) }})
				// 备份跑着时量入库要等多久（拿写锁再放，不写数据；规格 10.10.6）
				c.WriteProbes = append(c.WriteProbes, backup.WriteProbe{Rel: dbRel, Probe: func(ctx context.Context) (time.Duration, error) { return sqlitesnap.WriteLockWait(ctx, db) }})
				c.Stickers = append(c.Stickers, backup.StickerSource{
					MediaRel: path.Join(rel(a.paths.GoMediaDir()), n.Platform), DBRel: dbRel, Hashes: mediastore.StickerHashes,
				})
			}
			if tr := a.set.Trace(); tr != nil {
				c.DBs = append(c.DBs, backup.DBSource{Rel: rel(a.paths.GoTraceDB()), Snapshot: func(ctx context.Context, dst string) error { return sqlitesnap.Into(ctx, tr, dst) }})
			}
		}
		// 旧版媒体（shared/media）的表情包和头像记在 Bun 留下的 shared/events.db 里；那个库没有持有方，由备份包兜底取快照
		c.Stickers = append(c.Stickers, backup.StickerSource{MediaRel: rel(a.paths.MediaDir()), DBRel: rel(a.paths.BunEventsDB()), Hashes: mediastore.StickerHashes})
		return c
	})
	a.backup = svc
}

// minFreeBytes 把配置里的 GB 换成 backup.Options.MinFreeBytes：0 = 不查，对应 Options 里的负数
func minFreeBytes(gb float64) int64 {
	if gb <= 0 {
		return -1
	}
	return int64(gb * (1 << 30))
}

// protectBackupDir 把备份目录并进审批门的受保护目录：备份目录里有数据目录的完整副本（聊天库快照、明文配置、secrets.bin 的副本），
// 角色在本地私聊里读它不能比读数据目录里的同名文件容易。并进去的有：现在的备份目录（ResolveDir 的结果）、配置里写的原样路径
// （就算被临时数据目录的规则盖掉了、或者写进了数据目录里被拒绝，也照样保护），每个都连真实路径（联接解析后）一起，不存在也行
func (a *App) protectBackupDir() {
	if a.backup == nil || a.approvalGate == nil {
		return
	}
	var dirs []string
	add := func(dir string) {
		dirs = append(dirs, dir)
		// 真实路径明着并进去：卷标识写法（\\?\Volume{GUID}\x）规整不成 D:\x，审批门自己解不了它（不透明路径不解析）
		if real, err := backup.InspectDir(a.paths, dir); real != "" && (err == nil || errors.Is(err, backup.ErrDirInsideData)) {
			dirs = append(dirs, real)
		}
	}
	if dir, err := a.backup.Dir(); err == nil {
		add(dir)
	}
	// 设备路径前缀（\\?\D:\x）先还原成 D:\x：带前缀写的路径和审批门里规整过的 D:\x 比不上，保护就漏了
	if raw := paths.StripDevicePrefix(strings.TrimSpace(a.cfg.Backup.Dir)); raw != "" {
		if !filepath.IsAbs(raw) {
			raw = filepath.Join(filepath.Dir(a.paths.Root), raw) // 和 backup.ResolveDir 一样：相对路径按数据目录的上一级解
		}
		raw = filepath.Clean(raw)
		real, err := backup.InspectDir(a.paths, raw)
		if errors.Is(err, backup.ErrDirTooBroad) {
			// 盘符根、数据目录的上级整个并进去会让整个盘 / 项目变成每次问，这个路径本来也用不了（ResolveDir 拒绝），不并；
			// 但它里面本程序以前写的备份文件夹（有聊天库快照和明文配置）逐个并进去
			a.warnOnce(backupDirWhy(err), "配置里的 backup.dir（%s）%s：备份用不了它，审批门也不把它整个当受保护目录，只保护里面以前写的备份文件夹", a.cfg.Backup.Dir, backupDirWhy(err))
			scan := raw
			if real != "" {
				scan = real
			}
			if entries, serr := backup.Scan(scan); serr == nil {
				for _, e := range entries {
					add(e.Folder)
				}
			}
		} else {
			add(raw)
		}
	}
	// 每周抽验的临时目录：里面是数据目录的完整副本，和备份目录同一个待遇；固定在备份目录的上一级里，所以装配时就能并进去
	// （Gate.Protect 只能在装配时调，每次抽验的子目录是新的，保护它的上级）
	if root, err := a.backup.DrillRoot(); err == nil {
		add(root)
	}
	add(a.paths.BackupRoot()) // 默认位置也保护：配置以后改回空，或者备份目录算不出来时它还是那个样子
	a.approvalGate.Protect(dirs...)
}

// backupDirWhy 把备份目录算不出来的原因说成一句人话：是哪一种，别只说“要改”
func backupDirWhy(err error) string {
	switch {
	case errors.Is(err, backup.ErrDirInsideData):
		return "写在了数据目录里面（备份不能放在要备份的数据里）"
	case errors.Is(err, backup.ErrDirTooBroad):
		return "是盘符根或数据目录的上级（太宽，整个盘或项目都会被当成备份目录）"
	default:
		return "解不开（路径写错、盘不在，或没有权限）：" + err.Error()
	}
}

// warnOnce 同一个原因（key）只记一次 Warn：每个角色、每次装配都会问一遍备份目录，原样都记会刷屏
func (a *App) warnOnce(key, format string, args ...any) {
	if _, dup := a.backupWarned.LoadOrStore(key, true); !dup {
		a.warnFor(applog.Backup)(format, args...)
	}
}

// projectReadApproval 是项目说明读文件前的读取批准：碰到审批门的受保护目录（数据目录、备份目录……）一律拒。
// 传的是取清单的函数，每次读都现取门里的受保护目录，不是建它那一刻的快照
func (a *App) projectReadApproval() func(context.Context, string) error {
	return projectcontext.ReadApproval(func() []string { return a.approvalGate.ProtectedDirs })
}

// backupDirForCopies 是备份目录（backup.ResolveDir 的结果）：开写前的 memory.db 副本这类“别处做的备份”放这里，和每天备份在同一个地方。
// 备份服务没建好（不该发生）时直接用 ResolveDir 算
// 算不出来（写进了数据目录里、太宽、解不开）时记一条 Warn，写明是哪种原因；每个角色、每次装配都会问一遍，同一个原因只记一次
func (a *App) backupDirForCopies() (string, error) {
	var dir string
	var err error
	if a.backup != nil {
		dir, err = a.backup.Dir()
	} else {
		dir, err = backup.ResolveDir(a.paths, a.cfg.Backup.Dir, os.Getenv("SUMIKA_DATA_DIR") != "")
	}
	if err != nil {
		why := backupDirWhy(err)
		a.warnOnce("memcopies:"+why, "记忆开写前的备份没地方放，还没开过写（或整理）的角色先不开：backup.dir（%s）%s", a.cfg.Backup.Dir, why)
	}
	return dir, err
}

// runBackup 随后台起每天备份的调度（接收模式的 Run 里调）。关着每日备份就只记一行日志
func (a *App) runBackup() {
	if a.backup != nil {
		a.backup.Start()
	}
}

// closeBackup 停调度、取消正在做的那份（没做完的整个删掉）；要在关库之前。可重复调
func (a *App) closeBackup() {
	if a.backup != nil {
		a.backup.Close()
	}
}

// backupSelfcheck 是自检 S9 读的备份状态（调度器缓存的，不扫目录）
func (a *App) backupSelfcheck() selfcheck.BackupState {
	if a.backup == nil {
		return selfcheck.BackupState{}
	}
	st := a.backup.Status()
	return selfcheck.BackupState{Enabled: st.Enabled, Checked: st.Checked, Running: st.Running, HaveGood: st.HaveGood, LastGood: st.LastGood,
		LastFailure: st.LastFailure, FailedAt: st.FailedAt, Started: st.Started, LastSkipped: st.LastSkipped, RunningSince: st.RunningSince, RunMax: st.RunMax, Deferred: st.Deferred}
}

// drillSelfcheck 是自检 S12 读的备份抽验状态（调度器缓存的，不扫目录）
func (a *App) drillSelfcheck() selfcheck.DrillState {
	if a.backup == nil {
		return selfcheck.DrillState{}
	}
	st, bs := a.backup.DrillStatus(), a.backup.Status()
	d := selfcheck.DrillState{Enabled: bs.Enabled, Checked: st.Checked, HaveBackup: st.HaveBackup, Have: st.Have, LastAt: st.Last.At, LastOK: st.Last.OK,
		LastBackup: st.Last.Backup, HavePass: st.HavePass, LastPassAt: st.LastPassAt, Running: st.Running, RunningSince: st.RunningSince, RunMax: bs.RunMax, Started: bs.Started}
	if len(st.Last.Problems) > 0 {
		d.LastProblem = st.Last.Problems[0]
	}
	return d
}

// BackupService 给 sumika-ctl 用：这个 App 的备份服务（没建好是 nil）
func (a *App) BackupService() *backup.Service { return a.backup }

// ErrNoBackup 是备份服务没有建好
var ErrNoBackup = errors.New("备份服务还没准备好")

// BackupNow 做一份手动备份，最多等 wait（0 = 不等）：到点还没做完，备份在后台接着做，Settled=false
func (a *App) BackupNow(ctx context.Context, media string, wait time.Duration) (ctlproto.BackupNow, error) {
	if a == nil || a.backup == nil {
		return ctlproto.BackupNow{}, ErrNoBackup
	}
	done, err := a.backup.Go(backup.Request{Reason: backup.ReasonManual, Media: media})
	if err != nil {
		return ctlproto.BackupNow{}, err
	}
	if wait <= 0 {
		return ctlproto.BackupNow{Settled: false}, nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case o := <-done:
		if o.Err != nil && o.Result.Name == "" {
			return ctlproto.BackupNow{}, o.Err // 根本没做出文件夹（目录不可写、目录在数据目录里面……）
		}
		r := o.Result
		return ctlproto.BackupNow{Settled: true, Name: r.Name, Status: string(r.Status), Files: r.Files, Linked: r.Linked, Bytes: r.Bytes, Problems: r.Problems}, nil
	case <-t.C:
		return ctlproto.BackupNow{Settled: false}, nil
	case <-ctx.Done():
		return ctlproto.BackupNow{Settled: false}, nil
	}
}

func dbChecksOf(in []backup.DBCheck) []ctlproto.BackupDBCheck {
	out := make([]ctlproto.BackupDBCheck, 0, len(in))
	for _, d := range in {
		out = append(out, ctlproto.BackupDBCheck{Path: d.Path, Integrity: d.Integrity, Rows: string(d.Rows), Tables: d.Tables, Mismatch: d.Mismatch})
	}
	return out
}

// BackupRestore 把一份备份恢复成新目录并核对（规格 10.10）：后台做，最多等 wait，到点没做完回 Settled=false（目标目录里的 .restore/report.json 看结果）。
// 开工前就被拒绝（备份名不对、目标不合适、锁被占……）回 error；开工了但核对没通过，Settled=true、OK=false，问题在返回值里。
// 不切换、不改配置：这里没有任何一行去动现在的数据目录或配置
func (a *App) BackupRestore(ctx context.Context, name, to string, wait time.Duration) (ctlproto.BackupRestored, error) {
	if a == nil || a.backup == nil {
		return ctlproto.BackupRestored{}, ErrNoBackup
	}
	done, err := a.backup.GoRestore(name, to)
	if err != nil {
		return ctlproto.BackupRestored{}, err
	}
	if wait <= 0 {
		return ctlproto.BackupRestored{Settled: false, Name: name, Target: to}, nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case o := <-done:
		rep := o.Report
		if rep.Target == "" { // 没开工
			return ctlproto.BackupRestored{}, o.Err
		}
		problems := rep.Problems
		if !rep.OK && len(problems) == 0 && o.Err != nil {
			problems = []string{o.Err.Error()}
		}
		return ctlproto.BackupRestored{Settled: true, Name: rep.Backup, Target: rep.Target, OK: rep.OK && o.Err == nil, OtherSource: rep.OtherSource, Files: rep.Files, Bytes: rep.Bytes,
			DurationMs: rep.DurationMs, RowsRecorded: rep.RowsRecorded, DBs: dbChecksOf(rep.DBs), Problems: problems, Notes: rep.Notes}, nil
	case <-t.C:
		return ctlproto.BackupRestored{Settled: false, Name: name, Target: to}, nil
	case <-ctx.Done():
		return ctlproto.BackupRestored{Settled: false, Name: name, Target: to}, nil
	}
}

// BackupVerify 抽验一份备份（不写名字抽最新一份好的每天备份）：恢复到临时目录、核对、删掉、记下结果。规矩同 BackupRestore
func (a *App) BackupVerify(ctx context.Context, name string, wait time.Duration) (ctlproto.BackupVerified, error) {
	if a == nil || a.backup == nil {
		return ctlproto.BackupVerified{}, ErrNoBackup
	}
	done, err := a.backup.GoVerify(name, backup.DrillManual)
	if err != nil {
		return ctlproto.BackupVerified{}, err
	}
	if wait <= 0 {
		return ctlproto.BackupVerified{Settled: false, Name: name}, nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case o := <-done:
		if o.Err != nil {
			return ctlproto.BackupVerified{}, o.Err
		}
		r := o.Result.Record
		return ctlproto.BackupVerified{Settled: true, Name: r.Backup, Kind: r.Kind, OK: r.OK, Files: r.Files, Bytes: r.Bytes, DurationMs: r.DurationMs,
			RowsRecorded: r.RowsRecorded, DBs: dbChecksOf(o.Result.Report.DBs), Problems: r.Problems, Counts: r.Counts, NotCountedWhy: r.NotCountedWhy}, nil
	case <-t.C:
		return ctlproto.BackupVerified{Settled: false, Name: name}, nil
	case <-ctx.Done():
		return ctlproto.BackupVerified{Settled: false, Name: name}, nil
	}
}

// BackupList 列出备份和总览
func (a *App) BackupList(ctx context.Context) (ctlproto.BackupList, error) {
	if a == nil || a.backup == nil {
		return ctlproto.BackupList{}, ErrNoBackup
	}
	o, entries := a.backup.Overview()
	out := ctlproto.BackupList{Rows: []ctlproto.BackupRow{}, Overview: ctlproto.BackupOverview{
		Dir: o.Dir, DirError: o.DirError, Total: o.Total, Daily: o.Daily, Manual: o.Manual, PreChange: o.PreChange, Bad: o.Bad, Partial: o.Partial, Foreign: o.Foreign,
		UsedBytes: o.UsedBytes, UsedAt: msOf(o.UsedAt), UsedEstimated: o.UsedEstimated, FreeBytes: o.FreeBytes, FreeKnown: o.FreeKnown, AlertGB: o.AlertGB, DailyOn: o.DailyOn, Media: o.Media, Rules: o.Rules,
		Drill: ctlproto.BackupDrill{On: o.DrillOn, Have: o.Drill.Have, At: msOf(o.Drill.Last.At), Kind: o.Drill.Last.Kind, Backup: o.Drill.Last.Backup, OK: o.Drill.Last.OK,
			Problems: o.Drill.Last.Problems, DurationMs: o.Drill.Last.DurationMs, HavePass: o.Drill.HavePass, PassAt: msOf(o.Drill.LastPassAt), Running: o.Drill.Running}}}
	if o.Drill.HaveAny && !o.Drill.LastAny.Counts {
		x := o.Drill.LastAny
		out.Overview.Drill.HaveOther, out.Overview.Drill.OtherAt, out.Overview.Drill.OtherBackup, out.Overview.Drill.OtherOK, out.Overview.Drill.OtherWhy =
			true, msOf(x.At), x.Backup, x.OK, x.NotCountedWhy
		if len(x.Problems) > 0 {
			out.Overview.Drill.OtherProblem = x.Problems[0]
		}
	}
	if o.DirError != "" {
		return out, nil
	}
	for _, e := range entries {
		m := e.Manifest
		classes := make([]string, len(m.Classes))
		for i, c := range m.Classes {
			classes[i] = string(c)
		}
		status := string(m.Status)
		if e.Partial {
			status = string(backup.StatusPartial)
		}
		out.Rows = append(out.Rows, ctlproto.BackupRow{Name: e.Name, At: m.Time.UnixMilli(), Reason: string(m.Reason), Status: status, Bytes: m.Bytes,
			Linked: m.Linked, Classes: classes, Media: m.Media, DBs: len(m.DBs), Problems: m.Problems, SkippedMedia: m.SkippedMedia})
		last := &out.Rows[len(out.Rows)-1]
		last.WriteWaitSamples, last.WriteWaitFailed, last.WriteWaitMaxMs = backup.WriteWaitSummary(m.WriteWait)
	}
	return out, nil
}

// msOf 零值时间是 0，其余是毫秒时间戳
func msOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
