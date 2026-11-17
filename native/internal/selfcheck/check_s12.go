package selfcheck

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// DrillState 是备份抽验的状态（internal/backup 的 DrillState 的一份拷贝：自检包不能 import backup，和 BackupState 一样由 app 注入一个函数）。
// 评估时不扫盘：自检单次评估限 50 毫秒。
type DrillState struct {
	Enabled      bool      // 每周自动抽验开着（跟着 backup.daily）
	Checked      bool      // 调度器已经读过抽验记录
	HaveBackup   bool      // 备份目录里有可以抽验的备份（校验通过的）
	Have         bool      // 有过抽验记录
	LastAt       time.Time // 最近一次抽验的时间
	LastOK       bool
	LastBackup   string // 最近一次抽验的是哪一份
	LastProblem  string // 最近一次失败的第一条问题（只拿来分类，不放进例子）
	HavePass     bool
	LastPassAt   time.Time
	Running      bool
	RunningSince time.Time
	RunMax       time.Duration // 一次备份、抽验最多做多久（backup.runMaxHours）；0 = 不知道，按 BackupRunMax
	Started      time.Time     // 备份服务建好的时间（进程启动）
}

// DefaultDrillStale 是“抽验太久没通过”的阈值：每周一次，留 3 天余量（关机、角色在回复、失败后隔一天再试）
const DefaultDrillStale = 10 * 24 * time.Hour

// backupDrillStale 是 S12 backup.drill_stale：每周自动抽验（把最新一份好备份真的恢复到临时目录再核对）超过 10 天没有通过，
// 或者最近一次抽验失败。备份有没有做、做得好不好是 S9 的事；这一条管“备份能不能真的恢复”。
//
// 判断（规格-数据目录与备份 10.10.4）：
//   - 每周抽验没开（每日备份关着）→ ok；调度还没读过记录 → unknown；
//   - 正在抽验 → unknown，做了 RunMax + 1 小时以上记 bad（卡住了）；
//   - 最近一次抽验失败（且之后没有通过的）→ bad，备注写下一步；
//   - 最近一次通过在 10 天内 → ok；
//   - 一份可以抽验的备份都没有、也从没通过过 → unknown（备份本身由 backup.daily_stale 管）；
//   - 否则进程（或最近一次通过）已经超过 10 天还没有通过的抽验 → bad；其余（刚开机，还在等第一次机会）→ unknown。
type backupDrillStale struct{ after time.Duration }

func (backupDrillStale) ID() string   { return "backup.drill_stale" }
func (backupDrillStale) Name() string { return "备份抽验太久没通过或最近一次失败" }

const (
	drillNextFailed = "运行 sumika-ctl backup verify 再验一次；还不过，说明最近的备份可能不能真的恢复，看 sumika-ctl backup list 和日志，必要时 sumika-ctl backup now 重做一份"
	drillNextStale  = "运行 sumika-ctl backup verify 手动验一次；再看 sumika-ctl backup list 里有没有校验通过的备份"
)

func (c backupDrillStale) Eval(_ context.Context, e *Env) Result {
	after := c.after
	if after <= 0 {
		after = DefaultDrillStale
	}
	r := Result{Threshold: int64(after / (24 * time.Hour))}
	if e.Drill == nil {
		r.State, r.Note = Unknown, "没有接备份抽验状态"
		return r
	}
	st := e.Drill()
	if !st.Enabled {
		r.State, r.Note = Ok, "每周自动抽验已关闭（跟着每天自动备份）"
		r.Next = "仍可用 sumika-ctl backup verify 手动抽验"
		return r
	}
	if !st.Checked {
		r.State, r.Note = Unknown, "抽验记录还没读过"
		return r
	}
	if st.HavePass {
		r.Value = int64(e.Now.Sub(st.LastPassAt) / (24 * time.Hour))
	}
	if st.Running {
		limit := BackupRunMax
		if st.RunMax > 0 {
			limit = st.RunMax + time.Hour
			if limit > DefaultBackupStale {
				limit = DefaultBackupStale
			}
		}
		if !st.RunningSince.IsZero() && e.Now.Sub(st.RunningSince) > limit {
			r.State = Bad
			r.Example = map[string]string{"running": e.Now.Sub(st.RunningSince).Round(time.Minute).String()}
			r.Note = "这次抽验做得太久，像是卡住了"
			r.Next = drillNextStale
			return r
		}
		r.State, r.Note = Unknown, "正在抽验"
		return r
	}
	if st.Have && !st.LastOK && (!st.HavePass || st.LastAt.After(st.LastPassAt)) {
		r.State = Bad
		r.Example = map[string]string{"failedAt": st.LastAt.Format(time.RFC3339), "backup": st.LastBackup}
		if st.LastProblem != "" {
			r.Example["errorClass"] = StoreErrorClass(errors.New(st.LastProblem)) // 只记类别：原文带路径
		}
		if st.HavePass {
			r.Example["lastPass"] = st.LastPassAt.Format(time.RFC3339)
		} else {
			r.Example["lastPass"] = "没有"
		}
		r.Note = "最近一次抽验没通过"
		r.Next = drillNextFailed
		return r
	}
	if st.HavePass && e.Now.Sub(st.LastPassAt) <= after {
		r.State = Ok
		return r
	}
	if !st.HaveBackup && !st.HavePass {
		r.State, r.Note = Unknown, "还没有可以抽验的备份（备份本身由 backup.daily_stale 管）"
		return r
	}
	since := st.Started
	if st.HavePass && st.LastPassAt.After(since) {
		since = st.LastPassAt
	}
	if e.Now.Sub(since) > after {
		r.State = Bad
		r.Example = map[string]string{"since": strconv.FormatInt(int64(e.Now.Sub(since)/(24*time.Hour)), 10) + "d"}
		if st.HavePass {
			r.Example["lastPass"] = st.LastPassAt.Format(time.RFC3339)
		} else {
			r.Example["lastPass"] = "没有"
		}
		r.Note = fmt.Sprintf("已经 %d 天没有通过的抽验", int64(e.Now.Sub(since)/(24*time.Hour)))
		r.Next = drillNextStale
		return r
	}
	r.State, r.Note = Unknown, "刚开机，等第一次抽验机会"
	return r
}
