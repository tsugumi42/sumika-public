package main

import (
	"context"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/loadstate"
	"sumika-native/internal/loadui"
)

// 记忆页的加载占位（loadstate 的规则）：快（阈值内）全程没有骨架、慢才出骨架且留够时间、刷新时旧行一直在。
// 读服务用"门"控制什么时候回，时间用 ui.Tester.Advance 推进，不靠墙钟；数据量用 600 条 × 3 个角色。

// gatedMemory 在 ListMemory、ReadMemoryDetail 前等"门"：hold 以后的读都停在门口，release 才放行
type gatedMemory struct {
	*demoMemory
	mu     sync.Mutex
	gate   chan struct{}
	evGate chan struct{} // 只卡证据的读，别的照常回
	ovGate chan struct{} // 只卡第一份概览
}

func (g *gatedMemory) holdOverview() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ovGate == nil {
		g.ovGate = make(chan struct{})
	}
}

func (g *gatedMemory) overviewHeld() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ovGate != nil
}

func (g *gatedMemory) releaseOverview() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ovGate != nil {
		close(g.ovGate)
		g.ovGate = nil
	}
}

func (g *gatedMemory) holdEvidence() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.evGate == nil {
		g.evGate = make(chan struct{})
	}
}

func (g *gatedMemory) releaseEvidence() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.evGate != nil {
		close(g.evGate)
		g.evGate = nil
	}
}

func newGatedMemory(perChar int) *gatedMemory {
	return &gatedMemory{demoMemory: newDemoMemoryN([]string{"suzune", "reika", "nodb"}, perChar)}
}

func (g *gatedMemory) hold() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gate == nil {
		g.gate = make(chan struct{})
	}
}

func (g *gatedMemory) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gate != nil {
		close(g.gate)
		g.gate = nil
	}
}

func (g *gatedMemory) wait(ctx context.Context) error {
	g.mu.Lock()
	ch := g.gate
	g.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gatedMemory) ListMemory(ctx context.Context, req api.MemoryListRequest) (api.Result[api.MemoryPage], error) {
	if err := g.wait(ctx); err != nil {
		return api.Result[api.MemoryPage]{State: api.Failed, Err: err}, err
	}
	return g.demoMemory.ListMemory(ctx, req)
}

func (g *gatedMemory) ReadMemoryDetail(ctx context.Context, character, ref string) (api.Result[api.MemoryDetail], error) {
	if err := g.wait(ctx); err != nil {
		return api.Result[api.MemoryDetail]{State: api.Failed, Err: err}, err
	}
	return g.demoMemory.ReadMemoryDetail(ctx, character, ref)
}

func (g *gatedMemory) ReadMemoryRecent(ctx context.Context, character string) (api.Result[[]api.MemoryRecentChange], error) {
	if err := g.wait(ctx); err != nil {
		return api.Result[[]api.MemoryRecentChange]{State: api.Failed, Err: err}, err
	}
	return g.demoMemory.ReadMemoryRecent(ctx, character)
}

func (g *gatedMemory) ReadMemoryEvidence(ctx context.Context, character, ref string, cursor api.Cursor) (api.Result[api.MemoryEvidencePage], error) {
	g.mu.Lock()
	ch := g.evGate
	g.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return api.Result[api.MemoryEvidencePage]{State: api.Failed, Err: ctx.Err()}, ctx.Err()
		}
	}
	return g.demoMemory.ReadMemoryEvidence(ctx, character, ref, cursor)
}

func (g *gatedMemory) ReadMemoryQuote(ctx context.Context, character string, src api.MemorySource) (api.Result[api.MemoryQuote], error) {
	if err := g.wait(ctx); err != nil {
		return api.Result[api.MemoryQuote]{State: api.Failed, Err: err}, err
	}
	return g.demoMemory.ReadMemoryQuote(ctx, character, src)
}

// WatchMemory 等概览的门：门关着时第一份概览迟迟不来（测"概览没到时整页不乱闪"）
func (g *gatedMemory) WatchMemory(ctx context.Context) (api.Subscription[api.MemoryOverview], error) {
	g.mu.Lock()
	ch := g.ovGate
	g.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.demoMemory.WatchMemory(ctx)
}

// 推进测试时钟 d，再把后台结果并回界面线程、重画
func (h *memHarness) advance(d time.Duration) {
	h.t.Helper()
	h.tt.Advance(d)
	settleMem(h.q)
	h.tt.Frame()
}

// waitHas 放行队列、画帧，直到页面上有这段字：数据在后台协程里读，放闸以后多久回来不定，advance 只推虚拟时间
func (h *memHarness) waitHas(text string) {
	h.t.Helper()
	remoteWait(h.t, "页面上出现 "+text, func() bool { settleMem(h.q); h.tt.Frame(); return h.has(text) })
}

