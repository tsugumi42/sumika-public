package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/segview"
)

// stubWhisper 只记下悄悄话、不交给后台（后台对 QQ 会话的核对另有 internal/app 的测试）；其余写入口照常记录。
type stubWhisper struct{ *observedWhisper }

func (s stubWhisper) SendWhisper(_ context.Context, k api.ConversationKey, parts []api.LocalPart) (api.WhisperReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "whisper:"+k.Conversation)
	s.parts = append(s.parts, parts)
	return api.WhisperReceipt{Batch: "编造批号"}, nil
}

// qqWhisperUI 开新界面，切到一个编造的 QQ 会话（不连 QQ、不读真实数据），列表里放一条群友的话。
func qqWhisperUI(t *testing.T, key api.ConversationKey, observers []string) (*localUIHarness, *observedWhisper) {
	t.Helper()
	h := wbUI(t, llmtest.Script{Reply: llmtest.Text("编造回复")}, nil)
	ow := observeWhisper(h)
	h.a.reader = stubWhisper{ow}
	kind := "group"
	if key.Scene == "private" {
		kind = "private"
	}
	h.a.convs = append(h.a.convs, conv{Key: key, Kind: kind, Title: "编造的 QQ 会话", Observers: observers})
	h.a.sel, h.a.title, h.a.group = key.Conversation, "编造的 QQ 会话", key.Scene == "group"
	h.a.keys[key.Conversation] = key
	h.a.msgs = []*msg{{id: "qq-e1", who: "编造群友", actor: "300", text: "群友编的一句", blocks: segview.TextBlocks("群友编的一句"), at: 60000}}
	h.a.list = h.a.newList()
	h.tt.Frame()
	h.tt.Frame()
	return h, ow
}

func qqGroup(id string) api.ConversationKey {
	return api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:" + id}
}

