package selfcheck

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// BackupState 是备份调度器缓存的状态（internal/backup 的 State 的一份拷贝：自检包不能 import backup，和 Running、Pending 一样由 app 注入一个函数）。
// 评估时不扫备份目录：自检单次评估限 50 毫秒。
type BackupState struct {
	Enabled      bool      // backup.daily 开着
	Checked      bool      // 调度器已经看过一次备份目录
	Running      bool      // 现在有一份在做
	HaveGood     bool      // 有校验通过的每天备份（时间不晚于现在的）
	LastGood     time.Time // 其中最新的一份
	LastFailure  string    // 这个进程里最近一次每天备份失败的原因（之后成功了就空）
	FailedAt     time.Time
	Started      time.Time     // 备份服务建好的时间（进程启动）
	LastSkipped  int           // 最近一份好的每天备份跳过了几个媒体（链接、读不了）
	RunMax       time.Duration // 一份备份最多做多久（backup.runMaxHours）；0 = 不知道，按 BackupRunMax（7 小时）
	RunningSince time.Time     // 现在这份是什么时候开始做的（Running 为真时才有）
	Deferred     bool          // 到点了，有角色在回复，先等着
}

// DefaultBackupStale 是“太久没成功”的阈值：每天一份，留 6 小时余量给“正在回复就先等”
const DefaultBackupStale = 26 * time.Hour

// BackupRunMax：不知道备份自己的上限（BackupState.RunMax 是 0）时，一份备份做超过这么久记异常（卡住了）。
// 知道的话用 min(RunMax + 1 小时, 26 小时)：让备份自己先取消、记失败，但 runMaxHours 调得再大，做了 26 小时还在做也报。（常量放在这里是因为自检包不能 import backup）
const BackupRunMax = 7 * time.Hour

const backupNext = "运行 sumika-ctl backup now 重做一份；失败时用 sumika-ctl backup list 看原因"

// backupStale 是 S9 backup.daily_stale：每天自动备份开着，却超过 26 小时没有校验通过的“每天”备份。
//
// 判断（规格-数据目录与备份 10.6）：
//   - 没开每日备份 → ok（备注写关着）；调度器还没做过第一次检查、或这一份正在做 → unknown，不记成 ok；
//   - 最近一份好的在阈值以内 → ok；
//   - 否则：这个进程里有过失败的尝试且之后没成功，或者进程已经开了超过阈值还没做出来 → bad；
//     其余（刚开机，还在等第一次机会：关机几天再开机时的前几分钟）→ unknown，不报警。
type backupStale struct{ after time.Duration }

func (backupStale) ID() string   { return "backup.daily_stale" }
func (backupStale) Name() string { return "每天自动备份太久没成功" }

func (c backupStale) Eval(_ context.Context, e *Env) Result {
	after := c.after
	if after <= 0 {
		after = DefaultBackupStale
	}
	r := Result{Threshold: int64(after / time.Hour)}
	if e.Backup == nil {
		r.State, r.Note = Unknown, "没有接备份状态"
		return r
	}
	st := e.Backup()
	if !st.Enabled {
		r.State, r.Note = Ok, "每天自动备份已关闭"
		return r
	}
	if !st.Checked {
		r.State, r.Note = Unknown, "备份调度还没做过第一次检查"
		return r
	}
	if st.Running {
		limit := BackupRunMax
		if st.RunMax > 0 {
			limit = st.RunMax + time.Hour // 备份自己到 RunMax 会取消并记失败，再给一小时收尾；超过这个还在做才是真卡住
			if limit > DefaultBackupStale {
				limit = DefaultBackupStale // 但不超过“每天备份太久没成功”的 26 小时：runMaxHours 调得再大，做了一整天还在做也该报
			}
		}
		if !st.RunningSince.IsZero() && e.Now.Sub(st.RunningSince) > limit {
			r.State, r.Value = Bad, int64(e.Now.Sub(st.RunningSince)/time.Hour)
			r.Example = map[string]string{"running": e.Now.Sub(st.RunningSince).Round(time.Minute).String()}
			r.Note = "这份备份做得太久，像是卡住了"
			r.Next = backupNext
			return r
		}
		r.State, r.Note = Unknown, "正在备份"
		return r
	}
	if st.HaveGood {
		age := e.Now.Sub(st.LastGood)
		r.Value = int64(age / time.Hour)
		if age <= after {
			r.State = Ok
			if st.LastSkipped > 0 {
				r.Note = fmt.Sprintf("最近一份跳过了 %d 个没法带的媒体（链接或读不了）", st.LastSkipped)
			}
			return r
		}
	} else {
		r.Value = -1 // 一份校验通过的每天备份都没有
	}
	failed := !st.FailedAt.IsZero() && (!st.HaveGood || st.FailedAt.After(st.LastGood))
	if st.Deferred && !failed { // 失败判断排在“先等”前面：失败以后碰上有角色在回复，也要报
		r.State, r.Note = Unknown, "有角色在回复，每天备份先等（最多等到 36 小时就不再等）"
		return r
	}
	since := st.Started
	if st.HaveGood && st.LastGood.After(since) {
		since = st.LastGood
	}
	if failed || e.Now.Sub(since) > after {
		r.State = Bad
		r.Next = backupNext
		r.Note = "超过阈值没有校验通过的每天备份"
		if failed {
			r.Note = "最近一次每天备份失败"
		}
		r.Example = map[string]string{}
		if st.HaveGood {
			r.Example["lastGood"] = st.LastGood.Format(time.RFC3339)
		} else {
			r.Example["lastGood"] = "没有"
		}
		if failed {
			r.Example["errorClass"] = StoreErrorClass(errors.New(st.LastFailure)) // 只记类别：原文带路径
			r.Example["failedAt"] = st.FailedAt.Format(time.RFC3339)
		} else {
			r.Example["since"] = strconv.FormatInt(int64(e.Now.Sub(since)/time.Hour), 10) + "h"
		}
		return r
	}
	r.State, r.Note = Unknown, "刚开机，等第一次备份机会"
	return r
}
