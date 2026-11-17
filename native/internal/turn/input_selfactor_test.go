package turn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
)

// "发言人不能是接收账号自己"的整张表：入口 × 形状。
// 入口列全本地私聊、本地群聊、QQ 私聊、QQ 群，外加没登记的虚构平台（telegram，以及渠道名和平台名不同的 tg）、话题线。
// 不看渠道名：规则对每个有接收账号的入口一视同仁；本地主窗口没有平台账号，只豁免"缺账号"，不豁免"发言人是账号自己"。
// runes 用码点拼字符串：不可见字符写成码点，源码里不放原字符。
func runes(rs ...rune) string { return string(rs) }

type selfEntry struct {
	name              string
	platform, channel string
	scene             string
	conv              string
	kind, actor       string
	account           string // 入口自己的账号写法（本地为空，或已绑的 "desktop"；QQ 群回合是真号或 "channel"）
	required          bool   // 这个入口该不该带接收账号
}

var selfEntries = []selfEntry{
	{"本地私聊（账号未填）", "local", "console", "private", "console:made-up", "owner", "owner", "", false},
	{"本地私聊（已绑 desktop）", "local", "console", "private", "console:made-up", "owner", "owner", "desktop", false},
	{"本地群聊", "local", "console", "group", "console:group:made-up", "owner", "owner", "", false},
	{"QQ 私聊", "qq", "qq", "private", "qq:private:91001:92001", "human", "qq:92001", "91001", true},
	{"QQ 群（真号）", "qq", "qq", "group", "qq:group:93001", "human", "qq:92001", "91001", true},
	{"QQ 群（channel 占位）", "qq", "qq", "group", "qq:group:93001", "human", "qq:92001", "channel", true},
	{"telegram 私聊", "telegram", "telegram", "private", "telegram:private:55001:66001", "human", "telegram:66001", "55001", true},
	{"telegram 群", "telegram", "telegram", "group", "telegram:group:77001", "human", "telegram:66001", "55001", true},
	{"渠道名和平台名不同的私聊", "tgplat", "tg", "private", "tg:private:55001:66001", "human", "tg:66001", "55001", true},
}

func (e selfEntry) input(character string, mutate func(*kernel.Scope)) Input {
	scope := kernel.Scope{CharacterID: character, Conversation: e.conv, ScopeID: "scope-a", Platform: e.platform, Channel: e.channel, Scene: e.scene, ActorKind: e.kind, ActorID: e.actor, AccountID: e.account}
	if mutate != nil {
		mutate(&scope)
	}
	return Input{Identity: kernel.Invocation{Scope: scope, Kind: kernel.KindForeground}, CharacterID: character, Conversation: e.conv, Scope: "scope-a", Audience: audience.Audience{Channel: e.channel, Scene: e.scene, Conversation: e.conv}, Text: "你好"}
}

type selfShape struct {
	name   string
	mutate func(e selfEntry, s *kernel.Scope)
	// reject 说这个入口上这个形状该不该被拒
	reject func(e selfEntry) bool
}

func always(selfEntry) bool         { return true }
func never(selfEntry) bool          { return false }
func onlyRequired(e selfEntry) bool { return e.required }

