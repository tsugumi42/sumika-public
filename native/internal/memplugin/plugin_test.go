package memplugin

// 钩子本身的测试：不经内核，直接给 ContextIn 调。经过内核的在 hub_test.go。
// 对着边界清单的 ④（停掉、超时以后旧调用还在回来）、⑤（多角色、多听众、四种场合）、⑬（并发）、⑧（嵌入晚到）

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/memory"
	"sumika-native/internal/turn"
)

// 单测宿主：把返回的贡献接纳后再观察账本；丢弃路径直接调 context。
func acceptedContext(lf *life, ctx context.Context, in turn.ContextIn) ([]string, error) {
	c, err := lf.context(ctx, in)
	if err != nil {
		return nil, err
	}
	input, err := lf.inputContext(ctx, in)
	if err != nil {
		return nil, err
	}
	if input != nil && input.OnAccepted != nil {
		input.OnAccepted()
	}
	if c == nil {
		return nil, nil
	}
	if c.OnAccepted != nil {
		c.OnAccepted()
	}
	return c.Lines, nil
}

// 第一轮给固定要点和命中；原样再问一遍什么都不加；问到新的只加新的，固定要点不重复（缓存规则：只追加）
func TestAppendOnly(t *testing.T) {
	t.Parallel()
	e := &fakeEmbed{}
	m := openMemory(t, fixedNow, standardFacts(), e)
	e.reset()
	lf, _ := newLife(t, only(m), nil)
	st := map[string]any{}

	lines, err := acceptedContext(lf, context.Background(), in("suzu", consoleAud, "吉他练得怎么样了", st))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"要一直遵守的约定和边界：", pinnedPublic, pinnedOwner, "可能相关的记忆：", textGuitar} {
		if count(lines, want) != 1 {
			t.Errorf("第一轮该有一处「%s」：\n%s", want, joined(lines))
		}
	}
	if count(lines, textMove) != 0 || count(lines, textGame) != 0 {
		t.Errorf("没问到的不该给：\n%s", joined(lines))
	}

	// 原样再问：什么都没有新的，整段不出
	lines, _ = acceptedContext(lf, context.Background(), in("suzu", consoleAud, "吉他练得怎么样了", st))
	if len(lines) != 0 {
		t.Fatalf("重复问不该再追加：\n%s", joined(lines))
	}

	// 问到新的一条（待确认的，写"听说"）：只有它，固定要点不重复，上一条不重复
	lines, _ = acceptedContext(lf, context.Background(), in("suzu", consoleAud, "搬家的事怎么样了", st))
	if got := joined(lines); !strings.Contains(got, "听说："+textMove) || strings.Contains(got, "要一直遵守") || strings.Contains(got, textGuitar) {
		t.Fatalf("只该追加新的一条：\n%s", got)
	}
}

// 固定要点变了才重给，整份重给（照 memory.ts 的 contextPart）；没变不给
func TestPinnedChangedGivenAgain(t *testing.T) {
	t.Parallel()
	m1 := openMemory(t, fixedNow, standardFacts(), nil)
	more := append(standardFacts(), seedRow{"7", "boundary", "active", "不拿主人的名字开玩笑", "owner", "public"})
	m2 := openMemory(t, fixedNow, more, nil)
	cur := m1
	lf, _ := newLife(t, func(string) *memory.Memory { return cur }, nil)
	st := map[string]any{}
	ask := func() []string {
		lines, err := acceptedContext(lf, context.Background(), in("suzu", consoleAud, "吉他练得怎么样了", st))
		if err != nil {
			t.Fatal(err)
		}
		return lines
	}

	ask()
	if got := ask(); len(got) != 0 {
		t.Fatalf("固定要点没变不该重给：\n%s", joined(got))
	}
	cur = m2 // 角色卡之外又多了一条边界
	lines := ask()
	for _, want := range []string{pinnedPublic, pinnedOwner, "不拿主人的名字开玩笑"} {
		if count(lines, want) != 1 {
			t.Errorf("固定要点变了应该整份重给，缺「%s」：\n%s", want, joined(lines))
		}
	}
	if count(lines, "可能相关的记忆：") != 0 {
		t.Errorf("命中给过了不该再给：\n%s", joined(lines))
	}
	if got := ask(); len(got) != 0 {
		t.Fatalf("重给以后又不变了，不该再给：\n%s", joined(got))
	}
	cur = m1 // 又少回去：也算变了，重给当前这一份
	if got := ask(); count(got, "不拿主人的名字开玩笑") != 0 || count(got, pinnedPublic) != 1 {
		t.Fatalf("少了一条也要按当前这份重给：\n%s", joined(got))
	}
}

