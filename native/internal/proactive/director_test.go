package proactive

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/config"
)

// 导演的纯函数（规格-主动联系 第七节、第十六节第 1 条）：间隔上下限、睡觉软信号、每道检查的顺序、系数三种变化。角色、时间都是编的。

var tz = time.FixedZone("T", 8*3600)

func at(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, tz) }

func params() config.Proactive {
	p := config.DefaultProactive()
	p.Enabled, p.Characters = true, []string{"aa"}
	return p
}

func fixed(v float64) Rand { return func() float64 { return v } }

func TestIntervalBoundsAndFactor(t *testing.T) {
	p := params()
	for _, c := range []struct {
		name   string
		factor float64
		rnd    float64
		want   time.Duration
	}{
		{"基准下限", 1, 0, 120 * time.Minute},
		{"基准上限附近", 1, 0.999999, 180*time.Minute - time.Second},
		{"基准中间", 1, 0.5, 150 * time.Minute},
		{"热了缩短", 0.5, 0, 60 * time.Minute},
		{"夹到 45 分钟下限", 0.1, 0, 45 * time.Minute},
		{"冷了拉长", 3, 0.5, 450 * time.Minute},
		{"夹到 12 小时上限", 5, 0.999999, 12 * time.Hour},
		{"系数 0 当 1", 0, 0, 120 * time.Minute},
		{"系数负数当 1", -2, 0, 120 * time.Minute},
	} {
		got := Interval(p, c.factor, fixed(c.rnd))
		if d := got - c.want; d < -2*time.Second || d > 2*time.Second {
			t.Errorf("%s：间隔 %v，想要 %v", c.name, got, c.want)
		}
	}
	if Interval(p, 1, nil) != 120*time.Minute {
		t.Fatal("没给随机数时应取基准下限")
	}
}

