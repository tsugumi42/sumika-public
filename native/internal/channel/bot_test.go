package channel

import (
	"context"
	"testing"

	"sumika-native/internal/channel/types"
)

func TestBotAliasBoundaries(t *testing.T) {
	t.Parallel()
	aliases := []string{"小澪", "DS", "R1", "ro", "te", "seek", "澪", "D S"}
	positive := []string{"小澪在吗", "叫小澪", "小澪？", "你好小澪", "小澪，来", "DS", "ds", "Ds!", "(DS)", "@DS", "中文DS中文", "R1", "r1?", "[R1]", "RO", "ro 在吗", "te", "TE?", "seek", "小澪和 DS"}
	negative := []string{"ADS", "BDSM", "DSL", "dsx", "xDS", "_DS", "DS_", "DS2", "2DS", "R12", "XR1", "R1x", "pro", "test", "deepseek", "roster", "teapot", "seeker", "澪在吗", "没有名字"}
	for _, text := range positive {
		if !aliasHit(aliases, text) {
			t.Errorf("漏叫名 %q", text)
		}
	}
	for _, text := range negative {
		if aliasHit(aliases, text) {
			t.Errorf("误叫名 %q", text)
		}
	}
	get := func(string) []string { return aliases }
	if aliasInSegs(get, "ca", []types.Seg{{Kind: types.SegReply, Text: "小澪"}, {Kind: types.SegCard, Text: "DS"}, {Kind: types.SegForward, Text: "R1"}}) {
		t.Fatal("引用、卡片、转发内的字触发叫名")
	}
	if aliasInSegs(nil, "ca", []types.Seg{{Kind: types.SegText, Text: "小澪"}}) {
		t.Fatal("空表触发叫名")
	}
	if !aliasInSegs(get, "ca", []types.Seg{{Kind: types.SegText, Text: "D"}, {Kind: types.SegText, Text: "S"}}) {
		t.Fatal("相邻文字段的名字没有连起来")
	}
	if aliasInSegs(get, "ca", []types.Seg{{Kind: types.SegText, Text: "D"}, {Kind: types.SegReply, Text: "DS"}, {Kind: types.SegText, Text: "S"}}) {
		t.Fatal("跨过引用把两段文字拼成了别名")
	}
	t.Logf("编造叫名样本：正 %d，负 %d", len(positive), len(negative))
}

func TestBotDeniedPendingContext(t *testing.T) {
	t.Parallel()
	h := newGH(t, nil, func(c *GateConfig) {
		c.Roster.NoReply = map[string]string{"qq:" + friend: "blocked"}
		c.Aliases = func(string) []string { return []string{"小澪"} }
	})
	conv := group("5001")
	h.seed(conv, highRounds(), 0)
	// 直接从测试的流水线交给闸门，也必须拦住；不能只依赖正式 Pipeline 的过滤。
	deniedID := h.say(conv, friend, "小澪在吗", 0, atHer())
	if len(h.released()) != 0 {
		t.Fatal("名单者 @ 或叫名触发闸门")
	}
	h.say(conv, other, "看上面", 1, types.Seg{Kind: types.SegReply, Target: deniedID})
	if len(h.released()) != 0 {
		t.Fatal("正常人引用名单者的话继承了旧消息的叫名")
	}
	h.say(conv, owner, "正常人提问", 2, atHer())
	got := h.released()
	if len(got) != 1 || len(got[0].Msgs) != 3 {
		t.Fatalf("混批上下文没保留：%+v", got)
	}
	if got[0].Msgs[0].Addressed || got[0].Msgs[1].Addressed || !got[0].Msgs[2].Addressed {
		t.Fatal("名单者在重读攒批时恢复了叫到资格")
	}
}

func TestBotPipelineScenesAndAliasPriority(t *testing.T) {
	t.Parallel()
	for _, cv := range []types.Conversation{
		{Channel: "console", Scene: types.ScenePrivate, Self: "local", ID: "owner"},
		{Channel: "console", Scene: types.SceneGroup, ID: "room"},
		{Channel: "qq", Scene: types.ScenePrivate, Self: acctA, ID: friend},
		{Channel: "qq", Scene: types.SceneGroup, ID: "5001"},
	} {
		t.Run(cv.String(), func(t *testing.T) {
			t.Parallel()
			plugin := types.Interceptor{Name: "repeat-probe", Occasions: []types.Occasion{{Scene: cv.Scene}}, Triggers: []string{types.TriggerMessage},
				Intercept: func(_ context.Context, b types.Batch) types.Verdict {
					if !b.Addressed {
						return types.Took("没有叫名才接")
					}
					return types.Passed("叫名让路")
				}}
			h := newPH(t, []types.Interceptor{plugin}, func(_ *Config, p *PipelineConfig, _ *ChainConfig) {
				p.Roster.NoReply = map[string]string{"qq:" + friend: "blocked"}
				p.Aliases = func(string) []string { return []string{"DS"} }
			})
			s := Stored{Character: "ca", Seq: 1, Observed: true, Origin: OriginPeer,
				Actor:   Actor{Kind: ActorHuman, ID: cv.Channel + ":" + friend},
				Inbound: types.Inbound{Account: acctA, Conv: cv, From: types.User{ID: friend, Nick: "主人"}, Segs: []types.Seg{{Kind: types.SegText, Text: "DS?"}}}}
			h.pl.OnStored(s)
			if got := h.dispatches(); cv.Channel == "qq" {
				if len(got) != 0 {
					t.Fatal("QQ 名单者触发流水线")
				}
			} else if len(got) != 1 || !got[0].Batch.Addressed || got[0].Result.Taken {
				t.Fatalf("QQ 名单串到本地或叫名未让插件让路：%+v", got)
			}
			s.Seq = 2
			s.Actor = Actor{Kind: ActorOwner, ID: "owner"}
			h.pl.OnStored(s)
			got := h.dispatches()
			last := got[len(got)-1]
			if !last.Batch.Addressed || last.Result.Taken {
				t.Fatalf("叫名没在链前算好：%+v", last)
			}
			s.Seq = 3
			s.Inbound.From.Nick = "DS"
			s.Inbound.Segs = []types.Seg{{Kind: types.SegText, Text: "普通消息"}}
			h.pl.OnStored(s)
			got = h.dispatches()
			last = got[len(got)-1]
			if last.Batch.Addressed || !last.Result.Taken {
				t.Fatalf("昵称触发叫名，或探针没经过链：%+v", last)
			}
		})
	}
}