// 插话送到时再来一次（Steer）：同一份记录，所以只多出新的；它说的话也进最近几句
func TestSteerOnlyAppends(t *testing.T) {
	t.Parallel()
	m := openMemory(t, fixedNow, standardFacts(), nil)
	lf, _ := newLife(t, only(m), nil)
	st := map[string]any{}
	if _, err := acceptedContext(lf, context.Background(), in("suzu", consoleAud, "吉他练得怎么样了", st)); err != nil {
		t.Fatal(err)
	}
	steer := func(text string) []string {
		x := in("suzu", consoleAud, text, st)
		x.Steer = true
		lines, err := acceptedContext(lf, context.Background(), x)
		if err != nil {
			t.Fatal(err)
		}
		return lines
	}
	got := joined(steer("对了还有搬家的事"))
	if !strings.Contains(got, textMove) || strings.Contains(got, textGuitar) || strings.Contains(got, "要一直遵守") {
		t.Fatalf("插话那一次只该追加新的：\n%s", got)
	}
	if again := steer("再说说吉他和搬家"); len(again) != 0 {
		t.Fatalf("两条都给过了，插话再来不该重复：\n%s", joined(again))
	}
}

// 寒暄不查，固定要点照给一次；群友问起也能想起同角色的主人私事。
func TestGateAndVisibilityInGroup(t *testing.T) {
	t.Parallel()
	m := openMemory(t, fixedNow, standardFacts(), nil)
	lf, _ := newLife(t, only(m), nil)
	st := map[string]any{}
	chat := func(a audience.Audience, text string, addressed bool) []string {
		x := in("suzu", a, text, st)
		x.Addressed = addressed
		lines, err := acceptedContext(lf, context.Background(), x)
		if err != nil {
			t.Fatal(err)
		}
		return lines
	}

	lines := chat(friendInA, "草", false)
	if got := joined(lines); !strings.Contains(got, pinnedPublic) || !strings.Contains(got, pinnedOwner) || strings.Contains(got, "可能相关") {
		t.Fatalf("寒暄应给同角色约定并跳过召回：\n%s", got)
	}
	if again := chat(friendInA, "哈哈", false); len(again) != 0 {
		t.Fatalf("约定没变、寒暄不查，不该再给：\n%s", joined(again))
	}
	// 想得起两种来源，说不说交给带来源提示词和发送前守卫。
	got := joined(chat(friendInA, "吉他和开黑的事你还记得吗", true))
	if !strings.Contains(got, textGame) || !strings.Contains(got, textGuitar) {
		t.Fatalf("缺少同角色跨场合记忆：\n%s", got)
	}
}

// 被叫到的话不受门控：两个字的"吉他"本来不值得查，被叫到了就要接得上；没被叫到的不查
func TestAddressedBypassesGate(t *testing.T) {
	t.Parallel()
	m := openMemory(t, fixedNow, standardFacts(), nil)
	lf, _ := newLife(t, only(m), nil)
	ask := func(addressed bool) []string {
		x := in("suzu", ownerInA, "吉他", map[string]any{})
		x.Addressed = addressed
		lines, err := acceptedContext(lf, context.Background(), x)
		if err != nil {
			t.Fatal(err)
		}
		return lines
	}
	if got := ask(true); count(got, textGuitar) != 1 {
		t.Errorf("被叫到的两个字也该查：\n%s", joined(got))
	}
	if got := ask(false); count(got, textGuitar) != 0 {
		t.Errorf("没被叫到的两个字不查：\n%s", joined(got))
	}
}

