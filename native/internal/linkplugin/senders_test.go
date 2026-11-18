package linkplugin

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/plugintrace"
)

// 群里谁发的链接能触发展开（用户 2026-11-13，linkReader.expand.senders）：插件按发送者身份认，不认名字。

func TestSenderAllowedIdentity(t *testing.T) {
	e := expandSettings(func(e *config.LinkExpand) {
		e.Senders.Mode, e.Senders.Users = config.SendersWhitelist, []string{"111"}
	})().Settings.Expand
	cases := []struct {
		name, kind, id string
		want           bool
	}{
		{"名单里的群友", channel.ActorHuman, "qq:111", true},
		{"名单外的群友", channel.ActorHuman, "qq:222", false},
		{"主人", channel.ActorOwner, "owner", true},
		{"角色自己的号", channel.ActorCharacter, "c1", false},
		{"别的渠道前缀", channel.ActorHuman, "tg:111", false},
		{"号是空的", channel.ActorHuman, "qq:", false},
		{"没有前缀", channel.ActorHuman, "111", false},
		{"身份是空的", "", "", false},
		{"把名字写成主人也不算主人", channel.ActorHuman, "owner", false},
	}
	for _, c := range cases {
		if got := SenderAllowed(e, g1, c.kind, c.id); got != c.want {
			t.Errorf("%s：得到 %v，想要 %v", c.name, got, c.want)
		}
	}
}

// 整条路径：白名单里的人发的链接照常展开到准备发送；名单外的人发的记一行“不在展开名单里”，不取数、不展开
func TestSenderFilterOnStored(t *testing.T) {
	t.Parallel()
	link := []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/9"}}
	whitelist := func(e *config.LinkExpand) {
		e.Senders.Mode, e.Senders.Users = config.SendersWhitelist, []string{"111"}
	}
	exp, drafts := draftCapture()
	r := newRig(t, &fakeFetcher{}, Options{Settings: expandSettings(whitelist), Expand: exp})
	ok := storedMsg(4, link)
	ok.Actor = channel.Actor{Kind: channel.ActorHuman, ID: "qq:111"}
	r.p.OnStored(ok)
	nextDraft(t, drafts) // 名单里的人：走到准备发送
	r.idle()

	tr, sink := newTraceRig(t, &fakeFetcher{}, Options{Settings: expandSettings(whitelist), Expand: stubExpand(true)})
	no := storedMsg(5, link)
	no.Actor = channel.Actor{Kind: channel.ActorHuman, ID: "qq:222"}
	tr.p.OnStored(no)
	tr.idle()
	rows := sink.snapshot()
	if len(rows) != 1 || rows[0].Note != noteSender || rows[0].Result != plugintrace.Skip {
		t.Fatalf("名单外的人应当只记一行跳过：%+v", rows)
	}
}

// 设置改严以后：在途的展开被撤销（Refresh），发送前复查（permit）也拦下（复核 2026-11-13：这两处原来没有测试守着）
func TestSenderTightenedStopsInFlight(t *testing.T) {
	t.Parallel()
	var cur atomic.Pointer[Snapshot]
	all := expandSettings(nil)()
	cur.Store(&all)
	r := newRig(t, &fakeFetcher{}, Options{Settings: func() Snapshot { return *cur.Load() }, Expand: sendingExpand(confirmed, nil)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := storedMsg(7, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/9"}})
	src.Actor = channel.Actor{Kind: channel.ActorHuman, ID: "qq:222"}
	j := &expansionRun{source: src, ctx: ctx, cancel: cancel, state: ActiveJob{JobID: "编的任务"}}
	r.p.mu.Lock()
	r.p.active[j.state.JobID] = j
	r.p.mu.Unlock()

	if err := r.p.permit(j, nil); err != nil {
		t.Fatalf("全部放行时发送前复查应当通过：%v", err)
	}
	r.p.Refresh()
	if ctx.Err() != nil {
		t.Fatal("全部放行时不该撤销在途展开")
	}

	tight := expandSettings(func(e *config.LinkExpand) {
		e.Senders.Mode, e.Senders.Users = config.SendersWhitelist, []string{"111"}
	})()
	cur.Store(&tight)
	var blocked *Blocked
	if err := r.p.permit(j, nil); !errors.As(err, &blocked) || blocked.Why != notePermitGone {
		t.Fatalf("改成白名单后，名单外的人发的展开在发送前应当被拦下：%v", err)
	}
	r.p.Refresh()
	if ctx.Err() == nil {
		t.Fatal("改成白名单后，名单外的人发的在途展开应当被撤销")
	}
	r.p.mu.Lock()
	delete(r.p.active, j.state.JobID)
	r.p.mu.Unlock()
}
