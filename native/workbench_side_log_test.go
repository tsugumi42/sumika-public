package main

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
)

// 右栏“对话日志”（规格-群聊引用和路由 第十五节）：悄悄话、每位被指角色的安排和状态、她的过程、点开读全文、取消、
// 审批卡放在日志里批。名字、话、批号都是编的；后台是假的，只记下界面调了什么。

type fakeLogSub struct {
	ch   chan api.Snapshot[ctlproto.LogPage]
	once sync.Once
}

func (s *fakeLogSub) Updates() <-chan api.Snapshot[ctlproto.LogPage] { return s.ch }
func (s *fakeLogSub) Close()                                         { s.once.Do(func() { close(s.ch) }) }

// fakeLog 在 stubWhisper 上加对话日志、审批入口：页是预先给的，读到的、取消的、批准的都记下来。
type fakeLog struct {
	stubWhisper
	mu       sync.Mutex
	page     ctlproto.LogPage
	older    ctlproto.LogPage
	details  map[string]ctlproto.LogDetail
	read     []string
	cancels  []string
	decided  []string
	watching int
}

func (f *fakeLog) ReadWhisperLog(_ context.Context, _ api.ConversationKey, cursor string, _ int) (ctlproto.LogPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.read = append(f.read, "page:"+cursor)
	return f.older, nil
}
func (f *fakeLog) ReadWhisperLogEntry(_ context.Context, _ api.ConversationKey, id string, _ bool) (ctlproto.LogDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.read = append(f.read, "entry:"+id)
	return f.details[id], nil
}
func (f *fakeLog) WatchWhisperLog(_ context.Context, _ api.ConversationKey) (api.Subscription[ctlproto.LogPage], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watching++
	s := &fakeLogSub{ch: make(chan api.Snapshot[ctlproto.LogPage], 4)}
	s.ch <- api.Snapshot[ctlproto.LogPage]{Version: 1, Result: api.Result[ctlproto.LogPage]{State: api.Ready, Value: f.page}}
	return s, nil
}
func (f *fakeLog) CancelWhisper(_ context.Context, k api.ConversationKey, batch, character string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, k.Conversation+"|"+batch+"|"+character)
	return 1, nil
}
func (f *fakeLog) SendLocal(context.Context, api.ConversationKey, string, func(api.LocalDraft)) error {
	return nil
}
func (f *fakeLog) StopLocal(context.Context, api.ConversationKey) error { return nil }
func (f *fakeLog) WatchLocalApprovals(context.Context) (api.Subscription[[]api.LocalApproval], error) {
	return nil, api.ErrLocalChatUnsupported
}
func (f *fakeLog) DecideLocal(_ context.Context, k api.ConversationKey, id string, c api.ApprovalChoice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decided = append(f.decided, k.Conversation+"|"+id+"|"+string(c))
	return nil
}

func (f *fakeLog) seen() (read, cancels, decided []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.read), slices.Clone(f.cancels), slices.Clone(f.decided)
}

func samplePage() ctlproto.LogPage {
	const b = "BATCH1"
	return ctlproto.LogPage{Running: true, Entries: []ctlproto.LogEntry{
		{ID: "say:" + b, Batch: b, Kind: ctlproto.LogWhisper, At: 60000, Targets: []string{"char00", "char01"}, Summary: "帮我查一下这个梗",
			Quotes: []ctlproto.LogQuote{{Name: "编造群友", Text: "这个梗是什么意思"}}},
		{ID: "arr:" + b + ":char00", Batch: b, Kind: ctlproto.LogArrange, Character: "char00", Parent: "say:" + b, At: 60001, State: ctlproto.WhisperProcessing},
		{ID: "step:t1", Batch: b, Kind: ctlproto.LogThink, Character: "char00", Parent: "arr:" + b + ":char00", At: 60002, Runes: 42, Summary: "先看看这句话"},
		{ID: "step:t2", Batch: b, Kind: ctlproto.LogTool, Character: "char00", Parent: "arr:" + b + ":char00", At: 60003, State: "waiting", Waiting: true, Summary: "web_fetch：编造链接"},
		{ID: "arr:" + b + ":char01", Batch: b, Kind: ctlproto.LogArrange, Character: "char01", Parent: "say:" + b, At: 60001, State: ctlproto.WhisperQueued, Position: 1},
		{ID: "note:" + b + ":char01", Batch: b, Kind: ctlproto.LogNote, Character: "char01", Parent: "arr:" + b + ":char01", At: 60009, Summary: "是个群里的梗"},
	}}
}