// 给过的要告诉召回（Query.Given）：待确认每轮最多 1 条，同一件事有两条待确认时，第二次问到的该是另一条，而不是把给过的又选一遍再被去重去掉
func TestGivenGoesToRecall(t *testing.T) {
	t.Parallel()
	m := openMemory(t, fixedNow, []seedRow{
		{"1", "fact", "candidate", "群友小A说下周搬家", "qq:60001", "conv:" + groupA},
		{"2", "fact", "candidate", "群友小A搬家以后请大家吃饭", "qq:60001", "conv:" + groupA},
	}, nil)
	lf, _ := newLife(t, only(m), nil)
	st := map[string]any{}
	var seen []string
	for range 3 {
		lines, err := acceptedContext(lf, context.Background(), in("suzu", consoleAud, "搬家的事怎么样了", st))
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, joined(lines))
	}
	a, b := strings.Contains(seen[0], "下周搬家"), strings.Contains(seen[0], "请大家吃饭")
	if a == b {
		t.Fatalf("第一次该只给其中一条：\n%s", seen[0])
	}
	if other := map[bool]string{true: "请大家吃饭", false: "下周搬家"}[a]; !strings.Contains(seen[1], other) || strings.Contains(seen[1], map[bool]string{true: "下周搬家", false: "请大家吃饭"}[a]) {
		t.Fatalf("第二次该给另一条、不重复第一条：\n%s", seen[1])
	}
	if seen[2] != "" {
		t.Fatalf("两条都给过了：\n%s", seen[2])
	}
}

// 主人 → 群友 → 主人：请求者各有上下文去重状态，召回资格相同。
func TestOwnerThenFriendThenOwner(t *testing.T) {
	t.Parallel()
	m := openMemory(t, fixedNow, standardFacts(), nil)
	lf, _ := newLife(t, only(m), nil)
	byScope := map[string]map[string]any{} // 档 → 这个实例的 State
	ask := func(a audience.Audience, text string) []string {
		x := in("suzu", a, text, nil)
		if byScope[x.Scope] == nil {
			byScope[x.Scope] = map[string]any{}
		}
		x.State = byScope[x.Scope]
		lines, err := acceptedContext(lf, context.Background(), x)
		if err != nil {
			t.Fatal(err)
		}
		return lines
	}

	first := joined(ask(ownerInA, "吉他练得怎么样了"))
	if !strings.Contains(first, textGuitar) || !strings.Contains(first, pinnedOwner) {
		t.Fatalf("主人问：该想起自己的事和自己的约定：\n%s", first)
	}
	second := joined(ask(friendInA, "吉他练得怎么样了"))
	for _, memory := range []string{textGuitar, pinnedOwner} {
		if !strings.Contains(second, memory) {
			t.Fatalf("请求者变化丢掉同角色记忆 %q：\n%s", memory, second)
		}
	}
	if !strings.Contains(second, pinnedPublic) {
		t.Fatalf("群友问：公开的约定照给：\n%s", second)
	}
	// 主人再问：回到第一个档的记录，给过的不重复
	if third := ask(ownerInA, "吉他练得怎么样了"); len(third) != 0 {
		t.Fatalf("主人再问：第一个档里给过了，不该重复：\n%s", joined(third))
	}
	if len(byScope) != 2 {
		t.Fatalf("两个档该是两份记录：%d", len(byScope))
	}
}