func selfShapes() []selfShape {
	setBoth := func(account, actor func(e selfEntry) string) func(e selfEntry, s *kernel.Scope) {
		return func(e selfEntry, s *kernel.Scope) { s.AccountID, s.ActorID = account(e), actor(e) }
	}
	acct := func(e selfEntry) string { return "777" }
	return []selfShape{
		{"入口原样", func(selfEntry, *kernel.Scope) {}, never},
		{"账号空", func(_ selfEntry, s *kernel.Scope) { s.AccountID = "" }, onlyRequired},
		{"账号只有空白", func(_ selfEntry, s *kernel.Scope) { s.AccountID = " \t" }, onlyRequired},
		{"账号只有零宽字符", func(_ selfEntry, s *kernel.Scope) { s.AccountID = "\u200b" }, onlyRequired},
		{"发言人就是账号（裸号）", setBoth(acct, acct), always},
		{"账号和发言人是同一串空白（原样相同）", setBoth(func(selfEntry) string { return " " }, func(selfEntry) string { return " " }), always},
		{"发言人尾部多变体选择符 U+FE0F", setBoth(acct, func(selfEntry) string { return "777" + runes(0xfe0f) }), always},
		{"发言人尾部多 CGJ U+034F", setBoth(acct, func(selfEntry) string { return "777" + runes(0x34f) }), always},
		{"发言人尾部多韩文填充 U+3164", setBoth(acct, func(selfEntry) string { return "777" + runes(0x3164) }), always},
		{"发言人前面多盲文空白 U+2800", setBoth(acct, func(selfEntry) string { return runes(0x2800) + "777" }), always},
		{"丢字符后才能组合（账号 é、发言人 e+ZWJ+U+0301）", setBoth(func(selfEntry) string { return runes(0xe9) }, func(selfEntry) string { return "e" + runes(0x200d, 0x301) }), always},
		{"发言人带渠道前缀", setBoth(acct, func(e selfEntry) string { return e.channel + ":777" }), always},
		{"发言人带平台前缀", setBoth(acct, func(e selfEntry) string { return e.platform + ":777" }), always},
		{"账号带渠道前缀、发言人裸号", setBoth(func(e selfEntry) string { return e.channel + ":777" }, acct), always},
		{"前缀大写", setBoth(acct, func(e selfEntry) string { return strings.ToUpper(e.channel) + ":777" }), always},
		{"前后空白", setBoth(acct, func(selfEntry) string { return " 777\u00a0" }), always},
		{"全角数字", setBoth(acct, func(selfEntry) string { return "７７７" }), always},
		{"叠两层前缀", setBoth(acct, func(e selfEntry) string { return e.channel + ":" + e.channel + ":777" }), always},
		{"占位值 channel 当发言人", setBoth(func(selfEntry) string { return "channel" }, func(selfEntry) string { return "channel" }), always},
		{"占位值 desktop 当发言人", setBoth(func(selfEntry) string { return "desktop" }, func(selfEntry) string { return "desktop" }), always},
		{"账号是 777、发言人是别人", setBoth(acct, func(e selfEntry) string {
			if e.kind == "owner" {
				return "owner"
			}
			return e.channel + ":778"
		}), never},
		{"号只是前缀相同", setBoth(acct, func(e selfEntry) string {
			if e.kind == "owner" {
				return "owner"
			}
			return e.channel + ":7771"
		}), never},
		{"前缀是别的渠道的不剥", setBoth(acct, func(e selfEntry) string {
			if e.kind == "owner" {
				return "owner"
			}
			return "other:777"
		}), never},
		// 平台和渠道拼不对：豁免要两个字段一起认，只认其中一个会放过这两格
		{"平台 local 渠道不是 console、账号空", func(e selfEntry, s *kernel.Scope) { s.Platform, s.AccountID = "local", "" }, func(e selfEntry) bool { return e.channel != "console" }},
		{"渠道 console 平台不是 local、账号空", func(e selfEntry, s *kernel.Scope) { s.Platform, s.AccountID = "qq", "" }, always},
	}
}

func TestSelfActorEntriesByShape(t *testing.T) {
	t.Parallel()
	const character = "selfactor-char"
	for _, e := range selfEntries {
		for _, sh := range selfShapes() {
			t.Run(e.name+"/"+sh.name, func(t *testing.T) {
				in := e.input(character, func(s *kernel.Scope) { sh.mutate(e, s) })
				err := validateInput(in)
				switch want := sh.reject(e); {
				case want && !errors.Is(err, ErrInputAuthority):
					t.Fatalf("应当拒绝却是 %v（scope=%+v）", err, in.Identity.Scope)
				case !want && err != nil:
					t.Fatalf("不该拒绝却是 %v（scope=%+v）", err, in.Identity.Scope)
				}
			})
		}
	}
}

// 话题线：system 发言人、本地主窗口，没有接收账号也要放行，放了账号也不被误拒
func TestSelfActorThreadEntry(t *testing.T) {
	t.Parallel()
	for _, account := range []string{"", "desktop"} {
		in := ThreadInput("selfactor-char", audience.ThreadConversation("selfactor-char", "t1"), []int64{1}, "醒来", nil)
		in.Identity.Scope.AccountID = account
		if err := validateInput(in); err != nil {
			t.Fatalf("话题线账号 %q 被拒：%v", account, err)
		}
	}
}