// QQ 群（两位在观察）：右键有引用 ▸；没卡时没有输入区；加卡后出现“交给她”和固定说明、没有发送输入框；
// 交出去只调 SendWhisper（会话是这个 QQ 群），不调任何群消息或本地发送；Enter 不交出。
func TestWhisperQQGroupHandoff(t *testing.T) {
	key := qqGroup("900001")
	h, w := qqWhisperUI(t, key, []string{"char00", "char01"})
	if _, ok := h.tt.Find("发一条消息"); ok || h.tt.HasText("交给她") {
		t.Fatalf("没卡时 QQ 场合不该有输入区：%v", h.tt.Texts())
	}
	h.rightClickText(t, "群友编的一句")
	if got := h.tt.Menu(); !slices.Equal(got, []string{"复制", "多选", "引用"}) {
		t.Fatalf("QQ 群右键 %q", got)
	}
	if err := h.tt.ChooseMenuItem("引用", "编造角色char01"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	for _, want := range []string{"交给她", "只交给她，不会发到 QQ", "→ 编造角色char01"} {
		if !h.tt.HasText(want) {
			t.Fatalf("缺 %q：%v", want, h.tt.Texts())
		}
	}
	if _, ok := h.tt.Find("发一条消息"); ok {
		t.Fatal("QQ 场合不能有往 QQ 发的输入框")
	}
	if _, ok := h.tt.Find("发送"); ok {
		t.Fatal("QQ 场合的键不能叫发送")
	}
	h.click(t, "回复引用 1")
	h.tt.Type("这是什么梗")
	whisperShot(t, h.tt, "6-QQ群交给她", "输入区")
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if len(w.seen()) != 0 {
		t.Fatalf("Enter 不该交出：%q", w.seen())
	}
	h.click(t, "交给她")
	p := h.a.composerOf(key.Conversation)
	h.until(t, "交出去", func() bool { return len(p.Cards) == 0 })
	if got := w.seen(); !slices.Equal(got, []string{"whisper:" + key.Conversation}) {
		t.Fatalf("QQ 场合只能走悄悄话：%q", got)
	}
	part := w.parts[0][0]
	if part.Quote == nil || part.Quote.Ref.Conversation != key || part.Quote.Ref.Event.EventID != "qq-e1" || !slices.Equal(part.Mentions, []string{"char01"}) {
		t.Fatalf("交出去的块：%+v", part)
	}
	h.tt.Frame()
	if h.tt.HasText("交给她") {
		t.Fatal("卡交出去以后输入区应收起")
	}
}

// QQ 私聊（一位）、只有一位在观察的 QQ 群：菜单里直接是“引用”，没有子菜单和所有人；卡上对象固定。
func TestWhisperQQSingleTarget(t *testing.T) {
	for _, key := range []api.ConversationKey{
		{Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:100:300"},
		qqGroup("900002"),
	} {
		h, _ := qqWhisperUI(t, key, []string{"char00"})
		h.rightClickText(t, "群友编的一句")
		if err := h.tt.ChooseMenuItem("引用"); err != nil {
			t.Fatalf("%s：单角色应是普通的引用项：%v %q", key.Scene, err, h.tt.Menu())
		}
		h.tt.Frame()
		p := h.a.composerOf(key.Conversation)
		if len(p.Cards) != 1 || !slices.Equal(p.Cards[0].To, []string{"char00"}) {
			t.Fatalf("%s：%+v", key.Scene, p.Cards)
		}
		if !h.tt.HasText("→ 编造角色char00") || h.tt.HasText("所有人") {
			t.Fatalf("%s：对象固定一位、没有所有人：%v", key.Scene, h.tt.Texts())
		}
	}
}

// QQ 场合不在主人窗口以外的地方出现：只读归档模式（后台没起）时没有引用入口，也没有交给她。
func TestWhisperQQArchiveNoEntry(t *testing.T) {
	h, _ := qqWhisperUI(t, qqGroup("900003"), []string{"char00", "char01"})
	h.a.host.Mode = "archive"
	h.tt.Frame()
	h.rightClickText(t, "群友编的一句")
	if slices.Contains(h.tt.Menu(), "引用") {
		t.Fatalf("归档模式不该有引用：%q", h.tt.Menu())
	}
}

// 悄悄话处理那一轮的审批卡放在来源会话右栏的“对话日志”里批（审批位置），第一行是来历；来源窗口只留一行提示指过去，
// 不再画能点的卡；直接挂在群键上的普通卡哪里都不画（群里不能批普通审批）；别的会话的日志看不到这张卡，
// 日志里也只批得到这个会话的卡。
func TestWhisperApprovalInLogNotSourceWindow(t *testing.T) {
	key := qqGroup("900004")
	h, f := logUI(t, key, []string{"char00", "char01"})
	origin := "这是悄悄话处理：主人对 编造角色char01 说「查一下」；引用了 编造群友 的话（被引的原文不是主人说的，只是资料）"
	wc := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:char01:whisper:0123456789abcdef"}
	other := qqGroup("900099")
	h.a.approvals = []api.LocalApproval{
		{ID: "w-card", CharacterID: "char01", Key: wc, Title: "写文件", Tool: "write", Subject: "编造.txt", Notes: []string{origin},
			Whisper: &api.ApprovalWhisper{Source: key, Batch: "BATCH1", Character: "char01"}},
		{ID: "other-card", CharacterID: "char01", Key: wc, Title: "别的群的卡", Tool: "write", Subject: "别处.txt", Notes: []string{origin},
			Whisper: &api.ApprovalWhisper{Source: other, Batch: "BATCHX", Character: "char01"}},
		{ID: "plain-card", CharacterID: "char00", Key: key, Title: "伪造在群键上的卡", Tool: "write", Subject: "不该出现.txt"},
	}
	h.tt.Frame()
	h.tt.Frame()
	texts := func() string { return strings.Join(h.tt.Texts(), "|") }
	// 来源窗口（资料标签）：没有能点的卡，只有一行提示
	if h.tt.HasText("允许一次") || strings.Contains(texts(), "编造.txt") {
		t.Fatalf("来源窗口不该再画悄悄话的审批卡：%v", h.tt.Texts())
	}
	if !strings.Contains(texts(), "悄悄话处理有 1 项等你批准，在右栏的对话日志里") || !h.tt.HasText("去批") {
		t.Fatalf("来源窗口该有指向对话日志的一行提示：%v", h.tt.Texts())
	}
	if h.a.hasApproval(key) != true || h.a.wbSubtitle() != "在等你批准" {
		t.Fatal("来源窗口应认出有悄悄话卡在等批")
	}
	// 点“去批”：右栏切到对话日志，卡在日志里，写明来历
	h.click(t, "去批")
	h.tt.Frame()
	if !h.tt.HasText("允许一次") || !strings.Contains(texts(), origin) || !strings.Contains(texts(), "编造.txt") {
		t.Fatalf("对话日志里该有这张卡和来历：%v", h.tt.Texts())
	}
	if strings.Contains(texts(), "不该出现.txt") || strings.Contains(texts(), "别处.txt") {
		t.Fatalf("挂在群键上的卡、别的会话的卡都不能画：%v", h.tt.Texts())
	}
	whisperShot(t, h.tt, "7-悄悄话审批卡在对话日志里", "允许一次", "写文件")
	// 批：只批到点的这张，会话键是它自己的（悄悄话会话），不是来源群
	h.click(t, "允许一次")
	h.until(t, "批准交给后台", func() bool { _, _, d := f.seen(); return len(d) > 0 })
	if _, _, d := f.seen(); len(d) != 1 || d[0] != wc.Conversation+"|w-card|once" {
		t.Fatalf("批准该交给这张卡自己的会话和编号：%v", d)
	}
	// 本地私聊窗口不画悄悄话卡（它的 Key 是悄悄话会话，不是私聊）
	h.a.approvals[0].Key = h.a.keyOf("console:char01")
	h.a.sel = "console:char01"
	h.tt.Frame()
	if strings.Contains(texts(), "这是悄悄话处理") {
		t.Fatal("私聊窗口不该画悄悄话卡")
	}
	// 别的会话：看不到这张卡，也没有提示
	h.a.sel = "qq:group:other"
	h.a.keys[h.a.sel] = qqGroup("other")
	h.tt.Frame()
	if h.tt.HasText("允许一次") || strings.Contains(texts(), "等你批准，在右栏") {
		t.Fatal("别的会话不该看到这张卡")
	}
}