// 装配处算错档（Input.Scope 和听众对不上）：宁可什么都不给，也不让不同听众共用一个实例的历史；日志只记一次、不带会话 id
func TestScopeMismatchFailsClosed(t *testing.T) {
	t.Parallel()
	m := openMemory(t, fixedNow, standardFacts(), nil)
	lf, logs := newLife(t, only(m), nil)
	cases := []struct {
		name  string
		a     audience.Audience
		scope string
	}{
		{"群里没填档", ownerInA, ""},
		{"群里填成主窗口的档", ownerInA, turn.ConsoleScope},
		{"群友的档给了主人的听众", ownerInA, memory.ScopeKey(friendInA)},
		{"主窗口的听众填了群的档", consoleAud, memory.ScopeKey(friendInA)},
		{"主窗口没填档", consoleAud, ""},
	}
	for _, c := range cases {
		st := map[string]any{}
		for range 2 {
			x := in("suzu", c.a, "吉他练得怎么样了", st)
			x.Scope = c.scope
			lines, err := acceptedContext(lf, context.Background(), x)
			if err != nil || len(lines) != 0 {
				t.Fatalf("%s：对不上的档不该给任何东西：%v %v", c.name, lines, err)
			}
		}
		s := st[stateKey].(*state)
		if len(s.given) != 0 || s.pinnedKey != "" || len(s.recent) != 0 {
			t.Errorf("%s：不给就不能记成给过：%+v", c.name, s)
		}
	}
	if n := strings.Count(logs.String(), "对不上"); n != len(cases) {
		t.Errorf("每个实例只记一次日志，要 %d 行，得到 %d：\n%s", len(cases), n, logs.String())
	}
	if strings.Contains(logs.String(), groupA) || strings.Contains(logs.String(), "owner") {
		t.Errorf("日志不能带会话 id 和档的内容：\n%s", logs.String())
	}
	// 对的档照常给
	if lines, _ := acceptedContext(lf, context.Background(), in("suzu", ownerInA, "吉他练得怎么样了", map[string]any{})); count(lines, textGuitar) != 1 {
		t.Errorf("档对得上就该照常给：%v", lines)
	}
}

// 主窗口的档永远不会撞上 ScopeKey 算出来的值（W1 留的话：memory 不引 turn，所以在这里和 turn.ConsoleScope 直接比）
func TestConsoleScopeNeverEqualsScopeKey(t *testing.T) {
	t.Parallel()
	auds := []audience.Audience{consoleAud, ownerInA, friendInA,
		{Channel: "console", Conversation: "console:group:x", Scene: "group", PrivateOK: true},
		{Channel: "console", Conversation: "console:group:x", Scene: "group"},
		{Channel: "qq", Conversation: "qq:300", Scene: "private", PrivateOK: true, PersonIDs: []string{"owner"}},
		{Channel: "qq", Conversation: "qq:301", Scene: "private", PersonIDs: []string{"qq:301"}},
		{Channel: "qq", Conversation: "qq:302", Scene: "private"},
		{Channel: "qq", Conversation: groupA}, // Scene 空着的群
		{},
	}
	for _, a := range auds {
		if memory.ScopeKey(a) == turn.ConsoleScope {
			t.Errorf("%+v 的档撞上了主窗口的档 %q", a, turn.ConsoleScope)
		}
	}
	// 主窗口的实例仍独立，召回不分场合不改变内核历史隔离。
	if !scopeMatches(turn.ContextIn{Scope: turn.ConsoleScope, Audience: consoleAud}) {
		t.Error("主窗口的听众配主窗口的档该认")
	}
	if scopeMatches(turn.ContextIn{Scope: turn.ConsoleScope, Audience: ownerInA}) {
		t.Error("群里的听众不能用主窗口的档")
	}
	if scopeMatches(turn.ContextIn{Scope: turn.ConsoleScope, Audience: audience.Audience{Channel: "qq", Conversation: "qq:302"}}) {
		t.Error("平台私聊不能用主窗口的表达档")
	}
}

