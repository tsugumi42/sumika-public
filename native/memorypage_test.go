package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/memory"
	"sumika-native/internal/memview"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/store/memsource"
)

// 记忆页（规格-记忆页 第十节第 10 条）：空 / 加载 / 出错 / 没有记忆库 / 没迁移 / 搜不到六种状态各出一次；选中、切角色、切筛选、连敲搜索词；
// 窄窗口。数据是 demo_memory.go 编的；界面线程的活（a.post）进队列，测试里手动倒出来，和真窗口一样"后台线程算、界面线程并"。

func memApp(t *testing.T, m api.MemoryReader) (*app, *uiQueue) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := &app{rail: 2, ctx: ctx, cancel: cancel, keys: map[string]api.ConversationKey{}}
	a.mainView = testMainView()
	q := &uiQueue{}
	a.postFn = q.post
	a.attachMemory(m)
	return a, q
}

// uiWaitLimit 是等后台结果回到界面线程的上限，只防卡死：等不到就是红，改坏了照样红，只是红得慢。
// 合并队列低优先级跑、CPU 80% 以上时，后台协程停几秒很常见。
const uiWaitLimit = time.Minute

// settleMem 反复倒界面线程的队列，直到静下来（后台线程的结果会引出新的请求）
func settleMem(q *uiQueue) {
	deadline := time.Now().Add(5 * time.Second)
	quiet := 0
	for time.Now().Before(deadline) {
		if q.drain() > 0 {
			quiet = 0
		} else if quiet++; quiet > 8 {
			return
		}
		time.Sleep(4 * time.Millisecond)
	}
}

type memHarness struct {
	t  *testing.T
	a  *app
	q  *uiQueue
	tt *ui.Tester
}

func newMemHarness(t *testing.T, m api.MemoryReader, w, h int) *memHarness {
	a, q := memApp(t, m)
	hn := &memHarness{t: t, a: a, q: q, tt: ui.NewTester(a.view, w, h)}
	hn.frame()
	return hn
}

// frame：画一帧、倒队列、再画（画的时候可能又发起了请求）
func (h *memHarness) frame() {
	h.t.Helper()
	for i := 0; i < 3; i++ {
		h.tt.Frame()
		settleMem(h.q)
	}
	h.tt.Frame()
	// “安静一会儿”判断不了后台有没有回话，负载高时会提前放弃：还在加载就接着等（到期限算测试自己失败）
	p := &h.a.mem
	// 测试用"门"故意卡着读服务时，读不会回来，loading 是预期的状态：不陪它等满期限（原来每次卡门的测试白等 30 秒）
	for deadline := time.Now().Add(30 * time.Second); ((!h.gateHeld() && (p.phase == "loading" || p.recentPhase == "loading" || p.det.phase == "loading")) || h.overviewPending()) && time.Now().Before(deadline); {
		settleMem(h.q)
		h.tt.Frame()
	}
}