// clickPrefix 点第一个以 prefix 开头的字（筛选标签带数字，数字随数据量变）
func (h *memHarness) clickPrefix(prefix string) {
	h.t.Helper()
	for _, s := range h.tt.Texts() {
		if strings.HasPrefix(s, prefix) {
			h.click(s)
			return
		}
	}
	h.t.Fatalf("窗口里没有以 %q 开头的字：%v", prefix, h.texts(60))
}

const memScale = 600

// 快：第一次打开，列表在阈值内到了。从开始到到达的每一帧都没有骨架，到了直接出内容。
func TestMemoryLoadFastFirstOpenNeverShowsSkeleton(t *testing.T) {
	g := newGatedMemory(memScale)
	g.hold()
	h := newMemHarness(t, g, 2048, 1030)
	base := loadui.Drawn()
	for i := 0; i < 5; i++ { // 0 到 250 毫秒，每 50 毫秒一帧
		if h.has(demoRowText(0)) {
			t.Fatalf("第 %d 帧：门还关着，行不该有", i)
		}
		h.advance(50 * time.Millisecond)
		if n := loadui.Drawn(); n != base {
			t.Fatalf("第 %d 帧（%d 毫秒）就画了骨架（%d → %d）", i, (i+1)*50, base, n)
		}
		if h.has("取数据中…") {
			t.Fatalf("第 %d 帧副标题就写了取数据中", i)
		}
	}
	h.advance(40 * time.Millisecond) // 290 毫秒
	g.release()
	h.frame()
	h.waitHas(demoRowText(0))
	if !h.has(demoRowText(0)) {
		t.Fatalf("放行后行没出来：%v", h.texts(40))
	}
	for i := 0; i < 8; i++ { // 之后再过 2 秒也不该补出骨架
		h.advance(250 * time.Millisecond)
	}
	if n := loadui.Drawn(); n != base {
		t.Fatalf("到了以后又画了骨架（%d → %d）", base, n)
	}
}

// 慢：阈值前什么都不画；阈值一到出骨架和"取数据中…"；数据比最短时长早到，骨架留够才换成行
func TestMemoryLoadSlowFirstOpenShowsSkeletonAndHolds(t *testing.T) {
	g := newGatedMemory(memScale)
	g.hold()
	h := newMemHarness(t, g, 2048, 1030)
	base := loadui.Drawn()
	h.advance(290 * time.Millisecond)
	if loadui.Drawn() != base || h.has("取数据中…") {
		t.Fatalf("290 毫秒时不该有骨架")
	}
	h.advance(20 * time.Millisecond) // 310 毫秒，过了阈值
	if loadui.Drawn() == base {
		t.Fatalf("过了阈值没有出骨架：%v", h.texts(30))
	}
	if !h.has("取数据中…") {
		t.Fatalf("骨架出来时副标题应写取数据中：%v", h.texts(30))
	}
	// 数据马上到：骨架才出来 10 毫秒，要留够 300 毫秒
	g.release()
	h.frame()
	if h.has(demoRowText(0)) {
		t.Fatal("骨架刚出来就被换掉了")
	}
	h.advance(200 * time.Millisecond)
	if h.has(demoRowText(0)) {
		t.Fatal("骨架只留了 210 毫秒就撤了")
	}
	h.advance(100 * time.Millisecond) // 共 310 毫秒，够了
	h.waitHas(demoRowText(0))
	if !h.has(demoRowText(0)) {
		t.Fatalf("留够以后行应出来：%v", h.texts(30))
	}
	mid := loadui.Drawn()
	h.advance(500 * time.Millisecond)
	if loadui.Drawn() != mid {
		t.Fatal("行出来以后骨架还在画")
	}
}