func TestInSleepAndSleepEnd(t *testing.T) {
	p := params() // 01:00～09:00
	for _, c := range []struct {
		h, m int
		in   bool
	}{{0, 59, false}, {1, 0, true}, {8, 59, true}, {9, 0, false}, {14, 0, false}, {23, 59, false}} {
		if got := InSleep(p, at(9, c.h, c.m)); got != c.in {
			t.Errorf("%02d:%02d 在睡觉时段内：%v，想要 %v", c.h, c.m, got, c.in)
		}
	}
	if got := SleepEnd(p, at(9, 2, 0)); !got.Equal(at(9, 9, 0)) {
		t.Errorf("凌晨 2 点的时段结束：%v", got)
	}

	// 跨午夜的区间 23:00～07:00
	p.Sleep.Start, p.Sleep.End = "23:00", "07:00"
	for _, c := range []struct {
		h, m int
		in   bool
	}{{22, 59, false}, {23, 0, true}, {23, 59, true}, {0, 0, true}, {6, 59, true}, {7, 0, false}, {12, 0, false}} {
		if got := InSleep(p, at(9, c.h, c.m)); got != c.in {
			t.Errorf("跨午夜区间 %02d:%02d：%v，想要 %v", c.h, c.m, got, c.in)
		}
	}
	if got := SleepEnd(p, at(9, 23, 30)); !got.Equal(at(10, 7, 0)) {
		t.Errorf("晚上 23:30 的时段结束应是次日 07:00：%v", got)
	}
	if got := SleepEnd(p, at(10, 3, 0)); !got.Equal(at(10, 7, 0)) {
		t.Errorf("凌晨 3 点的时段结束应是当天 07:00：%v", got)
	}
	// 月底跨月：11 月 30 日 23:30 → 12 月 1 日 07:00
	if got := SleepEnd(p, at(30, 23, 30)); !got.Equal(time.Date(2026, 12, 1, 7, 0, 0, 0, tz)) {
		t.Errorf("月底跨月：%v", got)
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(p *config.Proactive)
		st     State
		sg     Snapshot
		now    time.Time
		kind   Kind
		reason string
		next   time.Time // 零值不查
	}{
		{name: "白天没有任何拦的：放行", now: at(9, 14, 0), kind: KindWake, reason: ReasonDue, next: at(9, 14, 0).Add(150 * time.Minute)},
		{name: "别烦没到：顺延到别烦结束再加 0～30 分钟", now: at(9, 14, 0), st: State{QuietUntil: at(9, 20, 0)},
			kind: KindSkip, reason: ReasonSnoozed, next: at(9, 20, 15)},
		{name: "别烦已过：放行", now: at(9, 14, 0), st: State{QuietUntil: at(9, 13, 0)}, kind: KindWake, reason: ReasonDue},
		{name: "今天找满了：顺延到次日 0 点再加 0～60 分钟", now: at(9, 14, 0), sg: Snapshot{ThreadsToday: 4},
			kind: KindSkip, reason: ReasonDailyCap, next: at(10, 0, 30)},
		{name: "今天 3 次：还能找", now: at(9, 14, 0), sg: Snapshot{ThreadsToday: 3}, kind: KindWake, reason: ReasonDue},
		{name: "已有开着的线、没到续醒：顺延 15 分钟", now: at(9, 14, 0), sg: Snapshot{OpenThread: true},
			kind: KindSkip, reason: ReasonAlreadyOpen, next: at(9, 14, 15)},
		{name: "开着的线到了续醒时间：续醒", now: at(9, 14, 0), sg: Snapshot{OpenThread: true, FollowUpDue: true}, kind: KindFollowUp, reason: ReasonDue},
		{name: "续醒不受今天上限管", now: at(9, 14, 0), sg: Snapshot{OpenThread: true, FollowUpDue: true, ThreadsToday: 4}, kind: KindFollowUp, reason: ReasonDue},
		{name: "续醒不受连冷管", now: at(9, 14, 0), st: State{ColdStreak: 3, LastWake: at(9, 13, 0)}, sg: Snapshot{OpenThread: true, FollowUpDue: true}, kind: KindFollowUp, reason: ReasonDue},
		{name: "续醒被别烦拦下：只顺延 15 分钟，不等到别烦结束", now: at(9, 14, 0), st: State{QuietUntil: at(9, 20, 0)},
			sg: Snapshot{OpenThread: true, FollowUpDue: true}, kind: KindSkip, reason: ReasonSnoozed, next: at(9, 14, 15)},
		{name: "续醒时主人正在聊：顺延", now: at(9, 14, 0), sg: Snapshot{OpenThread: true, FollowUpDue: true, OwnerLastActive: at(9, 13, 55)},
			kind: KindSkip, reason: ReasonOwnerChatting, next: at(9, 14, 15)},
		{name: "连冷 3 次且当天醒过：当天不再找", now: at(9, 22, 0), st: State{ColdStreak: 3, LastWake: at(9, 19, 0)},
			kind: KindSkip, reason: ReasonColdStreak, next: at(10, 0, 30)},
		{name: "连冷 3 次但上次醒是昨天：新的一天放行", now: at(9, 14, 0), st: State{ColdStreak: 3, LastWake: at(8, 22, 0)}, kind: KindWake, reason: ReasonDue},
		{name: "连冷 2 次：还找", now: at(9, 14, 0), st: State{ColdStreak: 2, LastWake: at(9, 9, 0)}, kind: KindWake, reason: ReasonDue},
		{name: "跨日：23:50 连冷被拦，次日 0:10 放行", now: at(9, 23, 50), st: State{ColdStreak: 3, LastWake: at(9, 23, 10)},
			kind: KindSkip, reason: ReasonColdStreak, next: at(10, 0, 30)},
		{name: "跨日后：0:10 上次醒在昨天", now: at(10, 0, 10), st: State{ColdStreak: 3, LastWake: at(9, 23, 10)}, kind: KindWake, reason: ReasonDue},
		{name: "主人 5 分钟前发过言：正在聊", now: at(9, 14, 0), sg: Snapshot{OwnerLastActive: at(9, 13, 55)},
			kind: KindSkip, reason: ReasonOwnerChatting, next: at(9, 14, 15)},
		{name: "主人刚好 10 分钟前：还算正在聊", now: at(9, 14, 0), sg: Snapshot{OwnerLastActive: at(9, 13, 50)}, kind: KindSkip, reason: ReasonOwnerChatting},
		{name: "主人 11 分钟前：不算", now: at(9, 14, 0), sg: Snapshot{OwnerLastActive: at(9, 13, 49)}, kind: KindWake, reason: ReasonDue},
		{name: "窗口在前台且本地 5 分钟内输入过：正在聊", now: at(9, 14, 0), sg: Snapshot{WindowForeground: true, LocalLastInput: at(9, 13, 55)},
			kind: KindSkip, reason: ReasonOwnerChatting},
		{name: "窗口在前台但很久没输入：不算在聊", now: at(9, 14, 0), sg: Snapshot{WindowForeground: true, LocalLastInput: at(9, 12, 0)}, kind: KindWake, reason: ReasonDue},
		{name: "没在前台的 5 分钟前本地输入：不算在聊", now: at(9, 14, 0), sg: Snapshot{LocalLastInput: at(9, 13, 55)}, kind: KindWake, reason: ReasonDue},
		{name: "未来的时间戳不算活跃", now: at(9, 14, 0), sg: Snapshot{OwnerLastActive: at(9, 14, 5)}, kind: KindWake, reason: ReasonDue},
		{name: "睡觉时段内没有活跃迹象：顺延到 09:00 再加 0～60 分钟", now: at(9, 2, 0),
			kind: KindSkip, reason: ReasonAsleepNoActive, next: at(9, 9, 30)},
		{name: "睡觉时段内主人 31 分钟前才发过言：仍算没活跃", now: at(9, 2, 0), sg: Snapshot{OwnerLastActive: at(9, 1, 29)}, kind: KindSkip, reason: ReasonAsleepNoActive},
		{name: "睡觉时段内主人 20 分钟前发过言：软信号让路", now: at(9, 2, 0), sg: Snapshot{OwnerLastActive: at(9, 1, 40)},
			kind: KindWake, reason: ReasonDueAsleepActive},
		{name: "睡觉时段内窗口在前台：算活跃", now: at(9, 2, 0), sg: Snapshot{WindowForeground: true}, kind: KindWake, reason: ReasonDueAsleepActive},
		{name: "睡觉时段内本地 20 分钟前有输入：算活跃", now: at(9, 2, 0), sg: Snapshot{LocalLastInput: at(9, 1, 40)}, kind: KindWake, reason: ReasonDueAsleepActive},
		{name: "睡觉时段内主人正在聊：先按正在聊顺延，不算睡觉拦", now: at(9, 2, 0), sg: Snapshot{OwnerLastActive: at(9, 1, 55)},
			kind: KindSkip, reason: ReasonOwnerChatting, next: at(9, 2, 15)},
		{name: "跨午夜睡觉区间 23:00～07:00，23:30 没活跃：顺延到次日 07:00", mod: func(p *config.Proactive) { p.Sleep.Start, p.Sleep.End = "23:00", "07:00" },
			now: at(9, 23, 30), kind: KindSkip, reason: ReasonAsleepNoActive, next: at(10, 7, 30)},
		{name: "跨午夜区间凌晨 3 点：顺延到当天 07:00", mod: func(p *config.Proactive) { p.Sleep.Start, p.Sleep.End = "23:00", "07:00" },
			now: at(10, 3, 0), kind: KindSkip, reason: ReasonAsleepNoActive, next: at(10, 7, 30)},
		{name: "顺序：别烦先于今天上限", now: at(9, 14, 0), st: State{QuietUntil: at(9, 15, 0)}, sg: Snapshot{ThreadsToday: 4}, kind: KindSkip, reason: ReasonSnoozed},
		{name: "顺序：今天上限先于已有开着的线", now: at(9, 14, 0), sg: Snapshot{ThreadsToday: 4, OpenThread: true}, kind: KindSkip, reason: ReasonDailyCap},
		{name: "顺序：已有开着的线先于正在聊", now: at(9, 14, 0), sg: Snapshot{OpenThread: true, OwnerLastActive: at(9, 13, 58)}, kind: KindSkip, reason: ReasonAlreadyOpen},
		{name: "顺序：正在聊先于睡觉", now: at(9, 3, 0), sg: Snapshot{OwnerLastActive: at(9, 2, 58)}, kind: KindSkip, reason: ReasonOwnerChatting},
		{name: "上限改成 2", mod: func(p *config.Proactive) { p.MaxThreadsPerDay = 2 }, now: at(9, 14, 0), sg: Snapshot{ThreadsToday: 2}, kind: KindSkip, reason: ReasonDailyCap},
		{name: "系数用状态里的", now: at(9, 14, 0), st: State{Factor: 0.5}, kind: KindWake, reason: ReasonDue, next: at(9, 14, 0).Add(75 * time.Minute)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := params()
			if c.mod != nil {
				c.mod(&p)
			}
			st := c.st
			if st.Factor == 0 {
				st.Factor = 1
			}
			d := Decide(p, st, c.sg, c.now, fixed(0.5))
			if d.Kind != c.kind || d.Reason != c.reason {
				t.Fatalf("结论 %s/%s，想要 %s/%s", d.Kind, d.Reason, c.kind, c.reason)
			}
			if !c.next.IsZero() && !d.NextAt.Equal(c.next) {
				t.Fatalf("下次 %v，想要 %v", d.NextAt, c.next)
			}
			if d.NextAt.Before(c.now) {
				t.Fatalf("下次 %v 比现在还早", d.NextAt)
			}
		})
	}
}