// logUI 开一个 QQ 群、装上假日志后端，右栏打开。
func logUI(t *testing.T, key api.ConversationKey, observers []string) (*localUIHarness, *fakeLog) {
	t.Helper()
	h, ow := qqWhisperUI(t, key, observers)
	f := &fakeLog{stubWhisper: stubWhisper{ow}, page: samplePage(), details: map[string]ctlproto.LogDetail{
		"note:BATCH1:char01": {ID: "note:BATCH1:char01", Kind: ctlproto.LogNote, Title: "处理记录 char01", Text: "完整的处理记录：这是群里的梗，来源是某个视频。"},
		"step:t1":            {ID: "step:t1", Kind: ctlproto.LogThink, Title: "思考", Text: "思考原文：先看看这句话是什么意思，再决定查不查。"},
	}}
	h.a.reader = f
	h.a.workbench.shell.st.Panes.SideOpen = true
	h.tt.Frame()
	return h, f
}

func TestSideLogShowsBatchStatesAndOpensDetail(t *testing.T) {
	key := qqGroup("900010")
	h, f := logUI(t, key, []string{"char00", "char01"})
	if !h.tt.HasText("对话日志") {
		t.Fatalf("QQ 群该有对话日志标签：%v", h.tt.Texts())
	}
	h.click(t, "对话日志")
	h.until(t, "日志出现", func() bool { return h.tt.HasText("处理中") })
	joined := strings.Join(h.tt.Texts(), "|")
	for _, want := range []string{"悄悄话 → 编造角色char00、编造角色char01", "编造群友：这个梗是什么意思", "帮我查一下这个梗",
		"编造角色char00", "处理中", "排队中，前面还有 1 个", "思考 · 42 字 · 先看看这句话", "工具 · 等你批准 · web_fetch：编造链接", "处理记录 · 是个群里的梗", "取消"} {
		if !strings.Contains(joined, want) {
			t.Errorf("日志里缺 %q：%q", want, h.tt.Texts())
		}
	}
	// 思考原文、处理记录全文平时不显示，点开才读
	if strings.Contains(joined, "思考原文") || strings.Contains(joined, "完整的处理记录") {
		t.Fatal("全文该折叠")
	}
	h.click(t, "处理记录 · 是个群里的梗")
	h.until(t, "读到全文", func() bool {
		return h.tt.HasText("完整的处理记录：这是群里的梗，来源是某个视频。")
	})
	if read, _, _ := f.seen(); !slices.Contains(read, "entry:note:BATCH1:char01") {
		t.Fatalf("点开该读这一条的全文：%v", read)
	}
	whisperShot(t, h.tt, "8-对话日志", "编造群友：这个梗是什么意思", "完整的处理记录：这是群里的梗，来源是某个视频。")
	// 取消：走 CancelWhisper，批号和会话都是这一批的
	h.click(t, "取消")
	h.until(t, "取消交给后台", func() bool { _, c, _ := f.seen(); return len(c) > 0 })
	if _, c, _ := f.seen(); len(c) != 1 || c[0] != key.Conversation+"|BATCH1|char00" { // 点的是 char00 那一行的取消，不连累另一位
		t.Fatalf("取消的会话和批号：%v", c)
	}
	// 聊天区没有悄悄话和处理过程
	for _, m := range h.a.msgs {
		if strings.Contains(m.text, "帮我查一下") || strings.Contains(m.text, "是个群里的梗") {
			t.Fatalf("悄悄话进了聊天区：%q", m.text)
		}
	}
}

// 切走标签、换会话：订阅结束或重新开；别的标签不读；本地私聊没有这个标签。
func TestSideLogOnlyWhenVisibleAndNotInPrivate(t *testing.T) {
	key := qqGroup("900011")
	h, f := logUI(t, key, []string{"char00", "char01"})
	if f.watching != 0 {
		t.Fatalf("没切到日志不该订阅：%d", f.watching)
	}
	h.click(t, "对话日志")
	h.until(t, "订阅", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.watching == 1 })
	h.a.sel = "console:char00"
	h.a.keys[h.a.sel] = h.a.keyOf("console:char00")
	h.tt.Frame()
	if h.tt.HasText("对话日志") {
		t.Fatalf("本地私聊没有对话日志：%v", h.tt.Texts())
	}
}

// 空：还没有悄悄话时写一句怎么用。
func TestSideLogEmpty(t *testing.T) {
	key := qqGroup("900012")
	h, f := logUI(t, key, []string{"char00"})
	f.page = ctlproto.LogPage{Entries: []ctlproto.LogEntry{}}
	h.click(t, "对话日志")
	h.until(t, "空态", func() bool {
		return h.tt.HasText("还没有悄悄话。在消息上点右键选“引用”，指给某位角色，她怎么处理的会记在这里。")
	})
}