// 骨架第 0 行和真实第 0 行的位置、间距一致：换的时候不跳。用同一个窗口、同一套 ReduceMotion（只画底色）量
func TestMemoryLoadSkeletonRowsLineUpWithRealRows(t *testing.T) {
	g := newGatedMemory(memScale)
	g.hold()
	h := newMemHarness(t, g, 2048, 1030)
	h.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	h.advance(400 * time.Millisecond) // 骨架出来了
	skelImg := h.tt.Image()
	g.release()
	h.advance(400 * time.Millisecond) // 留够、行出来
	h.waitHas(demoRowText(0))
	if !h.has(demoRowText(0)) {
		t.Fatalf("行没出来：%v", h.texts(30))
	}
	r0, _ := h.tt.Find(demoRowText(0))
	skel := barRuns(skelImg, int(r0.X))
	r1, _ := h.tt.Find(demoRowText(1))
	r2, _ := h.tt.Find(demoRowText(2))
	pitch := r1.Y - r0.Y
	if pitch <= 0 || abs32f(r2.Y-r1.Y-pitch) > 0.01 { // 布局是浮点，相邻两行差几个最低位不算不规则
		t.Fatalf("真实行间距不规则：%v %v %v", r0, r1, r2)
	}
	if len(skel) < 6 {
		t.Logf("r0=%v 图 %v 段 %v", r0, skelImg.Bounds(), skel)
		t.Fatalf("没在图上找到骨架条（%d 段）", len(skel))
	}
	// 骨架每行两条：第一条的位置落在真实第 0 行的框里，隔一行的第一条之间的距离 = 真实行距（Find 给的是整行按钮的框）
	first := skel[0]
	// 行内边距是 Space(2) = 8：骨架第一条的顶 = 真实行的顶 + 8，说明整行（从上边缘算）和真实行对齐
	if off := float32(first.top) - r0.Y; off < 7 || off > 9 {
		t.Errorf("骨架第一条的顶离真实第 0 行的顶 %v，应是行内边距 8：%v %v", off, first, r0)
	}
	got := float32(skel[2].top - skel[0].top)
	if d := got - pitch; d < -1 || d > 1 {
		t.Errorf("骨架行距 %v，真实行距 %v", got, pitch)
	}
}

// 刷新：换种类筛选时旧行一直在，没有骨架，新数据到了原地换；慢的刷新只在副标题写"刷新中…"
func TestMemoryLoadFilterKeepsOldRowsUntilNewArrive(t *testing.T) {
	g := newGatedMemory(memScale)
	h := newMemHarness(t, g, 2048, 1030)
	if !h.has(demoRowText(0)) {
		t.Fatalf("先要有第一页：%v", h.texts(30))
	}
	base := loadui.Drawn()
	g.hold()
	h.clickPrefix("约定 ")
	for i := 0; i < 8; i++ { // 一直等到 2 秒：门没开，旧行必须一直在、不出骨架
		if !h.has(demoRowText(0)) {
			t.Fatalf("第 %d 帧旧行没了：%v", i, h.texts(30))
		}
		if loadui.Drawn() != base {
			t.Fatalf("有旧行还画了骨架")
		}
		h.advance(250 * time.Millisecond)
		if i == 0 && h.has("刷新中…") {
			t.Fatal("刷新才 250 毫秒，标记就出来了")
		}
	}
	if !strings.Contains(strings.Join(h.tt.Texts(), "|"), "刷新中…") {
		t.Fatalf("过了阈值副标题应写刷新中：%v", h.texts(30))
	}
	g.release()
	h.advance(10 * time.Millisecond)
	h.waitHas(demoRowText(1))
	// 新的第一页是"约定"：第 0 条是 i%4==1 的那条
	if h.has(demoRowText(0)) || !h.has(demoRowText(1)) {
		t.Fatalf("新数据到了应原地换：%v", h.texts(30))
	}
	if loadui.Drawn() != base {
		t.Fatal("刷新全程不该画骨架")
	}
}

// 快的刷新：门不关，点一下筛选，每一帧都没有骨架、也没有空窗
func TestMemoryLoadFastFilterNeverBlanks(t *testing.T) {
	g := newGatedMemory(memScale)
	h := newMemHarness(t, g, 2048, 1030)
	base := loadui.Drawn()
	h.clickPrefix("约定 ")
	if h.has(demoRowText(0)) || !h.has(demoRowText(1)) {
		t.Fatalf("筛选结果不对：%v", h.texts(30))
	}
	h.clickPrefix("全部 ")
	h.advance(time.Second)
	if loadui.Drawn() != base {
		t.Fatal("快的刷新画了骨架")
	}
}

// 换角色：新角色的行没到之前，旧角色的行留着但不能点；到了换掉
func TestMemoryLoadCharacterSwitchKeepsRowsButNotClickable(t *testing.T) {
	g := newGatedMemory(memScale)
	h := newMemHarness(t, g, 2048, 1030)
	g.hold()
	h.clickPrefix("铃花 · ")
	if !h.has(demoRowText(0)) {
		t.Fatalf("换角色后旧行应还在：%v", h.texts(30))
	}
	if err := h.tt.Click(demoRowText(3)); err != nil {
		t.Fatal(err)
	}
	h.frame()
	if h.a.mem.selRef != "" {
		t.Fatalf("上一个角色的旧行被点中了：%q", h.a.mem.selRef)
	}
	g.release()
	h.advance(10 * time.Millisecond)
	remoteWait(t, "新角色的行换上", func() bool {
		settleMem(h.q)
		h.tt.Frame()
		return h.a.mem.rowsChar == "reika" && h.a.mem.phase == "ready"
	})
	if h.a.mem.rowsChar != "reika" || h.a.mem.phase != "ready" {
		t.Fatalf("新角色的行没换上：%q %q", h.a.mem.rowsChar, h.a.mem.phase)
	}
	if err := h.tt.Click(demoRowText(3)); err != nil {
		t.Fatal(err)
	}
	h.frame()
	if h.a.mem.selRef == "" {
		t.Fatal("新行到了应能点")
	}
}