// 多角色：各用各的记忆，各有各的记录；没有记忆的角色跳过，不影响别人
func TestCharactersDontShareMemory(t *testing.T) {
	t.Parallel()
	ma := openMemory(t, fixedNow, []seedRow{{"1", "fact", "active", "铃音这边记着主人在学吉他", "owner", "owner"}}, nil)
	mb := openMemory(t, fixedNow, []seedRow{{"1", "fact", "active", "铃花这边记着主人在学吉他", "owner", "owner"}}, nil)
	lf, _ := newLife(t, func(id string) *memory.Memory {
		switch id {
		case "ling":
			return ma
		case "hua":
			return mb
		}
		return nil
	}, nil)
	stA, stB, stC := map[string]any{}, map[string]any{}, map[string]any{}
	ask := func(id string, st map[string]any) string {
		lines, err := acceptedContext(lf, context.Background(), in(id, consoleAud, "吉他练得怎么样了", st))
		if err != nil {
			t.Fatal(err)
		}
		return joined(lines)
	}
	if got := ask("ling", stA); !strings.Contains(got, "铃音这边") || strings.Contains(got, "铃花这边") {
		t.Errorf("铃音只该看到自己的：\n%s", got)
	}
	if got := ask("hua", stB); !strings.Contains(got, "铃花这边") || strings.Contains(got, "铃音这边") {
		t.Errorf("铃花只该看到自己的：\n%s", got)
	}
	if got := ask("nobody", stC); got != "" {
		t.Errorf("没有记忆的角色不该给：\n%s", got)
	}
	if _, ok := stC[stateKey]; ok {
		t.Error("没有记忆的角色不该留记录")
	}
	if got := ask("ling", stA); got != "" {
		t.Errorf("铃音自己的记录里已经给过了：\n%s", got)
	}
}

// 没有 State 没地方记"给过没有"：报错，不悄悄每轮重给
func TestNoStateIsAnError(t *testing.T) {
	t.Parallel()
	m := openMemory(t, fixedNow, standardFacts(), nil)
	lf, _ := newLife(t, only(m), nil)
	if _, err := lf.context(context.Background(), in("suzu", consoleAud, "吉他练得怎么样了", nil)); err == nil {
		t.Fatal("State 为空该报错")
	}
}

// 钩子超时、这一轮被停下，内核不要这次的结果：给过的不能记，下一轮要照样给（边界 ④：晚到的结果丢掉，状态也不能留）
func TestDroppedResultIsNotRemembered(t *testing.T) {
	t.Parallel()
	cancelled := map[string]func(*testing.T, *fakeEmbed) (context.Context, context.CancelFunc){
		"这一轮被停下": func(t *testing.T, e *fakeEmbed) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				<-e.entered
				cancel()
			}()
			return ctx, cancel
		},
		"钩子超时": func(t *testing.T, e *fakeEmbed) (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 80*time.Millisecond)
		},
	}
	for name, mk := range cancelled {
		t.Run(name, func(t *testing.T) {
			e := &fakeEmbed{entered: make(chan struct{}, 1)}
			m := openMemory(t, fixedNow, standardFacts(), e)
			e.reset()
			lf, _ := newLife(t, only(m), nil)
			st := map[string]any{}
			e.setHold(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
			ctx, cancel := mk(t, e)
			defer cancel()
			lines, err := acceptedContext(lf, ctx, in("suzu", consoleAud, "吉他练得怎么样了", st))
			if err == nil || len(lines) != 0 {
				t.Fatalf("被丢的这一次该返回错误、不给东西：%v %v", lines, err)
			}
			s := st[stateKey].(*state)
			if len(s.given) != 0 || s.pinnedKey != "" || len(s.recent) != 0 {
				t.Fatalf("没送到她手里就不能记成给过：%+v", s)
			}
			// 下一轮嵌入恢复了：固定要点和命中都照样给
			e.setHold(nil)
			lines, err = acceptedContext(lf, context.Background(), in("suzu", consoleAud, "吉他练得怎么样了", st))
			if err != nil || count(lines, textGuitar) != 1 || count(lines, pinnedPublic) != 1 {
				t.Fatalf("下一轮该照样给：%v\n%s", err, joined(lines))
			}
		})
	}
}

