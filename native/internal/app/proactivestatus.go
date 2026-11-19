package app

// 右栏“联系”标签的读写（规格-主动联系 第十节第 2 条）：读线表和导演日志，写只有两个按钮——别烦和关线。
// DesktopReader 实现 api.ProactiveReader、api.ProactiveControl；主动联系没打开的后台读到 Enabled 为假，写返回错误。

import (
	"context"
	"errors"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/proactive"
)

var (
	_ api.ProactiveReader  = (*DesktopReader)(nil)
	_ api.ProactiveControl = (*DesktopReader)(nil)
)

// proactiveStatusRows 是“最近的导演决定”列几条
const proactiveStatusRows = 10

// ErrProactiveOff 是主动联系没打开
var ErrProactiveOff = errors.New("主动联系没有打开")

func (r *DesktopReader) ProactiveStatus(ctx context.Context, character string) (ctlproto.ProactiveStatus, error) {
	return r.desk.app.proactiveStatus(ctx, character)
}

func (r *DesktopReader) ProactiveSnooze(ctx context.Context, character string) error {
	h := r.desk.app.pthreads
	if h == nil || !h.enabledFor(character) {
		return ErrProactiveOff
	}
	_, err := h.snooze(character, 0, "主人在右栏点了今天别找我")
	return err
}

func (r *DesktopReader) ProactiveCloseThread(ctx context.Context, character string) error {
	h := r.desk.app.pthreads
	if h == nil || !h.enabledFor(character) {
		return ErrProactiveOff
	}
	h.closeNow(character)
	return nil
}

func (a *App) proactiveStatus(ctx context.Context, character string) (ctlproto.ProactiveStatus, error) {
	out := ctlproto.ProactiveStatus{Decisions: []ctlproto.ProactiveRow{}}
	h := a.pthreads
	if h == nil || !h.enabledFor(character) {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	now := a.now()
	out.Enabled, out.Send = true, h.cfg().Send
	db := proactive.DBStore{DB: a.set.Trace()}
	if st, found, err := db.LoadState(character); err != nil {
		return out, err
	} else if found {
		out.Factor = st.Factor
		out.NextWake, out.QuietUntil = msOfTime(st.NextWake), msOfTime(st.QuietUntil)
	}
	threads, err := h.mgr.Recent(character, 1)
	if err != nil {
		return out, err
	}
	if open, ok, err := h.mgr.OpenOf(character); err != nil {
		return out, err
	} else if ok {
		threads = []proactive.Thread{open}
	}
	if len(threads) == 1 {
		t := threads[0]
		out.Thread = &ctlproto.ProactiveThreadView{ID: t.ID, Open: t.Open(), State: string(t.State), StateText: proactive.ThreadSnapshot(t, now),
			OpenedAt: msOfTime(t.OpenedAt), OpenedReason: t.OpenedReason, LastSendAt: msOfTime(t.LastSendAt), Sends: t.Sends,
			OwnerReplies: t.OwnerReplies, FollowUps: t.FollowUps, CloseReason: string(t.CloseReason), Conclusion: t.Conclusion}
		sends, err := h.mgr.Sends(t.ID)
		if err != nil {
			return out, err
		}
		for _, s := range sends {
			out.Visits = append(out.Visits, ctlproto.ProactiveVisit{At: msOfTime(s.At), Label: s.Label, State: s.State, Summary: s.Summary})
		}
	}
	rows, err := db.RecentFor(character, proactiveStatusRows)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Decisions = append(out.Decisions, ctlproto.ProactiveRow{ID: r.ID, At: msOfTime(r.At), Character: r.Character, Kind: string(r.Kind), Reason: r.Reason,
			Inputs: r.Inputs, NextAt: msOfTime(r.NextAt), Thread: r.Thread, Note: r.ModelNote, Count: r.Count})
	}
	return out, nil
}

func msOfTime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
