package rpcconv

// 目标、定期任务、提醒的转换（规格-Bun后台第3批 第十五节）。约定同 conv.go：空切片不发、收回来是 nil。
// ReminderPatch 是例外里的例外：api 用指针表示「改不改这一项」，线上标量和数组零值不发，所以单独用 set 列出改了哪几项。

import (
	"slices"

	"sumika-native/internal/api"
	"sumika-native/internal/rpcwire"
)

func ints(s []int) []int {
	if len(s) == 0 {
		return nil
	}
	return slices.Clone(s)
}

func GoalInfoOut(g api.GoalInfo) rpcwire.GoalInfo {
	return rpcwire.GoalInfo{Text: g.Text, State: g.State, PauseReason: g.PauseReason, Rounds: g.Rounds, MaxRounds: g.MaxRounds, TokensUsed: g.TokensUsed, MaxTokens: g.MaxTokens,
		StartedAt: g.StartedAt, LastReason: g.LastReason, LastAchieved: g.LastAchieved, LastFailed: g.LastFailed, LastAt: g.LastAt}
}

func GoalInfoIn(g rpcwire.GoalInfo) api.GoalInfo {
	return api.GoalInfo{Text: g.Text, State: g.State, PauseReason: g.PauseReason, Rounds: g.Rounds, MaxRounds: g.MaxRounds, TokensUsed: g.TokensUsed, MaxTokens: g.MaxTokens,
		StartedAt: g.StartedAt, LastReason: g.LastReason, LastAchieved: g.LastAchieved, LastFailed: g.LastFailed, LastAt: g.LastAt}
}

func loopRunOut(r api.LoopRun) rpcwire.LoopRun {
	return rpcwire.LoopRun{At: r.At, Result: r.Result, Note: r.Note, Missed: r.Missed}
}

func loopRunIn(r rpcwire.LoopRun) api.LoopRun {
	return api.LoopRun{At: r.At, Result: r.Result, Note: r.Note, Missed: r.Missed}
}

func LoopInfoOut(l api.LoopInfo) rpcwire.LoopInfo {
	return rpcwire.LoopInfo{ID: l.ID, Prompt: l.Prompt, Status: l.Status, IntervalSec: l.IntervalSec, NextRun: l.NextRun, NextReason: l.NextReason, Runs: l.Runs, LastRun: l.LastRun,
		LastResult: l.LastResult, LastNote: l.LastNote, Expires: l.Expires, Kind: l.Kind, At: l.At, Days: ints(l.Days), Notify: l.Notify, NewChat: l.NewChat, History: mapSlice(l.History, loopRunOut)}
}

func LoopInfoIn(l rpcwire.LoopInfo) api.LoopInfo {
	return api.LoopInfo{ID: l.ID, Prompt: l.Prompt, Status: l.Status, IntervalSec: l.IntervalSec, NextRun: l.NextRun, NextReason: l.NextReason, Runs: l.Runs, LastRun: l.LastRun,
		LastResult: l.LastResult, LastNote: l.LastNote, Expires: l.Expires, Kind: l.Kind, At: l.At, Days: ints(l.Days), Notify: l.Notify, NewChat: l.NewChat, History: mapSlice(l.History, loopRunIn)}
}

func LoopSpecOut(s api.LoopSpec) rpcwire.LoopSpec {
	return rpcwire.LoopSpec{Prompt: s.Prompt, Kind: s.Kind, IntervalSec: s.IntervalSec, At: s.At, Days: ints(s.Days), Notify: s.Notify, NewChat: s.NewChat}
}

func LoopSpecIn(s rpcwire.LoopSpec) api.LoopSpec {
	return api.LoopSpec{Prompt: s.Prompt, Kind: s.Kind, IntervalSec: s.IntervalSec, At: s.At, Days: ints(s.Days), Notify: s.Notify, NewChat: s.NewChat}
}

func ReminderRepeatOut(r api.ReminderRepeat) rpcwire.ReminderRepeat {
	return rpcwire.ReminderRepeat{Unit: r.Unit, Every: r.Every, Weekdays: ints(r.Weekdays)}
}

func ReminderRepeatIn(r rpcwire.ReminderRepeat) api.ReminderRepeat {
	return api.ReminderRepeat{Unit: r.Unit, Every: r.Every, Weekdays: ints(r.Weekdays)}
}

func reminderEventOut(e api.ReminderEvent) rpcwire.ReminderEvent {
	return rpcwire.ReminderEvent{At: e.At, Kind: e.Kind, By: e.By, Note: e.Note}
}

func reminderEventIn(e rpcwire.ReminderEvent) api.ReminderEvent {
	return api.ReminderEvent{At: e.At, Kind: e.Kind, By: e.By, Note: e.Note}
}