// 嵌入很慢：向量那一路最多等 RecallTimeout，退回只用全文，固定要点和字面命中照给；不是整个插件这一轮被内核跳过
func TestSlowEmbedderDegradesToLexical(t *testing.T) {
	t.Parallel()
	e := &fakeEmbed{}
	m := openMemory(t, fixedNow, standardFacts(), e)
	e.reset()
	e.setHold(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	lf, _ := newLife(t, only(m), func(o *Options) { o.RecallTimeout = 40 * time.Millisecond })
	hookCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second) // 内核给钩子的时限（正式是 5 秒）
	defer cancel()
	start := time.Now()
	lines, err := acceptedContext(lf, hookCtx, in("suzu", consoleAud, "吉他练得怎么样了", map[string]any{}))
	if err != nil {
		t.Fatalf("向量那一路该自己先放弃，不该拖到钩子超时：%v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("不该等到钩子的时限：%v", took)
	}
	if count(lines, textGuitar) != 1 || count(lines, pinnedPublic) != 1 || count(lines, pinnedOwner) != 1 {
		t.Fatalf("只用全文也该给出字面命中和约定：\n%s", joined(lines))
	}
}

// 停掉：先拒绝新来的，取消在途的召回，等它退出才返回；在途的那次结果不要、不记；停掉以后不再碰记忆
func TestStopWaitsForInFlightAndDiscards(t *testing.T) {
	t.Parallel()
	e := &fakeEmbed{entered: make(chan struct{}, 1)}
	m := openMemory(t, fixedNow, standardFacts(), e)
	e.reset()
	var asked int
	var mu sync.Mutex
	lf := New(Options{Memories: func(string) *memory.Memory { mu.Lock(); asked++; mu.Unlock(); return m }, Logf: func(string, ...any) {}, Now: func() time.Time { return fixedNow }}).start()
	release := make(chan struct{})
	e.setHold(func(context.Context) error { <-release; return nil }) // 不听取消，只等放行：模拟卡在不可中断的地方
	st := map[string]any{}
	type res struct {
		lines []string
		err   error
	}
	got := make(chan res, 1)
	go func() {
		lines, err := acceptedContext(lf, context.Background(), in("suzu", consoleAud, "吉他练得怎么样了", st))
		got <- res{lines, err}
	}()
	<-e.entered

	stopped := make(chan struct{})
	go func() { lf.stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("在途的召回还没退出，stop 不该返回")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-stopped
	r := <-got
	if r.err != nil || len(r.lines) != 0 {
		t.Fatalf("停掉期间算完的结果不要：%v %v", r.lines, r.err)
	}
	if s := st[stateKey].(*state); len(s.given) != 0 || s.pinnedKey != "" {
		t.Fatalf("不要的结果不能记：%+v", s)
	}

	// 停掉以后再来：不碰记忆、不碰嵌入
	mu.Lock()
	before := asked
	mu.Unlock()
	e.reset()
	lines, err := acceptedContext(lf, context.Background(), in("suzu", consoleAud, "吉他练得怎么样了", st))
	if err != nil || len(lines) != 0 {
		t.Fatalf("停掉以后不该给：%v %v", lines, err)
	}
	mu.Lock()
	after := asked
	mu.Unlock()
	if after != before || len(e.asked()) != 0 {
		t.Fatalf("停掉以后不该再去找记忆（%d→%d）或嵌入（%v）", before, after, e.asked())
	}
	lf.stop() // 可以重复调
}

// 生成候选不记成给过；宿主丢弃后下一轮仍给同一份，接纳后才去重。
func TestPendingContextIsNotRemembered(t *testing.T) {
	t.Parallel()
	m := openMemory(t, fixedNow, standardFacts(), nil)
	lf, _ := newLife(t, only(m), nil)
	st := map[string]any{}
	x := in("suzu", consoleAud, "吉他练得怎么样了", st)
	c, err := lf.context(context.Background(), x)
	if err != nil || c == nil || count(c.Lines, textGuitar) != 1 {
		t.Fatalf("第一份候选不完整：%+v %v", c, err)
	}
	s := st[stateKey].(*state)
	if len(s.given) != 0 || s.pinnedKey != "" || len(s.recent) != 0 {
		t.Fatalf("候选还没接纳就记账了：%+v", s)
	}
	lines, err := acceptedContext(lf, context.Background(), x)
	if err != nil || count(lines, textGuitar) != 1 || count(lines, pinnedPublic) != 1 {
		t.Fatalf("丢弃候选后应该仍给完整贡献：%v %v", lines, err)
	}
	lines, err = acceptedContext(lf, context.Background(), x)
	if err != nil || len(lines) != 0 || len(s.given) == 0 || s.pinnedKey == "" {
		t.Fatalf("接纳后应该记账并去重：%v %v %+v", lines, err, s)
	}
}

// 查询的第二路带上最近几句人说的话；只留最近 4 句，每句只取开头；插话说的也算
func TestRecentFeedsSecondQuery(t *testing.T) {
	t.Parallel()
	e := &fakeEmbed{}
	m := openMemory(t, fixedNow, standardFacts(), e)
	lf, _ := newLife(t, only(m), nil)
	st := map[string]any{}
	say := func(text string, steer bool) {
		t.Helper()
		e.reset()
		x := in("suzu", consoleAud, text, st)
		x.Steer = steer
		if _, err := acceptedContext(lf, context.Background(), x); err != nil {
			t.Fatal(err)
		}
	}
	say("第一句说吉他", false)
	if got := e.asked(); len(got) != 1 || !slices.Equal(got[0], []string{"第一句说吉他"}) {
		t.Fatalf("第一句没有上文，只有一个查询：%q", got)
	}
	say("第二句说搬家", true)
	if got := e.asked(); len(got) != 1 || !slices.Equal(got[0], []string{"第二句说搬家", "第一句说吉他\n第二句说搬家"}) {
		t.Fatalf("第二句该带上第一句：%q", got)
	}
	long := strings.Repeat("长", 150) + "尾巴"
	say(long, false)
	for _, x := range []string{"第三句", "第四句", "第五句", "第六句"} {
		say(x+"说点别的", false)
	}
	say("第七句说点别的", false)
	want := strings.Join([]string{"第三句说点别的", "第四句说点别的", "第五句说点别的", "第六句说点别的", "第七句说点别的"}, "\n")
	if got := e.asked(); len(got) != 1 || len(got[0]) != 2 || got[0][1] != want {
		t.Fatalf("只留最近 4 句（第一、二句和那句长的都挤出去了），得到 %q", got)
	}
	// 上文每句只取开头
	st = map[string]any{}
	say(long, false)
	say("接着说搬家", false)
	if prev, _, _ := strings.Cut(e.asked()[0][1], "\n"); len([]rune(prev)) != recentClip {
		t.Fatalf("上文每句只取开头 %d 个字，得到 %d 个", recentClip, len([]rune(prev)))
	}
}

// 和内核的接口：Memories 没给不启用；同一个插件值停掉以后可以再启用；重复启用报错
func TestSetupOnHub(t *testing.T) {
	t.Parallel()
	hub := turn.NewHub(context.Background(), turn.Config{Logf: func(string, ...any) {}}, nil, nil, nil)
	if err := hub.Use(New(Options{})); err == nil {
		t.Fatal("没给 Memories 不该启用")
	}
	m := openMemory(t, fixedNow, standardFacts(), nil)
	p := New(Options{Memories: only(m), Logf: func(string, ...any) {}})
	if err := hub.Use(p); err != nil {
		t.Fatal(err)
	}
	if err := hub.Use(p); err == nil || !strings.Contains(err.Error(), "已经启用") {
		t.Fatalf("重复启用该报错：%v", err)
	}
	hub.StopPlugin(ID)
	if err := hub.Use(p); err != nil {
		t.Fatalf("停掉以后该能再启用：%v", err)
	}
	hub.StopPlugin(ID)
}

// stateOf 对放错类型的东西不 panic，换成自己的
func TestStateOfIgnoresForeignValue(t *testing.T) {
	t.Parallel()
	lf, _ := newLife(t, func(string) *memory.Memory { return nil }, nil)
	st := map[string]any{stateKey: "别人放的"}
	if s := lf.stateOf(st); s == nil || st[stateKey] != any(s) {
		t.Fatal("该换成自己的记录")
	}
}