// 失败时有旧行：行不清，标已过期；没有旧行才是出错状态
func TestMemoryLoadFailureKeepsRowsAsStale(t *testing.T) {
	g := newGatedMemory(memScale)
	h := newMemHarness(t, g, 2048, 1030)
	g.demoMemory.mu.Lock()
	g.demoMemory.failOn = "后台没回话"
	g.demoMemory.mu.Unlock()
	h.clickPrefix("约定 ")
	if !h.has("已过期") || !h.has(demoRowText(0)) {
		t.Fatalf("应标已过期并留着旧行：%v", h.texts(40))
	}
	if h.has("没取到记忆") {
		t.Fatal("有旧行不该整页换成出错")
	}
	g.demoMemory.mu.Lock()
	g.demoMemory.failOn = ""
	g.demoMemory.mu.Unlock()
	h.click("重试")
	if h.has("已过期") {
		t.Fatalf("重试成功后应去掉过期标记：%v", h.texts(40))
	}
}

// 详情：点另一条，阈值内留空位不画骨架；慢了才出骨架。换条时不出现上一条的内容
func TestMemoryLoadDetailDelayedSkeleton(t *testing.T) {
	g := newGatedMemory(memScale)
	h := newMemHarness(t, g, 2048, 1030)
	g.hold()
	base := loadui.Drawn()
	h.click(demoRowText(3))
	for i := 0; i < 5; i++ {
		h.advance(50 * time.Millisecond)
		if loadui.Drawn() != base {
			t.Fatalf("详情阈值内就画了骨架（第 %d 帧）", i)
		}
	}
	h.advance(100 * time.Millisecond)
	if loadui.Drawn() == base {
		t.Fatal("详情过了阈值应出骨架")
	}
	g.release()
	h.advance(400 * time.Millisecond)
	remoteWait(t, "详情出来", func() bool { settleMem(h.q); h.tt.Frame(); return h.a.mem.det.phase == "ready" })
	if h.a.mem.det.phase != "ready" {
		t.Fatalf("详情没出来：%q", h.a.mem.det.phase)
	}
}

// 回到记忆页：列表和详情先用旧的，不先清空（原来回来会整页闪一次骨架）
func TestMemoryLoadResumeKeepsRowsAndDetail(t *testing.T) {
	g := newGatedMemory(memScale)
	h := newMemHarness(t, g, 2048, 1030)
	h.click(demoRowText(3))
	base := loadui.Drawn()
	h.a.rail = 0
	h.frame()
	g.hold()
	h.a.rail = 2
	h.frame()
	for i := 0; i < 6; i++ {
		if !h.has(demoRowText(0)) {
			t.Fatalf("回来后第 %d 帧旧行没了：%v", i, h.texts(30))
		}
		if h.a.mem.det.phase != "ready" {
			t.Fatalf("回来后详情被清了：%q", h.a.mem.det.phase)
		}
		h.advance(100 * time.Millisecond)
	}
	if loadui.Drawn() != base {
		t.Fatal("回来后画了骨架")
	}
	g.release()
	h.advance(10 * time.Millisecond)
}

// 最近变动：第一次慢才出骨架；再打开时旧的先在，不闪骨架
func TestMemoryLoadRecentDelayedAndKeepsOld(t *testing.T) {
	g := newGatedMemory(memScale)
	h := newMemHarness(t, g, 2048, 1030)
	g.hold()
	base := loadui.Drawn()
	h.click("最近变动")
	h.advance(250 * time.Millisecond)
	if loadui.Drawn() != base {
		t.Fatal("最近变动 250 毫秒就画了骨架")
	}
	h.advance(100 * time.Millisecond)
	if loadui.Drawn() == base {
		t.Fatal("最近变动过了阈值应出骨架")
	}
	g.release()
	h.advance(10 * time.Millisecond)
	if h.has("编造的最近变动") {
		t.Fatal("骨架没留够就被换了")
	}
	h.advance(400 * time.Millisecond)
	h.waitHas("编造的最近变动")
	if !h.has("编造的最近变动") {
		t.Fatalf("最近变动没出来：%v", h.texts(30))
	}
	// 关掉再打开：旧的先在，不画骨架
	h.click("回到列表")
	g.hold()
	mid := loadui.Drawn()
	h.click("最近变动")
	for i := 0; i < 8; i++ {
		if !h.has("编造的最近变动") {
			t.Fatalf("第 %d 帧：重开时旧的最近变动没留着", i)
		}
		h.advance(250 * time.Millisecond)
	}
	if loadui.Drawn() != mid {
		t.Fatal("重开最近变动画了骨架")
	}
	g.release()
}