func TestOnCloseFactor(t *testing.T) {
	p := params()
	near := func(got, want float64) bool { d := got - want; return d > -1e-9 && d < 1e-9 }
	st := State{Factor: 1}
	st = OnClose(p, st, CloseHot)
	if !near(st.Factor, 0.7) {
		t.Fatalf("热一次：%v", st.Factor)
	}
	st = OnClose(p, st, CloseHot)
	st = OnClose(p, st, CloseHot)
	if !near(st.Factor, 0.5) {
		t.Fatalf("热多次要夹在下限 0.5：%v", st.Factor)
	}
	st = OnClose(p, st, CloseWarm)
	if !near(st.Factor, 0.65) {
		t.Fatalf("温向 1 靠拢 30%%：%v", st.Factor)
	}
	st = State{Factor: 2.5, ColdStreak: 1}
	st = OnClose(p, st, CloseWarm)
	if !near(st.Factor, 2.5-1.5*0.3) || st.ColdStreak != 0 {
		t.Fatalf("高于 1 的温也向 1 靠拢且清连冷：%+v", st)
	}

	st = State{Factor: 1}
	st = OnClose(p, st, CloseCold)
	if !near(st.Factor, 1.5) || st.ColdStreak != 1 {
		t.Fatalf("冷一次：%+v", st)
	}
	st = OnClose(p, st, CloseCold)
	st = OnClose(p, st, CloseCold)
	if !near(st.Factor, 3) || st.ColdStreak != 3 {
		t.Fatalf("冷多次要夹在上限 3：%+v", st)
	}
	before := st
	if got := OnClose(p, st, CloseQuiet); got != before {
		t.Fatalf("想了没开口不该改任何东西：%+v", got)
	}
	if got := OnClose(p, st, CloseSnoozed); got != before {
		t.Fatalf("别烦不该改系数：%+v", got)
	}
	st = OnClose(p, st, CloseHot)
	if st.ColdStreak != 0 {
		t.Fatalf("热要清连冷：%+v", st)
	}
	if got := OnClose(p, State{}, CloseCold); !near(got.Factor, 1.5) {
		t.Fatalf("系数为 0 的状态当 1：%+v", got)
	}
	until := at(9, 20, 0)
	if got := Snooze(State{Factor: 2}, until); !got.QuietUntil.Equal(until) || got.Factor != 2 {
		t.Fatalf("别烦：%+v", got)
	}
}

func TestSnapshotInputsHasOnlyNumbersAndBools(t *testing.T) {
	p := params()
	in := SnapshotInputs(p, State{Factor: 1.5, ColdStreak: 2}, Snapshot{
		LastContact: at(9, 11, 0), LastHeat: HeatWarm, ThreadsToday: 1, OwnerLastActive: at(9, 13, 0), WindowForeground: true,
	}, at(9, 14, 0))
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range m {
		switch v.(type) {
		case float64, bool:
		case string:
			if k != "heat" {
				t.Errorf("inputs 里不该有文字字段 %s=%v", k, v)
			}
		default:
			t.Errorf("inputs 里不该有 %s=%v", k, v)
		}
	}
	if in.SinceContactMin != 180 || in.OwnerLastActiveAgoMin != 60 || in.LocalLastInputAgoMin != -1 || in.InSleep || !in.WindowForeground || in.Heat != "warm" {
		t.Fatalf("快照：%+v", in)
	}
	if strings.Contains(string(b), "aa") {
		t.Fatal("快照里不该带角色或会话名")
	}
}