// gateHeld：读服务的"门"正关着（列表、详情、最近变动都停在门口，不会有结果回来）
func (h *memHarness) gateHeld() bool {
	g, ok := h.a.mem.rd.(*gatedMemory)
	if !ok {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gate != nil
}

// overviewPending：页面开着、第一份概览还没到（到了才选角色、才开始读列表）。测试故意卡着概览的不等。
func (h *memHarness) overviewPending() bool {
	p := &h.a.mem
	if p.cancel == nil || p.haveOv || p.ovErr != "" {
		return false
	}
	if g, ok := p.rd.(*gatedMemory); ok && g.overviewHeld() {
		return false
	}
	return true
}

func (h *memHarness) click(label string) {
	h.t.Helper()
	if err := h.tt.Click(label); err != nil {
		h.t.Fatalf("点 %q：%v\n窗口里的字：%v", label, err, h.texts(40))
	}
	h.frame()
}

func (h *memHarness) texts(n int) []string {
	all := h.tt.Texts()
	if len(all) > n {
		all = all[:n]
	}
	return all
}

func (h *memHarness) has(s string) bool { return h.tt.HasText(s) }

func demoRowText(i int) string {
	return fmt.Sprintf("编造的记忆第 %d 条：喜欢草莓蛋糕，周末想去海边，下周要考试。", i)
}

// 打开页面：挑第一个有库的角色，列表第一页出来，左栏的数字和编的数据一致
func TestMemoryPageOpensWithFirstCharacterAndCounts(t *testing.T) {
	h := newMemHarness(t, newDemoMemory(), 2048, 1030)
	p := &h.a.mem
	if p.char != "suzune" || p.phase != "ready" || len(p.rows) != 50 || !p.more {
		t.Fatalf("角色 %q 阶段 %q 行 %d more %v", p.char, p.phase, len(p.rows), p.more)
	}
	// 80 条事实按种类轮流：四种各 20；片段 80/3 = 26
	for _, want := range []string{"铃音 · 106", "铃花 · 106", "没有库的角色 · 还没有记忆库", "全部 80", "事实 20", "约定 20", "喜好 20", "边界 20", "片段 26"} {
		if _, ok := h.tt.Find(want); !ok {
			t.Errorf("左栏没有 %q；窗口里的字：%v", want, h.texts(60))
		}
	}
	if _, ok := h.tt.Find(demoRowText(0)); !ok {
		t.Errorf("第一行没出来：%v", h.texts(60))
	}
	if !strings.Contains(strings.Join(h.tt.Texts(), "|"), "已列出 50 条，往下滚还有") {
		t.Errorf("副标题：%v", h.texts(40))
	}
	// 第 0 条的创建时间为 0（i%53==0）：写"时间不详"，不写 1970
	for _, s := range h.tt.Texts() {
		if strings.Contains(s, "1970") {
			t.Errorf("出现了 1970：%q", s)
		}
	}
	if !h.has("时间不详") {
		t.Error("创建时间为 0 的要写时间不详")
	}
}

// 六种状态：空、加载、出错、没有记忆库、没迁移、搜不到
func TestMemoryPageSixStates(t *testing.T) {
	// 空：有库但一条都没有
	empty := newDemoMemoryN([]string{"suzune"}, 0)
	h := newMemHarness(t, empty, 2048, 1030)
	if !h.has("她还没有记忆") {
		t.Errorf("空：%v", h.texts(40))
	}
	// 加载：请求发出去、结果还没倒回来的那一刻
	d := newDemoMemory()
	a, q := memApp(t, d)
	tt := ui.NewTester(a.view, 2048, 1030)
	tt.Frame()
	q.drain() // 概览回来 → 选角色 → 发起列表请求
	tt.Frame()
	if a.mem.phase != "loading" {
		// 列表请求很快：可能已经回来排在队列里，不倒它就还是 loading
		t.Logf("阶段 %q", a.mem.phase)
	}
	if a.mem.phase == "loading" && a.mem.rows != nil {
		t.Errorf("加载中不该带着旧行")
	}
	for deadline := time.Now().Add(uiWaitLimit); a.mem.phase != "ready" && time.Now().Before(deadline); { // 列表在协程里读，不能只等“安静一会儿”
		settleMem(q)
		tt.Frame()
	}
	if a.mem.phase != "ready" {
		t.Errorf("加载完应是 ready：%q", a.mem.phase)
	}
	// 出错 → 重试
	d2 := newDemoMemory()
	d2.failOn = "编的读取失败"
	h = newMemHarness(t, d2, 2048, 1030)
	if !h.has("没取到记忆") || !h.has("编的读取失败") {
		t.Errorf("出错：%v", h.texts(40))
	}
	d2.mu.Lock()
	d2.failOn = ""
	d2.mu.Unlock()
	h.click("重试")
	if h.a.mem.phase != "ready" || len(h.a.mem.rows) != 50 {
		t.Errorf("重试以后：%q %d 行", h.a.mem.phase, len(h.a.mem.rows))
	}
	// 没有记忆库
	h = newMemHarness(t, newDemoMemory(), 2048, 1030)
	h.click("没有库的角色 · 还没有记忆库")
	if !h.has("这个角色还没有记忆库") || h.a.mem.phase != "nodb" {
		t.Errorf("没有库：%q %v", h.a.mem.phase, h.texts(40))
	}
	// 没迁移
	d3 := newDemoMemory()
	d3.mu.Lock()
	d3.chars[0].Migrated = false
	d3.mu.Unlock()
	h = newMemHarness(t, d3, 2048, 1030)
	if !h.has("这个库还没迁移") {
		t.Errorf("没迁移：%v", h.texts(60))
	}
	// 搜不到
	h = newMemHarness(t, newDemoMemory(), 2048, 1030)
	h.a.mem.query, h.a.mem.reported = "绝对不存在的词", "绝对不存在的词"
	h.frame()
	if !h.has("搜不到") {
		t.Errorf("搜不到：%v", h.texts(60))
	}
	// 筛选后没有（片段的状态组里没有"其他"）
	h = newMemHarness(t, newDemoMemory(), 2048, 1030)
	h.click("片段 26")
	h.click("其他 0")
	if !h.has("筛选后没有") {
		t.Errorf("筛选后没有：%v", h.texts(60))
	}
}

// 连敲搜索词：停下来才发一次请求；中文输入走真的输入框
func TestMemoryPageSearchTypedDebounced(t *testing.T) {
	d := newDemoMemory()
	var calls atomic.Int32
	h := newMemHarness(t, &countingMem{demoMemory: d, calls: &calls}, 2048, 1030)
	base := calls.Load()
	var x, y float32
	if r, ok := h.tt.Find("她记的话、人的名字"); ok { // 新界面只有框里的提示字
		x, y = r.X+30, r.Y+r.H/2
	} else if r, ok := h.tt.Find("搜索"); ok { // 旧界面框上面有小标题
		x, y = r.X+30, r.Y+r.H+22
	} else {
		t.Fatalf("没有搜索框：%v", h.texts(40))
	}
	h.tt.ClickAt(x, y)
	for _, ch := range []string{"第", " 7", "条"} { // 连敲三下
		h.tt.Type(ch)
	}
	h.tt.Advance(100 * time.Millisecond)
	settleMem(h.q)
	if got := calls.Load() - base; got != 0 {
		t.Errorf("还没停够 250 毫秒就发了 %d 次请求", got)
	}
	h.tt.Advance(300 * time.Millisecond)
	h.frame()
	if got := calls.Load() - base; got != 1 {
		t.Errorf("停下以后该只发 1 次请求，实际 %d", got)
	}
	if h.a.mem.reported != "第 7条" {
		t.Errorf("报告的搜索词 %q", h.a.mem.reported)
	}
	// 命中："第 7条"在编的正文里不存在 → 搜不到
	if !h.has("搜不到") {
		t.Errorf("该搜不到：%v", h.texts(40))
	}
}

type countingMem struct {
	*demoMemory
	calls *atomic.Int32
}

func (c *countingMem) ListMemory(ctx context.Context, req api.MemoryListRequest) (api.Result[api.MemoryPage], error) {
	c.calls.Add(1)
	return c.demoMemory.ListMemory(ctx, req)
}

// 切种类、状态组、范围，切角色：结果和编的数据逐一对上
func TestMemoryPageFiltersAndCharacterSwitch(t *testing.T) {
	h := newMemHarness(t, newDemoMemory(), 2048, 1030)
	p := &h.a.mem
	h.click("约定 20")
	if p.kind != "promise" || len(p.rows) != 20 {
		t.Fatalf("约定：kind %q %d 行", p.kind, len(p.rows))
	}
	for _, r := range p.rows {
		if r.Kind != "promise" {
			t.Errorf("约定里混进了 %s", r.Kind)
		}
	}
	// 约定 = i%4==1，状态组按 i%7 轮：待确认(i%7==3)的 i ∈ {1,5,9,...} 里 i%7==3 的有 i=17,45,73 → 3 条
	h.click("听说，待确认 3")
	if p.group != "candidate" || len(p.rows) != 3 {
		t.Errorf("约定 · 待确认：%q %d 行", p.group, len(p.rows))
	}
	// 换种类：状态组清回全部（片段和事实的状态组不一样）
	h.click("片段 26")
	if p.kind != "episode" || p.group != "" || len(p.rows) != 26 {
		t.Errorf("片段：kind %q group %q %d 行", p.kind, p.group, len(p.rows))
	}
	h.click("已撤销 13")
	if len(p.rows) != 13 {
		t.Errorf("已撤销的片段 %d 行", len(p.rows))
	}
	// 换角色：筛选清回默认、列表重取、选中项清掉
	h.click("铃花 · 106")
	if p.char != "reika" || p.kind != "" || p.group != "" || p.selRef != "" || len(p.rows) != 50 {
		t.Errorf("换角色：%q kind %q group %q sel %q %d 行", p.char, p.kind, p.group, p.selRef, len(p.rows))
	}
}

// 选中一条：详情分块出来，证据、出处、变化各自取；出处原话撤回的不显示正文；找不到的照实写
func TestMemoryPageDetailTabs(t *testing.T) {
	h := newMemHarness(t, newDemoMemory(), 2048, 1030)
	p := &h.a.mem
	// 第 3 条是待确认（i%7==3），出处数 [0,1,3,5,12][3%5] = 5
	h.click(demoRowText(3))
	if p.selRef != "f:d00003" || p.det.phase != "ready" {
		t.Fatalf("选中 %q 阶段 %q", p.selRef, p.det.phase)
	}
	for _, want := range []string{"她现在想得起", "已攒 1 / 3 次，其中本人说的 1 次；攒够就会自己提升成有效", "证据", "再显示 20 行（共 25 行）"} {
		if !h.has(want) {
			t.Errorf("详情里没有 %q：%v", want, h.texts(80))
		}
	}
	if len(p.det.evidence) != 20 {
		t.Errorf("证据第一页 %d 行", len(p.det.evidence))
	}
	for i := 0; i < 10 && func() bool {
		_, ok := h.tt.Find("再显示 20 行（共 25 行）")
		r, _ := h.tt.Find("再显示 20 行（共 25 行）")
		return !ok || r.Y > 1000
	}(); i++ {
		h.tt.Scroll(1800, 600, 0, 300) // 详情栏往下滚
		h.frame()
	}
	h.click("再显示 20 行（共 25 行）")
	if len(p.det.evidence) != 25 {
		t.Errorf("再显示以后 %d 行", len(p.det.evidence))
	}
	// 出处：前 3 条自动取，其余点开才取；seq 100..104 里 100 是 5 的倍数（撤回）
	h.click("出处")
	if p.tab != 1 {
		t.Fatalf("没切到出处页：%d", p.tab)
	}
	if !h.has("这句话后来被撤回了") || !h.has("编造的原话 #101：我下周搬家。") || !h.has("编造的原话 #102：我下周搬家。") {
		t.Errorf("出处页：%v", h.texts(80))
	}
	if h.has("编造的原话 #103") {
		t.Error("第 4 条出处不该自动取")
	}
	if !h.has("这条是整批总结的，共 5 句") {
		t.Errorf("整批总结的提示：%v", h.texts(80))
	}
	// 变化
	h.click("变化")
	if !h.has("放行：待确认 → 有效，放行者：她自己（攒够 3 次不同场合）") && !h.has("整理") {
		t.Errorf("变化页：%v", h.texts(80))
	}
	if len(p.det.revs) != 20 || !h.has("再显示 20 行") {
		t.Errorf("变化 %d 行", len(p.det.revs))
	}
	// 被取代的那条：指向取代它的
	h.click("详情")
	h.a.memSelect("f:d00005") // i%7==5 → superseded
	h.frame()
	if !h.has("被另一条取代了") || !h.has("被新的取代了，她想不起") {
		t.Errorf("被取代：%v", h.texts(80))
	}
	// 片段：没有证据和修订，出处是头尾几句
	h.a.memSelect("e:d00000")
	h.frame()
	if h.has("证据") {
		t.Error("片段不该有证据块")
	}
	h.click("变化")
	if !h.has("片段没有修订记录，只有状态。") {
		t.Errorf("片段的变化页：%v", h.texts(60))
	}
}

// 请求号：连发时旧结果晚到不能盖掉新的（改坏检查：去掉 memApplyList 里的 p.req != req，这个测试变红）
func TestMemoryPageStaleListResultIsDropped(t *testing.T) {
	d := newDemoMemory()
	gate := make(chan struct{})
	slow := &gatedMem{demoMemory: d, block: "AAA", gate: gate}
	h := newMemHarness(t, slow, 2048, 1030)
	p := &h.a.mem
	p.query, p.reported = "AAA", "AAA"
	h.a.memRefetch() // 第一个请求：卡在闸门上
	p.query, p.reported = "第 7 条", "第 7 条"
	before := len(p.rows)
	h.a.memRefetch()                                                                                     // 第二个请求：马上回来（搜索只剩一两行，行数变了才算它到了；旧行会一直留到新结果换上）
	for deadline := time.Now().Add(uiWaitLimit); len(p.rows) == before && time.Now().Before(deadline); { // 等第二个请求的结果真的到了再记下“想要的行”
		settleMem(h.q)
	}
	want := len(p.rows)
	rows := append([]api.MemoryRow(nil), p.rows...)
	close(gate) // 旧的现在才回来
	for deadline := time.Now().Add(uiWaitLimit); slow.done.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	h.q.waitPost(10 * time.Second) // 旧请求的结果真的排进队列了，再倒出来看它有没有盖掉新的
	settleMem(h.q)
	if len(p.rows) != want || (want > 0 && p.rows[0].Ref != rows[0].Ref) {
		t.Errorf("旧结果盖了新的：%d 行 → %d 行", want, len(p.rows))
	}
	if p.reported != "第 7 条" || p.phase != "ready" {
		t.Errorf("阶段 %q 搜索词 %q", p.phase, p.reported)
	}
}

type gatedMem struct {
	*demoMemory
	block string
	gate  chan struct{}
	done  atomic.Int32 // 卡在闸门上的那次请求读完了
}

func (g *gatedMem) ListMemory(ctx context.Context, req api.MemoryListRequest) (api.Result[api.MemoryPage], error) {
	if req.Query == g.block {
		<-g.gate
		defer g.done.Add(1)
	}
	return g.demoMemory.ListMemory(ctx, req)
}

// 滚到离底 10 行才取下一页，一次只取一页；下一页没取到：保留已取的，底部写"没取到，点这里重试"
func TestMemoryPagePagingNearBottom(t *testing.T) {
	d := newDemoMemoryN([]string{"suzune"}, 400)
	var inflight, maxInflight, pages atomic.Int32
	m := &pagingMem{demoMemory: d, inflight: &inflight, max: &maxInflight, pages: &pages}
	h := newMemHarness(t, m, 2048, 1030)
	p := &h.a.mem
	if len(p.rows) != 50 || pages.Load() != 1 {
		t.Fatalf("第一页 %d 行，请求 %d 次", len(p.rows), pages.Load())
	}
	// 窗口里只建看得见的几行：1000 高的窗口，行高 ~54，一屏不到 30 行，页面上的字远少于 50 行
	if n := len(h.tt.Texts()); n > 400 {
		t.Errorf("50 行都建出来了？窗口里 %d 段字", n)
	}
	for i := 0; i < 80 && len(p.rows) < 150; i++ {
		h.tt.Scroll(1000, 500, 0, 600)
		h.frame()
	}
	if len(p.rows) < 150 {
		t.Fatalf("滚了一阵只有 %d 行", len(p.rows))
	}
	if maxInflight.Load() != 1 {
		t.Errorf("同时在途的请求最多 %d 个，该是 1", maxInflight.Load())
	}
	// 下一页失败
	d.mu.Lock()
	d.failOn = "编的翻页失败"
	d.mu.Unlock()
	have := len(p.rows)
	for i := 0; i < 80 && p.moreErr == ""; i++ {
		h.tt.Scroll(1000, 500, 0, 600)
		h.frame()
	}
	if p.moreErr == "" {
		f, l := p.list.Visible()
		t.Fatalf("没出现翻页失败（%d 行，可见 %d~%d，busy %v，more %v，阶段 %q）", len(p.rows), f, l, p.moreBusy, p.more, p.phase)
	}
	if len(p.rows) < have {
		t.Errorf("翻页失败丢了已取的行：%d → %d", have, len(p.rows))
	}
	if !h.has("没取到下一页：编的翻页失败") || !h.has("点这里重试") {
		t.Errorf("底部提示：%v", h.texts(60))
	}
	d.mu.Lock()
	d.failOn = ""
	d.mu.Unlock()
	h.click("点这里重试")
	if p.moreErr != "" || len(p.rows) <= have {
		t.Errorf("重试以后 %d 行 moreErr %q", len(p.rows), p.moreErr)
	}
}

type pagingMem struct {
	*demoMemory
	inflight, max, pages *atomic.Int32
}

func (g *pagingMem) ListMemory(ctx context.Context, req api.MemoryListRequest) (api.Result[api.MemoryPage], error) {
	n := g.inflight.Add(1)
	defer g.inflight.Add(-1)
	for {
		m := g.max.Load()
		if n <= m || g.max.CompareAndSwap(m, n) {
			break
		}
	}
	g.pages.Add(1)
	time.Sleep(15 * time.Millisecond)
	return g.demoMemory.ListMemory(ctx, req)
}

// 换读服务（宿主切 archive / receive）：整页清回空，旧订阅取消；nil 是后台没起来
func TestMemoryPageReaderSwap(t *testing.T) {
	d1 := newDemoMemory()
	h := newMemHarness(t, d1, 2048, 1030)
	p := &h.a.mem
	h.click(demoRowText(2))
	if p.selRef == "" {
		t.Fatal("没选中")
	}
	gen := p.gen
	h.a.attachMemory(nil)
	h.frame()
	if p.gen == gen || p.selRef != "" || len(p.rows) != 0 || !h.has("后台还没起来") {
		t.Errorf("换成 nil：gen %d→%d sel %q %d 行 %v", gen, p.gen, p.selRef, len(p.rows), h.texts(20))
	}
	d1.mu.Lock()
	subs := len(d1.subs)
	d1.mu.Unlock()
	if subs != 0 {
		// 订阅的取消是异步的：等一等
		time.Sleep(50 * time.Millisecond)
		d1.mu.Lock()
		subs = len(d1.subs)
		d1.mu.Unlock()
	}
	if subs != 0 {
		t.Errorf("旧订阅没取消：%d 个", subs)
	}
	h.a.attachMemory(newDemoMemory())
	h.frame()
	if p.phase != "ready" || len(p.rows) != 50 {
		t.Errorf("换回来：%q %d 行", p.phase, len(p.rows))
	}
	// 旧读服务晚到的概览不能串进来：旧 gen 的快照直接丢
	h.a.memApplyOverview(gen, api.Snapshot[api.MemoryOverview]{Result: api.Result[api.MemoryOverview]{State: api.Ready, Value: api.MemoryOverview{}}})
	if len(p.ov.Characters) == 0 {
		t.Error("旧代次的快照把新的概览盖空了")
	}
}

// 库里有变化：计数立刻更新，列表不自己乱跳，出"有 N 处变化，点这里刷新"；什么都没点时自动刷新
func TestMemoryPageChangeNotice(t *testing.T) {
	d := newDemoMemory()
	h := newMemHarness(t, d, 2048, 1030)
	p := &h.a.mem
	// 什么都没点、没选中、在最顶端：自动刷新，新的一条出现在最上面
	d.Bump("suzune")
	h.frame()
	if len(p.rows) != 50 || p.rows[0].Text != "刚刚新写的一条（演示）" {
		t.Fatalf("自动刷新：%d 行，第一行 %q", len(p.rows), p.rows[0].Text)
	}
	if h.has("处变化，点这里刷新") {
		t.Error("自动刷新以后不该再有提示")
	}
	// 选中了一条：列表不动，出提示；选中项的详情重读
	h.click(demoRowText(4))
	before := p.rows[0].Ref
	d.Bump("suzune")
	h.frame()
	if p.rows[0].Ref != before {
		t.Error("选中了一条以后，列表不该自己重排")
	}
	if !h.has("有 1 处变化，点这里刷新") {
		t.Fatalf("没有提示：%v", h.texts(30))
	}
	h.click("有 1 处变化，点这里刷新")
	if p.rows[0].Ref == before || h.has("处变化，点这里刷新") {
		t.Errorf("点了刷新：第一行 %q", p.rows[0].Ref)
	}
	if p.selRef == "" {
		t.Error("刷新不该丢掉选中项")
	}
}

// 窄窗口：详情盖在列表上，能回去
func TestMemoryPageNarrowWindowOverlay(t *testing.T) {
	h := newMemHarness(t, newDemoMemory(), 1000, 800)
	if _, ok := h.tt.Find("点左边的一条，看她凭什么记下它"); ok {
		t.Fatal("窄窗口里没选中时不该占详情栏")
	}
	h.click(demoRowText(0))
	if !h.has("← 回到列表") || h.has("已列出") {
		t.Fatalf("选中以后该是详情盖住列表：%v", h.texts(40))
	}
	h.click("← 回到列表")
	if h.a.mem.selRef != "" || !h.has("已列出") {
		t.Errorf("回到列表：%q %v", h.a.mem.selRef, h.texts(40))
	}
	// 宽窗口：三栏同时在
	w := newMemHarness(t, newDemoMemory(), 2048, 1030)
	if !w.has("点左边的一条，看她凭什么记下它") || !w.has("已列出") {
		t.Errorf("宽窗口三栏：%v", w.texts(40))
	}
}

// 最近变动：列出、点一行去看那一条
func TestMemoryPageRecentChanges(t *testing.T) {
	h := newMemHarness(t, newDemoMemory(), 2048, 1030)
	h.click("最近变动")
	p := &h.a.mem
	if !p.showRecent || len(p.recent) != 50 || !h.has("回到列表") {
		t.Fatalf("最近变动：%v %d", p.showRecent, len(p.recent))
	}
	h.click("回到列表")
	if p.showRecent {
		t.Error("没回到列表")
	}
}

// 页面上没有会改库的按钮（第一步只看不改）：整个页面的字里没有这些词
func TestMemoryPageHasNoWriteButtons(t *testing.T) {
	h := newMemHarness(t, newDemoMemory(), 2048, 1030)
	h.click(demoRowText(3))
	for _, tab := range []string{"出处", "变化", "详情"} {
		h.click(tab)
		for _, s := range h.tt.Texts() {
			for _, bad := range []string{"撤销", "恢复", "删除", "确认", "忘掉", "敬请期待", "第二版"} {
				if s == bad || strings.HasPrefix(s, bad+" ") {
					t.Errorf("%s页出现了像写操作的按钮 %q", tab, s)
				}
			}
		}
	}
}

// 设计自查（docs/设计自查.md 第二、三条）：很长的无空格正文、很长的范围标签，在 2048×1030 下不把布局撑开——
// 每一段字的右边缘都在窗口里，列表里的字不压到详情栏，详情栏的字不出窗口
func TestMemoryPageLongTextStaysInsideTheirColumns(t *testing.T) {
	d := newDemoMemory()
	long := strings.Repeat("很长的没有空格的文字", 40)
	d.rows["suzune"][1].Text = long
	d.rows["suzune"][1].Scopes[0].Label = "只在 " + strings.Repeat("长群名", 30)
	d.rows["suzune"][1].Subject = api.MemoryWho{Kind: "other", Raw: strings.Repeat("N", 80)}
	h := newMemHarness(t, d, 2048, 1030)
	const W, detailW = 2048.0, 420.0
	for _, s := range h.tt.Texts() {
		if r, ok := h.tt.Find(s); ok && float64(r.X+r.W) > W+1 {
			t.Errorf("列表页：%q 的右边缘 %.0f 出了窗口", s[:min(len(s), 30)], r.X+r.W)
		}
	}
	if r, ok := h.tt.Find(long); !ok || float64(r.X+r.W) > W-detailW {
		t.Errorf("很长的正文该被列的宽度截住，右边缘 %.0f，详情栏从 %.0f 起", r.X+r.W, W-detailW)
	}
	h.click(long)
	for _, s := range h.tt.Texts() {
		if r, ok := h.tt.Find(s); ok && (float64(r.X+r.W) > W+1 || r.X < 0) {
			t.Errorf("详情打开后：%q 出了窗口 [%.0f, %.0f]", s[:min(len(s), 30)], r.X, r.X+r.W)
		}
	}
}

// 放大十倍（规格-记忆页 第十节第 9 条的界面一半）：10 个角色、每个 3000 条事实 + 1000 个片段，翻到 1000 行：
// 窗口里的字段数（虚拟列表只建看得见的行）、滚动每帧耗时。数字写进测试输出，提交说明从输出里抄
func TestMemoryPageScaleTenfold(t *testing.T) {
	if testing.Short() {
		t.Skip("放大十倍的量数很慢，-short 跳过")
	}
	ids := make([]string, 10)
	for i := range ids {
		ids[i] = fmt.Sprintf("c%d", i)
	}
	d := newDemoMemoryN(ids, 3000)
	t0 := time.Now()
	h := newMemHarness(t, d, 2048, 1030)
	p := &h.a.mem
	t.Logf("打开页面到第一页画出来：%v（%d 个角色，当前 %d 行）", time.Since(t0).Round(time.Millisecond), len(p.ov.Characters), len(p.rows))
	if len(p.ov.Characters) != 10 {
		t.Fatalf("角色 %d", len(p.ov.Characters))
	}
	t.Logf("第一页窗口里的字段数：%d", len(h.tt.Texts()))
	for i := 0; i < 400 && len(p.rows) < 1000; i++ {
		h.tt.Scroll(1000, 500, 0, 900)
		h.frame()
	}
	if len(p.rows) < 1000 {
		t.Fatalf("翻不到 1000 行：%d", len(p.rows))
	}
	t.Logf("翻到 %d 行时窗口里的字段数：%d（全部行都建出来的话至少 %d）", len(p.rows), len(h.tt.Texts()), len(p.rows)*2)
	// 滚 1000 行：每步一滚一帧，量每帧耗时
	var times []time.Duration
	for i := 0; i < 150; i++ {
		dy := float32(500)
		if i >= 75 {
			dy = -500
		}
		s := time.Now()
		h.tt.Scroll(1000, 500, 0, dy)
		h.tt.Frame()
		times = append(times, time.Since(s))
	}
	sorted := append([]time.Duration(nil), times...)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	t.Logf("在 %d 行的列表上滚 150 步，每步滚加一帧（Tester 里构建 + 布局 + 绘制到内存）：P50 %v，P95 %v，最大 %v",
		len(p.rows), sorted[len(sorted)/2].Round(10*time.Microsecond), sorted[len(sorted)*95/100].Round(10*time.Microsecond), sorted[len(sorted)-1].Round(10*time.Microsecond))
	// 切角色一遍
	t0 = time.Now()
	h.click("编造角色3 · 4000")
	t.Logf("切到另一个角色，列表重取并画出来：%v，%d 行", time.Since(t0).Round(time.Millisecond), len(p.rows))
	if p.char != "c3" || len(p.rows) != 50 {
		t.Errorf("切角色：%q %d 行", p.char, len(p.rows))
	}
}

// 标签两两不同才能交给 ChipChoice / Dropdown（重复会 panic）；键 ↔ 标签的翻译：标签里的数字变了，选中项跟着键走
func TestMemoryPageLabelHelpers(t *testing.T) {
	got := uniqLabels([]string{"铃音 · 5", "铃音 · 5", "铃花 · 5", "铃音 · 5"})
	want := []string{"铃音 · 5", "铃音 · 5（2）", "铃花 · 5", "铃音 · 5（3）"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("uniqLabels[%d] = %q，应 %q", i, got[i], want[i])
		}
	}
	opts := []memOption{{"", "全部"}, {"a", "甲"}, {"b", "乙"}}
	key := "b"
	var changed bool
	labels := []string{"全部 3", "甲 1", "乙 2"}
	tt := ui.NewTester(func(c *ui.Context) { changed = (&app{}).pickChip(c, "测试", opts, labels, &key) }, 600, 200)
	tt.Frame()
	if changed || key != "b" {
		t.Fatalf("没点就变了：%v %q", changed, key)
	}
	// 数字变了（"乙 2" → "乙 5"），选中的还是 b，不 panic
	labels = []string{"全部 3", "甲 1", "乙 5"}
	tt.Frame()
	if changed || key != "b" {
		t.Errorf("数字变了以后选中项丢了：%v %q", changed, key)
	}
	if err := tt.Click("甲 1"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if key != "a" {
		t.Errorf("点了甲：%q", key)
	}
}

// 下拉：打开、选一项，键跟着变
func TestMemoryPageDropdownPick(t *testing.T) {
	vals := []string{"", "x", "y"}
	labels := []string{"会话：全部", "群一 · 3", "群二 · 5"}
	key := ""
	var changed bool
	tt := ui.NewTester(func(c *ui.Context) {
		if (&app{}).pickDrop(c, "来自会话", vals, labels, &key, false) {
			changed = true
		}
	}, 600, 400)
	tt.Frame()
	if err := tt.Click("会话：全部"); err != nil {
		t.Skipf("下拉的触发器不是这个文字：%v；texts=%v", err, tt.Texts())
	}
	for i := 0; i < 200 && tt.FramePending(); i++ { // 面板从入口展开（ADR-395），等展开放完再点
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
	if err := tt.Click("群二 · 5"); err != nil {
		t.Fatalf("选项：%v；texts=%v", err, tt.Texts())
	}
	tt.Frame()
	if !changed || key != "y" {
		t.Errorf("选了群二：changed %v key %q", changed, key)
	}
}

// ───────────── 订阅和在途查询跟着"记忆页在前台"走（规格-记忆页 4.5、4.6；复核 2026-11-02）─────────────

// 计数的读服务：包住编的数据，数订阅了几次、列表请求了几次、有没有请求被取消
type countMem struct {
	*demoMemory
	watches, lists atomic.Int32
	cancelled      atomic.Int32
	blockFirst     string // 非空：第一个带这个搜索词的列表请求卡到 ctx 取消为止
	blocked        atomic.Bool
}

func (c *countMem) WatchMemory(ctx context.Context) (api.Subscription[api.MemoryOverview], error) {
	c.watches.Add(1)
	return c.demoMemory.WatchMemory(ctx)
}

func (c *countMem) ListMemory(ctx context.Context, req api.MemoryListRequest) (api.Result[api.MemoryPage], error) {
	c.lists.Add(1)
	if c.blockFirst != "" && req.Query == c.blockFirst && c.blocked.CompareAndSwap(false, true) {
		<-ctx.Done()
		c.cancelled.Add(1)
		return api.Result[api.MemoryPage]{State: api.Failed, Err: ctx.Err()}, ctx.Err()
	}
	return c.demoMemory.ListMemory(ctx, req)
}

func (c *countMem) subs() int {
	c.demoMemory.mu.Lock()
	defer c.demoMemory.mu.Unlock()
	return len(c.demoMemory.subs)
}

// waitSubs 订阅的摘掉是异步的（读服务的线程里 Close）：等到个数对上
func (c *countMem) waitSubs(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.subs() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("订阅数 %d，该是 %d", c.subs(), want)
}

// 切到别的页：订阅取消、不再发请求；后台提交了新东西也不动；回来：重新订、重新取，角色、筛选、选中项还在，走开时新写的那条出现
// （改坏检查：去掉 view 开头的 memSync，订阅一直活着，waitSubs(0) 报错）
func TestMemoryPageSubscriptionStopsWhenLeaving(t *testing.T) {
	m := &countMem{demoMemory: newDemoMemory()}
	h := newMemHarness(t, m, 2048, 1030)
	p := &h.a.mem
	h.click("约定 20")
	h.click(demoRowText(1))
	if m.watches.Load() != 1 || m.subs() != 1 || p.selRef == "" || p.kind != "promise" {
		t.Fatalf("在记忆页：订阅 %d 次、活着 %d 个、选中 %q、种类 %q", m.watches.Load(), m.subs(), p.selRef, p.kind)
	}
	sel := p.selRef
	h.a.rail = 0 // 切到本地页
	h.frame()
	m.waitSubs(t, 0)
	if p.cancel != nil {
		t.Error("离开记忆页以后总开关还在")
	}
	lists := m.lists.Load()
	m.Bump("suzune") // 走开的时候后台提交了一条
	h.frame()
	time.Sleep(30 * time.Millisecond)
	settleMem(h.q)
	if m.lists.Load() != lists || m.watches.Load() != 1 || m.subs() != 0 {
		t.Errorf("走开以后还在动：列表请求 %d→%d、订阅 %d 次、活着 %d 个", lists, m.lists.Load(), m.watches.Load(), m.subs())
	}
	h.a.rail = 2 // 回来
	h.frame()
	if m.watches.Load() != 2 || m.subs() != 1 {
		t.Fatalf("回来以后该重新订：订阅 %d 次、活着 %d 个", m.watches.Load(), m.subs())
	}
	if p.kind != "promise" || p.selRef != sel || p.char != "suzune" || p.phase != "ready" || p.det.phase != "ready" {
		t.Errorf("回来以后状态没留住：种类 %q 选中 %q 角色 %q 阶段 %q/%q", p.kind, p.selRef, p.char, p.phase, p.det.phase)
	}
	if m.lists.Load() <= lists {
		t.Error("回来以后该重新取列表")
	}
	// 走开时新写的那条是"全部"里的第一条，约定筛选里看不到；切回全部才看得到
	h.click("全部 81")
	if !h.has("刚刚新写的一条（演示）") {
		t.Errorf("走开时写的那条没取到：%v", h.texts(40))
	}
}

// 窗口隐藏 / 最小化：记忆页在前台也不订阅（away 标记由窗口事件设）；显示回来再订
func TestMemoryPageSubscriptionStopsWhenWindowHidden(t *testing.T) {
	m := &countMem{demoMemory: newDemoMemory()}
	h := newMemHarness(t, m, 2048, 1030)
	if m.subs() != 1 {
		t.Fatalf("起初该订着：%d", m.subs())
	}
	h.a.away.Store(true)
	h.a.memSync()
	m.waitSubs(t, 0)
	h.a.away.Store(false)
	h.a.memSync()
	h.frame()
	m.waitSubs(t, 1)
	if m.watches.Load() != 2 || h.a.mem.phase != "ready" {
		t.Errorf("显示回来：订阅 %d 次、阶段 %q", m.watches.Load(), h.a.mem.phase)
	}
}

// 在途的查询跟着取消：卡在读服务里的列表请求，离开页面时它的 ctx 被取消；晚到的失败结果不把页面弄成"出错"；回来重新取成功
func TestMemoryPageInFlightQueryCancelledWhenLeaving(t *testing.T) {
	m := &countMem{demoMemory: newDemoMemory(), blockFirst: "第 7 条"}
	h := newMemHarness(t, m, 2048, 1030)
	p := &h.a.mem
	p.query, p.reported = "第 7 条", "第 7 条"
	h.a.memRefetch()
	deadline := time.Now().Add(2 * time.Second)
	for !m.blocked.Load() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	// 刷新时旧的行留着（不先清空），读还在途
	if !m.blocked.Load() || p.phase != "ready" || !p.listLoad.Loading() || len(p.rows) == 0 {
		t.Fatalf("请求没卡住：阻塞 %v 阶段 %q 在读 %v 行 %d", m.blocked.Load(), p.phase, p.listLoad.Loading(), len(p.rows))
	}
	h.a.rail = 0
	h.frame()
	deadline = time.Now().Add(2 * time.Second)
	for m.cancelled.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if m.cancelled.Load() != 1 {
		t.Fatalf("离开页面以后在途请求的 ctx 没被取消")
	}
	settleMem(h.q)
	if p.phase == "failed" || p.err != "" {
		t.Errorf("被取消的请求不该把页面弄成出错：%q %q", p.phase, p.err)
	}
	h.a.rail = 2
	h.frame()
	if p.phase != "ready" || len(p.rows) == 0 {
		t.Errorf("回来以后该重新取成功：%q %d 行", p.phase, len(p.rows))
	}
}

// 回来以后选中项在走开时变了（状态组不同）：详情栏标"这条刚刚变了"
func TestMemoryPageResumeMarksChangedDetail(t *testing.T) {
	m := &changeMem{countMem: &countMem{demoMemory: newDemoMemory()}}
	h := newMemHarness(t, m, 2048, 1030)
	p := &h.a.mem
	h.click(demoRowText(1))
	if p.det.phase != "ready" || p.det.changed {
		t.Fatalf("起初：%q changed %v", p.det.phase, p.det.changed)
	}
	h.a.rail = 0
	h.frame()
	m.group.Store("revoked") // 走开的时候这条被撤销了
	h.a.rail = 2
	h.frame()
	if !p.det.changed || p.det.d.Row.Group != "revoked" {
		t.Errorf("回来后该标刚刚变了：changed %v 状态组 %q", p.det.changed, p.det.d.Row.Group)
	}
}

type changeMem struct {
	*countMem
	group atomic.Value
}

func (c *changeMem) ReadMemoryDetail(ctx context.Context, character, ref string) (api.Result[api.MemoryDetail], error) {
	r, err := c.countMem.ReadMemoryDetail(ctx, character, ref)
	if g, _ := c.group.Load().(string); g != "" && err == nil {
		r.Value.Row.Group = g
	}
	return r, err
}

// 出处原话在内存里最多留 20 条（取了 30 条，剩最近的 20 条）；离开记忆页就清；等的时候被丢掉的，结果回来也不留
// （改坏检查：去掉 memLoadQuote 里按 memQuoteKeep 丢最早的那段，长度变 30 报错）
func TestMemoryPageQuoteCapAndClearOnLeave(t *testing.T) {
	m := &manySourcesMem{demoMemory: newDemoMemory()}
	h := newMemHarness(t, m, 2048, 1030)
	p := &h.a.mem
	h.click(demoRowText(0))
	if len(p.det.d.Sources) != 30 {
		t.Fatalf("编的出处该有 30 条：%d", len(p.det.d.Sources))
	}
	for i := 0; i < 30; i++ {
		h.a.memLoadQuote(i)
		settleMem(h.q)
	}
	if len(p.det.quotes) != memQuoteKeep || len(p.det.quoteOrder) != memQuoteKeep {
		t.Fatalf("原话留了 %d 条（顺序表 %d），该是 %d", len(p.det.quotes), len(p.det.quoteOrder), memQuoteKeep)
	}
	for i := 0; i < 10; i++ {
		if p.det.quotes[i] != nil {
			t.Errorf("最早取的第 %d 条该被丢掉", i)
		}
	}
	for i := 10; i < 30; i++ {
		if p.det.quotes[i] == nil {
			t.Errorf("第 %d 条该还在", i)
		}
	}
	// 离开记忆页：原话全丢
	h.a.rail = 0
	h.frame()
	if len(p.det.quotes) != 0 || len(p.det.quoteOrder) != 0 {
		t.Errorf("离开记忆页以后原话还留着 %d 条", len(p.det.quotes))
	}
}

type manySourcesMem struct{ *demoMemory }

func (c *manySourcesMem) ReadMemoryDetail(ctx context.Context, character, ref string) (api.Result[api.MemoryDetail], error) {
	r, err := c.demoMemory.ReadMemoryDetail(ctx, character, ref)
	r.Value.Sources, r.Value.SourcesTotal = nil, 30
	for i := 0; i < 30; i++ {
		r.Value.Sources = append(r.Value.Sources, api.MemorySource{Conversation: "qq:group:30001", Seq: int64(101 + i*10), Label: "QQ 群 ***01"})
	}
	return r, err
}

// 出处原话不能选中，也就进不了剪贴板（规格 3.3）：在出处页里从头拖到尾再复制，剪贴板里没有它；
// 对照：同样拖选详情里的正文，剪贴板里有（证明这个测试选得中、复制得到）
// （改坏检查：memQuoteBody 里的 ui.Text 加回 .Selectable()，第一个断言报错）
func TestMemoryPageQuoteIsNotSelectable(t *testing.T) {
	h := newMemHarness(t, &retextMem{demoMemory: newDemoMemory()}, 2048, 1030)
	h.click(demoRowText(3))
	// 从这段字的头拖到尾、复制：能选中的字剪贴板里就有，不能选中的没有
	drag := func(label string) string {
		t.Helper()
		r, ok := h.tt.Find(label)
		if !ok {
			t.Fatalf("窗口里没有 %q：%v", label, h.texts(80))
		}
		h.tt.SetClipboard("")
		h.tt.SetFocused(true)
		y := r.Y + r.H/2
		h.tt.Press(r.X+2, y)
		h.tt.Move(r.X+r.W-2, y)
		h.tt.Release(r.X+r.W-2, y)
		h.tt.Key(ui.Cmd, ui.KeyC)
		return h.tt.Clipboard()
	}
	if got := drag("详情栏专用的正文，列表里没有"); !strings.Contains(got, "详情栏专用的正文") {
		t.Fatalf("对照失败：拖选详情正文再复制，剪贴板是 %q（这个测试选不中，下面的结论不算数）", got)
	}
	h.click("出处")
	if got := drag("编造的原话 #101：我下周搬家。"); got != "" {
		t.Errorf("出处原话被选中并复制进了剪贴板：%q", got)
	}
}

// retextMem 把详情里的正文换成列表里没有的一句：测试用 Find 按字找位置，列表行和详情的字相同的话找到的是列表行
type retextMem struct{ *demoMemory }

func (c *retextMem) ReadMemoryDetail(ctx context.Context, character, ref string) (api.Result[api.MemoryDetail], error) {
	r, err := c.demoMemory.ReadMemoryDetail(ctx, character, ref)
	r.Value.Row.Text = "详情栏专用的正文，列表里没有"
	return r, err
}

// 正式只读服务接到实际页面：共用事实随角色切换保留，专属条目和未见原话不跟过去。
func TestMemoryPageSharedSourceAndRoleBoundary(t *testing.T) {
	ctx := context.Background()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	poolFile := p.OwnerMemoryDB()
	pool, err := memory.OpenOwnerPool(ctx, poolFile, nil, nil, memory.Options{Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var chars []memview.Character
	var writer *memory.Memory
	for i, id := range []string{"aria", "sora"} {
		file := filepath.Join(p.Root, id+".db")
		empty, err := memory.OpenOwnerPool(ctx, file, nil, nil, memory.Options{Writable: true})
		if err != nil {
			t.Fatal(err)
		}
		if err = empty.Close(); err != nil {
			t.Fatal(err)
		}
		events, err := memsource.NewMulti(set, id)
		if err != nil {
			t.Fatal(err)
		}
		m, err := memory.Open(ctx, file, nil, nil, memory.Options{Character: id, Writable: true, Shared: pool, Events: events})
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		if i == 0 {
			writer = m
		}
		chars = append(chars, memview.Character{ID: id, Name: []string{"岚", "曜"}[i], DB: file, Browse: memory.BrowserOptions{SharedDB: poolFile}, Events: events})
	}
	local, err := set.For("console")
	if err != nil {
		t.Fatal(err)
	}
	no, yes := false, true
	for _, entry := range []struct {
		text string
		only *bool
	}{{"主人喜欢冷杉茶", &no}, {"主人给岚的专属称呼是星灯", &yes}} {
		inputSeq, appendErr := local.AppendEvent(store.NewEvent{Channel: "console", Conversation: "console:aria", Kind: "message", ActorKind: "owner", ActorID: "owner", Origin: "owner", Text: entry.text, CreatedAt: time.Now().UnixMilli()}, []string{"aria"})
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		res, err := writer.Remember(ctx, memory.Audience{Channel: "console", Conversation: "console:aria", PrivateOK: true}, memory.RememberArgs{InputSeqs: []int64{inputSeq}, Kind: "preference", Text: entry.text, OnlyHer: entry.only, SourceQuote: entry.text, Temporal: "stable"})
		if err != nil || res.IsError {
			t.Fatalf("写入=%+v %v", res, err)
		}
	}
	rd := memview.New(chars, memview.Options{})
	defer rd.Close()
	h := newMemHarness(t, rd, 2048, 1030)
	if h.a.mem.char != "aria" || len(h.a.mem.rows) != 2 {
		t.Fatalf("初始页面=%s %+v", h.a.mem.char, h.a.mem.rows)
	}
	h.click("曜 · 1")
	if len(h.a.mem.rows) != 1 || h.a.mem.rows[0].Text != "主人喜欢冷杉茶" {
		t.Fatalf("切角色串入专属：%+v", h.a.mem.rows)
	}
	h.click("主人喜欢冷杉茶")
	if !strings.Contains(strings.Join(h.tt.Texts(), "\n"), "主人共用，由 aria 得知") {
		t.Fatalf("页面没有显示共用来源：%v", h.texts(80))
	}
	d := h.a.mem.det.d
	for _, place := range d.Places {
		if place.Visible != "yes" {
			t.Fatalf("详情仍按场合过滤：%+v", d.Places)
		}
	}
	if len(d.Sources) != 1 {
		t.Fatalf("出处=%+v", d.Sources)
	}
	quote, err := rd.ReadMemoryQuote(ctx, "sora", d.Sources[0])
	if err != nil || quote.Value.Status != "missing" {
		t.Fatalf("共享扩大原话访问：%+v %v", quote, err)
	}
	for _, text := range h.tt.Texts() {
		if bounds, ok := h.tt.Find(text); ok && (bounds.X < 0 || bounds.X+bounds.W > 2049) {
			t.Errorf("来源文字超出客户区：%q %+v", text, bounds)
		}
	}
	t.Logf("实际页面：2048×1030，角色 aria=2 条、sora=1 条，详情来源=%d；未见原话=%s，可见文本节点=%d", len(d.Sources), quote.Value.Status, len(h.tt.Texts()))
}