// 真入口：Hub.Submit 先过 validateInput 再查平台登记。这里只试已登记的 QQ（开始、插话、排队三种模式一视同仁）：
// 没登记的平台（telegram 等）在 Submit 里还有第二道独立的拒绝（KernelPlatform 对不上 Scope.Platform 也报 ErrInputAuthority），
// 在那里测"被拒"分不清是哪一道拒的，它们的身份门由上面整张表直接打 validateInput 来证明。
// 拒绝的形状选旧条件（只认 qq、只比裸号和小写 "qq:" 前缀）会放行的写法，所以换回旧条件这里会红；
// 每个入口再配一条原样输入作正例：它不能被身份门拒（后面成不成功不管），证明拒绝来自账号检查，不是别的。
func TestSelfActorSubmitGate(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	for _, e := range selfEntries {
		if e.platform != "qq" {
			continue
		}
		for _, mode := range []kernel.InputMode{kernel.InputStart, kernel.InputSteer, kernel.InputQueue} {
			for _, bad := range []struct {
				name           string
				account, actor string
			}{
				{"大写前缀", "777", "QQ:777"},
				{"全角", "777", "７７７"},
				{"前后空白", "777", " 777 "},
				{"变体选择符", "777", "777" + runes(0xfe0f)},
				{"账号带前缀", "qq:777", "777"},
				{"账号归一后才缺（只剩零宽字符）", runes(0x200b), "qq:1"},
				{"账号空", "", "qq:1"},
			} {
				in := e.input(h.c.CharacterID, func(s *kernel.Scope) { s.AccountID, s.ActorID = bad.account, bad.actor })
				if _, err := h.hub.Submit(context.Background(), Submission{Input: in, Mode: mode, ClientKey: e.name + bad.name + string(mode), ExpectedRunID: "run"}); !errors.Is(err, ErrInputAuthority) {
					t.Fatalf("%s %s %s：被受理 %v", e.name, mode, bad.name, err)
				}
			}
			ok := e.input(h.c.CharacterID, nil)
			if _, err := h.hub.Submit(context.Background(), Submission{Input: ok, Mode: mode, ClientKey: e.name + "正例" + string(mode), ExpectedRunID: "run"}); errors.Is(err, ErrInputAuthority) {
				t.Fatalf("%s %s：原样输入被身份门拒了 %v", e.name, mode, err)
			}
		}
	}
}

// 薄入口 Hub.trustedInput（Respond、RespondContext、SteerIn、Compact 都经它）：身份从事件库取，没有调用方给的 Scope。
// QQ 私聊的接收账号取会话串里的号；库里发言人是接收账号自己的事件（不该出现，Roster.Resolve 会把它认成角色，这里当伪造）一律拒，
// 带前缀、大写、变体写法一样；正常对方的事件放行。
func TestSelfActorTrustedInput(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	const conv = "qq:private:91001:92001"
	aud := audience.Audience{Channel: "qq", Scene: "private", Conversation: conv}
	input := func(actor string) Input {
		seq, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: actor, Origin: "human", Kind: "message", Text: "你好", CreatedAt: time.Now().UnixMilli()}, []string{h.c.CharacterID})
		if err != nil {
			t.Fatal(err)
		}
		return Input{CharacterID: h.c.CharacterID, Conversation: conv, Seqs: []int64{seq}, Text: "你好", Audience: aud, Addressed: true, Scope: "qq"}
	}
	for _, actor := range []string{"92001", "qq:92001"} {
		got, err := h.hub.trustedInput(input(actor))
		if err != nil {
			t.Fatalf("正常对方 %q 被拒：%v", actor, err)
		}
		if got.Identity.Scope.AccountID != "91001" {
			t.Fatalf("接收账号没取会话串里的号：%+v", got.Identity.Scope)
		}
	}
	for _, actor := range []string{"91001", "qq:91001", "QQ:91001", " 91001 ", "９１００１", "91001" + runes(0xfe0f), "qq:" + runes(0x200d) + "91001"} {
		if _, err := h.hub.trustedInput(input(actor)); !errors.Is(err, ErrInputAuthority) {
			t.Fatalf("库里发言人 %q 就是接收账号，却被受理：%v", actor, err)
		}
	}
}