// 证据那一行："取数据中…"阈值后才写，位置一直占着
func TestMemoryLoadEvidenceHintDelayed(t *testing.T) {
	g := newGatedMemory(memScale)
	h := newMemHarness(t, g, 2048, 1030)
	g.holdEvidence()        // 只卡证据：详情先到，证据停在门口
	h.click(demoRowText(3)) // 第 3 条是事实，会取证据
	if h.a.mem.det.phase != "ready" {
		t.Fatalf("详情没出来：%q", h.a.mem.det.phase)
	}
	h.advance(250 * time.Millisecond)
	if h.has("取数据中…") {
		t.Fatalf("证据 250 毫秒就写了取数据中：%v", h.texts(60))
	}
	h.advance(100 * time.Millisecond)
	if !h.has("取数据中…") {
		t.Fatalf("证据过了阈值应写取数据中：%v", h.texts(60))
	}
	g.releaseEvidence()
	h.advance(10 * time.Millisecond)
	if h.has("取数据中…") {
		t.Fatal("证据到了提示还在")
	}
}

// 概览没到时整页安静：不闪"她还没有记忆"，左栏提示阈值后才出现
func TestMemoryLoadOverviewPendingNoFlashOfEmptyState(t *testing.T) {
	g := newGatedMemory(memScale)
	g.holdOverview()
	h := newMemHarness(t, g, 2048, 1030)
	for i := 0; i < 5; i++ {
		h.advance(50 * time.Millisecond)
		for _, bad := range []string{"她还没有记忆", "取数据中…", "还没有角色"} {
			if h.has(bad) {
				t.Fatalf("第 %d 帧（%d 毫秒）出现了 %q：%v", i, (i+1)*50, bad, h.texts(40))
			}
		}
	}
	h.advance(100 * time.Millisecond)
	if !h.has("取数据中…") {
		t.Fatalf("过了阈值左栏应写取数据中：%v", h.texts(40))
	}
	g.releaseOverview()
	h.advance(400 * time.Millisecond)
	if !h.has(demoRowText(0)) {
		t.Fatalf("概览到了以后列表应出来：%v", h.texts(40))
	}
}

// 阈值和最短保留写成公共常量，页面不自己带数
func TestMemoryLoadUsesSharedConstants(t *testing.T) {
	t.Parallel()
	if loadstate.ShowDelay != 300*time.Millisecond || loadstate.MinShow != 300*time.Millisecond {
		t.Fatalf("%v %v", loadstate.ShowDelay, loadstate.MinShow)
	}
}

type barRun struct{ top, bottom int }

// barRuns 在测试画出的图上找骨架条：同一行上从真实行的左边缘 textX 往右 20、120、220 三个点的颜色都和行右边空白处不一样，就算在条上
// （文字笔画碰不巧三个点都中）；相邻的行合成一段，从上到下列出来。要先设 ReduceMotion，条是纯底色
func barRuns(img *image.RGBA, textX int) []barRun {
	return barRunsAt(img, textX+1100, textX+20, textX+120, textX+220)
}

// barRunsAt 同 barRuns，底色取 bgX 那一列，探测点自己给（骨架条窄的页面用）
func barRunsAt(img *image.RGBA, bgX int, probes ...int) []barRun {
	b := img.Bounds()
	differs := func(x, y int) bool {
		bg, px := img.RGBAAt(bgX, y), img.RGBAAt(x, y)
		d := abs8(bg.R, px.R) + abs8(bg.G, px.G) + abs8(bg.B, px.B)
		return d >= 6
	}
	var runs []barRun
	in := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		bar := true
		for _, x := range probes {
			bar = bar && differs(x, y)
		}
		switch {
		case bar && !in:
			runs, in = append(runs, barRun{top: y, bottom: y}), true
		case bar:
			runs[len(runs)-1].bottom = y
		default:
			in = false
		}
	}
	return runs
}

func abs8(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}
