package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 旧事实被取代后，召回要把旧值和它自己的日期一起带出来（调研-记忆评测和对比 第六节：取代丢了旧值日期，时间题答成同一天）。
// 造法：时钟可拨，旧的写在 40 天前、新的写在 3 天前，名字、地名都是编的。

func supersedeClock(t *testing.T) (*Memory, *time.Time) {
	t.Helper()
	clock := toolsNow.Add(-40 * 24 * time.Hour)
	m, err := Open(context.Background(), migratedDB(t, nil), nil, nil, Options{Writable: true, Character: "suzune", Logf: (&logSink{}).logf, Events: newSQLEvents(t),
		Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, &clock
}

func TestRecallCarriesSupersededValueWithDates(t *testing.T) {
	t.Parallel()
	m, clock := supersedeClock(t)
	mustApply(t, m, remember(ownerFact("主人住在旧城")))
	oldID := str(t, m, `SELECT id FROM facts WHERE text = '主人住在旧城'`)
	*clock = toolsNow.Add(-3 * 24 * time.Hour)
	nf := ownerFact("主人搬到了新城")
	nf.Supersedes = oldID
	mustApply(t, m, remember(nf))
	*clock = toolsNow

	r := m.Recall(context.Background(), ownerHere, Query{Text: "主人住在哪", Mode: ModeExplicit, Now: toolsNow})
	var hit *Hit
	for i := range r.Hits {
		if r.Hits[i].Text == "主人搬到了新城" {
			hit = &r.Hits[i]
		}
	}
	if hit == nil {
		t.Fatalf("新值应该被召回：%+v", r.Hits)
	}
	// 每轮注入的写法：新值 [3 天前]，旧值 [40 天前]
	line := Render(Recalled{Hits: []Hit{*hit}}, toolsNow).Hits[0]
	if !strings.Contains(line, "[3 天前]") || !strings.Contains(line, "[40 天前] 主人住在旧城") {
		t.Fatalf("注入的行里新旧两个值都要带各自的日子：%q", line)
	}

	// memory_search 的写法：同样带
	res, err := m.Search(context.Background(), ownerHere, "主人住在哪")
	if err != nil || res.IsError {
		t.Fatalf("Search：%+v %v", res, err)
	}
	if !strings.Contains(res.Text, "[3 天前]") || !strings.Contains(res.Text, "[40 天前] 主人住在旧城") {
		t.Fatalf("memory_search 也要带旧值和日子：%q", res.Text)
	}
	// 旧值不单独被想起：召回里只有新值这一条
	if n := len(r.Hits); n != 1 {
		t.Fatalf("旧值不该单独成为命中：%d 条", n)
	}
}

// 旧值只在"看得见新值的人都看得见旧值"时才带：旧的是私事、新的放宽了范围，群里不能借新值把旧值带出去
func TestSupersededValueNotLeakedToWiderScope(t *testing.T) {
	t.Parallel()
	m, _ := supersedeClock(t)
	old := ownerFact("主人住在旧城")
	mustApply(t, m, remember(old))
	oldID := str(t, m, `SELECT id FROM facts WHERE text = '主人住在旧城'`)
	nf := ownerFact("主人搬到了新城")
	nf.Supersedes = oldID
	nf.Scopes = []string{"owner", "public"}
	mustApply(t, m, remember(nf))
	res, err := m.Search(context.Background(), ownerHere, "主人住在哪")
	if err != nil || !strings.Contains(res.Text, "主人搬到了新城") {
		t.Fatalf("Search：%+v %v", res, err)
	}
	if strings.Contains(res.Text, "旧城") {
		t.Fatalf("新值比旧值范围宽，不能带旧值：%q", res.Text)
	}
}

// 取代链：最多往前带三个，新的在前
func TestPreviousChainCappedNewestFirst(t *testing.T) {
	t.Parallel()
	m, clock := supersedeClock(t)
	prev := ""
	for i, name := range []string{"城甲", "城乙", "城丙", "城丁", "城戊"} {
		*clock = toolsNow.Add(-time.Duration(50-i*10) * 24 * time.Hour)
		f := ownerFact("主人住在" + name)
		f.Supersedes = prev
		mustApply(t, m, remember(f))
		prev = str(t, m, `SELECT id FROM facts WHERE text = ?`, "主人住在"+name)
	}
	res, err := m.Search(context.Background(), ownerHere, "主人住在城戊")
	if err != nil {
		t.Fatal(err)
	}
	iD, iC, iB := strings.Index(res.Text, "主人住在城丁"), strings.Index(res.Text, "主人住在城丙"), strings.Index(res.Text, "主人住在城乙")
	if iD < 0 || iC < iD || iB < iC || strings.Contains(res.Text, "城甲") {
		t.Fatalf("最多带三个旧的、新的在前：%q", res.Text)
	}
}

// 旧值随新值注入给她（召回不分场合），所以说出去也要拦：群友套话问旧值，守卫照样拦，对应的是旧版本的编号
func TestSupersededValueStillGuardedOutbound(t *testing.T) {
	t.Parallel()
	m, clock := supersedeClock(t)
	mustApply(t, m, remember(ownerFact("主人住在旧城")))
	oldID := str(t, m, `SELECT id FROM facts WHERE text = '主人住在旧城'`)
	*clock = toolsNow.Add(-3 * 24 * time.Hour)
	nf := ownerFact("主人搬到了新城")
	nf.Supersedes = oldID
	mustApply(t, m, remember(nf))
	group := Audience{Channel: "qq", Conversation: "qq:group:303", Scene: "group"}
	blocked, err := m.CheckOutbound(context.Background(), "主人住在旧城", group)
	if err != nil || len(blocked) != 1 || blocked[0] != "f:"+oldID {
		t.Fatalf("群友套话旧值未拦：%v %v", blocked, err)
	}
	if blocked, err := m.CheckOutbound(context.Background(), "主人搬到了新城", group); err != nil || len(blocked) != 1 {
		t.Fatalf("新值照旧拦：%v %v", blocked, err)
	}
	// 主人自己的私聊不拦
	if blocked, err := m.CheckOutbound(context.Background(), "主人住在旧城", ownerHere); err != nil || len(blocked) != 0 {
		t.Fatalf("主人私聊不该拦：%v %v", blocked, err)
	}
}
