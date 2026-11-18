package channel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/history"
	"sumika-native/internal/persona"
)

// 入库存行头（规格-渠道中间件 第六节第 5 步，Bun 的 segments.line）：重建实例以后，群友的话带名字和身份，
// 和接着用的实例里现排的那一行一样（以库为准）。名字、号都是编的。

func lineHook() LineEnv {
	return LineEnv{
		Location: time.FixedZone("", 8*3600),
		Owner:    "主人",
		NameOf: func(_ types.Conversation, id string) string {
			if id == other {
				return "路人乙"
			}
			return ""
		},
		Quote: func(_ types.Conversation, pid, _ string) (string, string, bool) {
			if pid == "q1" {
				return "路人乙", "上一句话\n带换行", true
			}
			return "", "", false
		},
	}
}

// instanceLine 是接着用的实例里这条消息现排的那一行：字段取法和 Pipeline.OnStored 交给链的 Msg 一样
func instanceLine(h *harness, in types.Inbound, s Stored, env LineEnv) string {
	env.Mine = h.in.cfg.Roster.accountsOf(in.Conv.Channel, s.Character)
	m := types.Msg{Seq: s.Seq, Time: in.Time, Who: s.Actor.ID, Origin: s.Origin, Segs: in.Segs, Name: nameOf(in.From)}
	return FormatLine(m, in.Conv, env, in.Time)
}

// idle 等 OnStored 也跑完（凭据比它早）
func (h *harness) idle() {
	h.t.Helper()
	if err := h.in.Idle(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

// rebuilt 是实例重建时这个会话里给角色 ca 看的历史的文字（user 角色的，按 seq 正序）
func rebuilt(t *testing.T, h *harness, conv types.Conversation) []string {
	t.Helper()
	_, msgs, _, err := history.For(h.st.Events, "ca", conv.String(), 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		out = append(out, m.Text)
	}
	return out
}

func TestStoredLineRebuildsWithNameAndRole(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(c *Config) { c.Line = lineHook() })
	g := group("g1")
	in := msg(acctA, g, "m1", friend, "你好[啊]")
	in.From.Card = "群名片甲"
	in.Segs = append([]types.Seg{{Kind: types.SegReply, Target: "q1"}, {Kind: types.SegAt, Target: acctA}, {Kind: types.SegAt, Target: other}}, in.Segs...)
	h.submit(in, Options{})
	h.idle()
	st := h.stored()[0]

	got := rebuilt(t, h, g)
	want := instanceLine(h, in, st, lineHook())
	if len(got) != 1 || got[0] != want {
		t.Fatalf("重建后的历史和现排的不一样\n历史 %q\n现排 %q", got, want)
	}
	for _, frag := range []string{"群名片甲(群友)：", "↪引用 路人乙：上一句话 带换行", "@你 ", "@路人乙 ", "你好［啊］", "+08:00"} {
		if !strings.Contains(got[0], frag) {
			t.Errorf("行里缺 %q：%q", frag, got[0])
		}
	}
}

func TestStoredLineByScene(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		conv types.Conversation
		from string
		want string
	}{
		{"QQ 群，群友", group("g1"), friend, "(群友)："},
		{"QQ 群，主人", group("g1"), owner, "(主人)："},
		{"QQ 私聊，好友", private(acctA, friend), friend, "(好友)："},
		{"QQ 私聊，主人", private(acctA, owner), owner, "(主人)："},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, func(cfg *Config) { cfg.Line = lineHook() })
			in := msg(acctA, c.conv, "m1", c.from, "在吗")
			h.submit(in, Options{})
			h.idle()
			got := rebuilt(t, h, c.conv)
			if len(got) != 1 || !strings.Contains(got[0], "昵称"+c.from+c.want+"在吗") {
				t.Fatalf("得到 %q，要带名字和 %s", got, c.want)
			}
			if got[0] != instanceLine(h, in, h.stored()[0], lineHook()) {
				t.Errorf("和现排的不一样：%q", got[0])
			}
		})
	}
}

// 卡上没写称呼、也没配 OwnerCall：所有者那一行叫"用户"
func TestStoredOwnerLabelDefaultsToUser(t *testing.T) {
	t.Parallel()
	env := lineHook()
	env.Owner = ""
	h := newHarness(t, func(cfg *Config) { cfg.Line = env })
	in := msg(acctA, group("g1"), "m1", owner, "在吗")
	h.submit(in, Options{})
	h.idle()
	got := rebuilt(t, h, group("g1"))
	if len(got) != 1 || !strings.Contains(got[0], "昵称"+owner+"(用户)：在吗") {
		t.Fatalf("得到 %q，所有者那一行该是 (用户)", got)
	}
}