// 载入更早的：游标原样交回去，旧批并进来，不覆盖新批。
func TestSideLogOlderPageMerges(t *testing.T) {
	key := qqGroup("900013")
	h, f := logUI(t, key, []string{"char00", "char01"})
	f.page.Next = "CURSOR1"
	f.older = ctlproto.LogPage{Entries: []ctlproto.LogEntry{
		{ID: "say:OLD", Batch: "OLD", Kind: ctlproto.LogWhisper, At: 1000, Targets: []string{"char00"}, Summary: "更早的那句话"},
		{ID: "arr:OLD:char00", Batch: "OLD", Kind: ctlproto.LogArrange, Character: "char00", At: 1001, State: ctlproto.WhisperDone},
	}}
	h.click(t, "对话日志")
	h.until(t, "日志出现", func() bool { return h.tt.HasText("载入更早的") })
	h.click(t, "载入更早的")
	h.until(t, "旧批出现", func() bool { return h.tt.HasText("更早的那句话") })
	if read, _, _ := f.seen(); !slices.Contains(read, "page:CURSOR1") {
		t.Fatalf("该带着游标取更早的：%v", read)
	}
	if !h.tt.HasText("帮我查一下这个梗") || h.tt.HasText("载入更早的") {
		t.Fatalf("新批还在、取尽后不再有“载入更早的”：%v", h.tt.Texts())
	}
	// 新批在上、旧批在下
	a, _ := h.tt.Find("帮我查一下这个梗")
	b, _ := h.tt.Find("更早的那句话")
	if a.Y >= b.Y {
		t.Fatalf("新批该在旧批上面：%v %v", a, b)
	}
}

// 合并：最新一页掉出去的批不丢；同一批的新状态覆盖旧的。
func TestSideLogMerge(t *testing.T) {
	t.Parallel()
	var st sideLogState
	st.merge(ctlproto.LogPage{Next: "C1", Entries: []ctlproto.LogEntry{
		{ID: "a1", Batch: "A", At: 10, Kind: ctlproto.LogArrange, State: "processing"}, {ID: "b1", Batch: "B", At: 20, Kind: ctlproto.LogArrange, State: "queued"}}}, true)
	st.merge(ctlproto.LogPage{Next: "C0", Entries: []ctlproto.LogEntry{{ID: "z1", Batch: "Z", At: 1, Kind: ctlproto.LogArrange, State: "done"}}}, false)
	// 新的最新一页只含 B、C：A 掉出去了，要还在；B 的状态是新的；next 不被头一页盖掉
	st.merge(ctlproto.LogPage{Next: "C2", Entries: []ctlproto.LogEntry{
		{ID: "b1", Batch: "B", At: 20, Kind: ctlproto.LogArrange, State: "done"}, {ID: "c1", Batch: "C", At: 30, Kind: ctlproto.LogArrange, State: "queued"}}}, true)
	if got := st.order(); !slices.Equal(got, []string{"C", "B", "A", "Z"}) {
		t.Fatalf("批的顺序（新到旧）：%v", got)
	}
	if st.batches["B"][0].State != "done" || st.next != "C0" {
		t.Fatalf("新状态该覆盖旧的、next 不被头一页盖掉：%+v next=%q", st.batches["B"], st.next)
	}
}

// 工具条目上的外发编号：和审批卡同一个号；有可疑的写几处，条目整行用危险色（规格-群聊引用和路由 8.2）
func TestLogEntryLineShowsEgressNumber(t *testing.T) {
	t.Parallel()
	plain := logEntryLine(ctlproto.LogEntry{Kind: ctlproto.LogTool, Summary: "powershell"})
	if strings.Contains(plain, "外发") {
		t.Fatalf("没有外发检查的工具条目不写外发：%q", plain)
	}
	calm := logEntryLine(ctlproto.LogEntry{Kind: ctlproto.LogTool, Summary: "powershell", Egress: "E-3FA2C1", EgressN: 2})
	if !strings.Contains(calm, "外发 E-3FA2C1") || strings.Contains(calm, "可疑") {
		t.Fatalf("有地址没可疑：%q", calm)
	}
	bad := logEntryLine(ctlproto.LogEntry{Kind: ctlproto.LogTool, Summary: "powershell", Egress: "E-3FA2C1", EgressN: 2, EgressDanger: 1})
	if !strings.Contains(bad, "外发 E-3FA2C1（1 处可疑）") {
		t.Fatalf("有可疑要写几处：%q", bad)
	}
}