func ReminderInfoOut(r api.ReminderInfo) rpcwire.ReminderInfo {
	return rpcwire.ReminderInfo{ID: r.ID, Character: r.Character, Text: r.Text, At: r.At, NextAt: r.NextAt, Status: r.Status, Source: r.Source, Repeat: ReminderRepeatOut(r.Repeat),
		RepeatText: r.RepeatText, Targets: strs(r.Targets), EffectiveTargets: strs(r.EffectiveTargets), Rings: r.Rings, LastRing: r.LastRing, LastNote: r.LastNote, EndNote: r.EndNote,
		EndedAt: r.EndedAt, Created: r.Created, History: mapSlice(r.History, reminderEventOut)}
}

func ReminderInfoIn(r rpcwire.ReminderInfo) api.ReminderInfo {
	return api.ReminderInfo{ID: r.ID, Character: r.Character, Text: r.Text, At: r.At, NextAt: r.NextAt, Status: r.Status, Source: r.Source, Repeat: ReminderRepeatIn(r.Repeat),
		RepeatText: r.RepeatText, Targets: strs(r.Targets), EffectiveTargets: strs(r.EffectiveTargets), Rings: r.Rings, LastRing: r.LastRing, LastNote: r.LastNote, EndNote: r.EndNote,
		EndedAt: r.EndedAt, Created: r.Created, History: mapSlice(r.History, reminderEventIn)}
}

func ReminderDraftOut(d api.ReminderDraft) rpcwire.ReminderDraft {
	return rpcwire.ReminderDraft{Text: d.Text, At: d.At, Repeat: ReminderRepeatOut(d.Repeat), Targets: strs(d.Targets)}
}

func ReminderDraftIn(d rpcwire.ReminderDraft) api.ReminderDraft {
	return api.ReminderDraft{Text: d.Text, At: d.At, Repeat: ReminderRepeatIn(d.Repeat), Targets: strs(d.Targets)}
}

// ReminderPatchOut：指针非空的项进 set，值照放
func ReminderPatchOut(p api.ReminderPatch) rpcwire.ReminderPatch {
	var w rpcwire.ReminderPatch
	if p.Text != nil {
		w.Set, w.Text = append(w.Set, "text"), *p.Text
	}
	if p.At != nil {
		w.Set, w.At = append(w.Set, "at"), *p.At
	}
	if p.Repeat != nil {
		w.Set, w.Repeat = append(w.Set, "repeat"), ReminderRepeatOut(*p.Repeat)
	}
	if p.Targets != nil {
		w.Set, w.Targets = append(w.Set, "targets"), strs(*p.Targets)
	}
	if p.Paused != nil {
		w.Set, w.Paused = append(w.Set, "paused"), *p.Paused
	}
	return w
}

func ReminderPatchIn(w rpcwire.ReminderPatch) api.ReminderPatch {
	var p api.ReminderPatch
	for _, name := range w.Set {
		switch name {
		case "text":
			v := w.Text
			p.Text = &v
		case "at":
			v := w.At
			p.At = &v
		case "repeat":
			v := ReminderRepeatIn(w.Repeat)
			p.Repeat = &v
		case "targets":
			v := slices.Clone(w.Targets)
			if v == nil {
				v = []string{}
			}
			p.Targets = &v
		case "paused":
			v := w.Paused
			p.Paused = &v
		}
	}
	return p
}

func ReminderOptionsOut(o api.ReminderOptions) rpcwire.ReminderOptions {
	return rpcwire.ReminderOptions{Enabled: o.Enabled, AllowQq: o.AllowQQ, DefaultTargets: strs(o.DefaultTargets)}
}

func ReminderOptionsIn(o rpcwire.ReminderOptions) api.ReminderOptions {
	return api.ReminderOptions{Enabled: o.Enabled, AllowQQ: o.AllowQq, DefaultTargets: strs(o.DefaultTargets)}
}

func LocalStatusOut(s api.LocalStatus) rpcwire.LocalStatus {
	w := rpcwire.LocalStatus{Key: KeyOut(s.Key), GoalLine: s.GoalLine, GoalState: s.GoalState, AutoRunning: s.AutoRunning, Typing: s.Typing, Loops: strs(s.Loops),
		LoopTasks: mapSlice(s.LoopTasks, LoopInfoOut), Reminders: mapSlice(s.Reminders, ReminderInfoOut)}
	if s.Goal != nil {
		g := GoalInfoOut(*s.Goal)
		w.Goal = &g
	}
	return w
}

func LocalStatusIn(w rpcwire.LocalStatus) api.LocalStatus {
	s := api.LocalStatus{Key: KeyIn(w.Key), GoalLine: w.GoalLine, GoalState: w.GoalState, AutoRunning: w.AutoRunning, Typing: w.Typing, Loops: strs(w.Loops),
		LoopTasks: mapSlice(w.LoopTasks, LoopInfoIn), Reminders: mapSlice(w.Reminders, ReminderInfoIn)}
	if w.Goal != nil {
		g := GoalInfoIn(*w.Goal)
		s.Goal = &g
	}
	return s
}

func LocalStatusesOut(l []api.LocalStatus) []rpcwire.LocalStatus { return mapSlice(l, LocalStatusOut) }
func LocalStatusesIn(l []rpcwire.LocalStatus) []api.LocalStatus  { return mapSlice(l, LocalStatusIn) }