// 卡上写了 ownerName：所有者那一行用卡上的称呼
func TestStoredOwnerLabelFromCard(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	card := filepath.Join(dir, "ca", "card")
	if err := os.MkdirAll(card, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(card, "character.yml"), []byte("name: 铃\nownerName: 主人\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := lineHook()
	env.Owner = ""
	h := newHarness(t, func(cfg *Config) {
		cfg.Line = env
		cfg.OwnerCall = func(character string) string { return persona.OwnerCallFor(dir, character) }
	})
	in := msg(acctA, group("g1"), "m1", owner, "在吗")
	h.submit(in, Options{})
	h.idle()
	got := rebuilt(t, h, group("g1"))
	if len(got) != 1 || !strings.Contains(got[0], "昵称"+owner+"(主人)：在吗") {
		t.Fatalf("得到 %q，所有者那一行该用卡上的 (主人)", got)
	}
}

// 本地私聊、本地群聊不走入库一站：主窗口的话照旧是原文，不带行头
func TestConsoleSaidByUnchanged(t *testing.T) {
	t.Parallel()
	for _, conv := range []string{"console:private:main", "console:group:room"} {
		e := history.Event{Channel: "console", Conversation: conv, ActorKind: "owner", Kind: "message", Text: "原文 [不转义]"}
		if got := history.SaidBy(e); got != e.Text {
			t.Errorf("%s：得到 %q，要原文", conv, got)
		}
	}
}

func TestStoredLineBackfillRecallSelf(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(c *Config) { c.Line = lineHook() })
	g := group("g1")
	t0 := baseTime.Add(-48 * time.Hour)

	bf := msg(acctA, g, "m1", friend, "补录来的")
	bf.Time = t0
	if _, err := h.in.Submit(context.Background(), bf, Options{Backfill: true}); err != nil {
		t.Fatal(err)
	}
	rc := msg(acctA, g, "m2", other, "马上撤回")
	h.submit(rc, Options{})
	if err := h.in.Notice(context.Background(), types.Notice{Kind: types.NoticeRecall, Account: acctA, Conv: g, Time: baseTime, Actor: "qq:" + other, MsgID: "m2"}); err != nil {
		t.Fatal(err)
	}
	self := msg(acctA, g, "m3", acctA, "我自己手动发的")
	self.Kind = types.InboundSelf
	h.submit(self, Options{})
	if err := h.in.Idle(context.Background()); err != nil {
		t.Fatal(err)
	}

	byText := map[string]string{}
	for _, m := range rebuilt(t, h, g) {
		for _, k := range []string{"补录来的", "马上撤回", "我自己手动发的"} {
			if strings.Contains(m, k) {
				byText[k] = m
			}
		}
	}
	// 补录的：行头用平台时间（48 小时前），不是入库的时间
	if want := lineTime(t0, lineHook().Location); !strings.HasPrefix(byText["补录来的"], "["+want+"] 昵称"+friend+"(群友)：") {
		t.Errorf("补录的行头不对：%q，时间要 %s", byText["补录来的"], want)
	}
	// 撤回的：撤回通知不改原来那一行
	if !strings.HasPrefix(byText["马上撤回"], "[") || !strings.Contains(byText["马上撤回"], "昵称"+other+"(群友)：马上撤回") {
		t.Errorf("被撤回的那条行头丢了：%q", byText["马上撤回"])
	}
	// 她自己的号发的：身份是“你的号”，前面带“不是你说的”的说明
	if s := byText["我自己手动发的"]; !strings.Contains(s, "(你的号)：我自己手动发的") || !strings.HasPrefix(s, "（这条是从你的号发出去的") {
		t.Errorf("她自己的号发的那条不对：%q", s)
	}
}

// 排行头出意外（钩子 panic）：这条照存，历史退回转义过的原文，后面的消息不受影响
func TestStoredLineHookPanicStillStores(t *testing.T) {
	t.Parallel()
	env := lineHook()
	env.Quote = func(types.Conversation, string, string) (string, string, bool) { panic("坏了") }
	h := newHarness(t, func(c *Config) { c.Line = env })
	g := group("g1")
	bad := msg(acctA, g, "m1", friend, "带引用")
	bad.Segs = append([]types.Seg{{Kind: types.SegReply, Target: "q1"}}, bad.Segs...)
	h.submit(bad, Options{})
	h.submit(msg(acctA, g, "m2", friend, "正常的"), Options{})
	got := rebuilt(t, h, g)
	if len(got) != 2 || got[0] != "带引用" || !strings.Contains(got[1], "(群友)：正常的") {
		t.Fatalf("得到 %q", got)
	}
	if !strings.Contains(h.logText(), "排给模型看的一行时出了意外") {
		t.Errorf("没留日志：%s", h.logText())
	}
}
