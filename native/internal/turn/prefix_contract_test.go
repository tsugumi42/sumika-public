package turn

// 提示词前缀的契约（cache-audit-20261113 第 5 节第 1 条）：用假网关记下的请求逐字节比。

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/memory"
)

// 渠道回合的多条批次：回合里就照库逐条追加，交回写库以后下一轮的请求以上一轮的请求为字节前缀（以前拼成一条多行、交回后变成 N 条，
// 下一轮在这批的位置断开）。两批都是几个人一起说（mixed），复用同一个实例
func TestChannelMultiBatchPrefixStable(t *testing.T) {
	t.Parallel()
	p := newPrefixRun(t, 4)
	batch := func(r int, who ...string) {
		t.Helper()
		var seqs []int64
		var lines []string
		for k, w := range who {
			s, l := p.storeLine(t, w, k, fmt.Sprintf("第 %d 批第 %d 句", r, k))
			seqs, lines = append(seqs, s), append(lines, l)
		}
		o := wait(t, p.h.hub.Respond(Input{CharacterID: p.h.c.CharacterID, Conversation: groupConv, Seqs: seqs, Text: strings.Join(lines, "\n"),
			Audience: groupAud, Addressed: true, Scope: memory.ScopeKey(groupAud)}))
		p.h.commitSaid(groupConv, o, nil)
		p.clock.add(time.Minute)
	}
	batch(0, "p1", "p2", "p3")
	batch(1, "p2", "p1")
	batch(2, "p3", "p1", "p2", "p1")
	for i := 1; i < 3; i++ {
		prev, cur := p.h.messages(i-1), p.h.messages(i)
		if !isPrefix(prev, cur) {
			n := 0
			for n < len(prev) && n < len(cur) && prev[n] == cur[n] {
				n++
			}
			t.Fatalf("第 %d 次请求不以第 %d 次为前缀，断在第 %d 条：\n上一次：%s\n这一次：%s", i+1, i, n, at(prev, n), at(cur, n))
		}
	}
	// 这一批在请求里是一条一行（每个 seq 一条 user 消息），和库里的写法一样
	last := p.h.messages(2)
	n := 0
	for _, m := range last {
		if strings.Contains(m, "第 2 批第") {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("第三批四句应该各是一条消息，实际有 %d 条带它们", n)
	}
}

func at(ms []string, i int) string {
	if i < len(ms) {
		return ms[i]
	}
	return "（没有）"
}

// 从库里重建（闲了 45 分钟以上）：窗口起点锚定，没跨过跳点时两次重建带的历史一样、和最早建的实例也一样；
// 参考信息块按档存库、重建时插回原位（第七节），所以重建出来的请求以上一轮请求整个为前缀，不再在第一块参考信息那里断开；
// 交回以后她的话不带思考，接着用的实例和重建出来的那条逐字节一样
func TestRebuildPrefixAnchored(t *testing.T) {
	t.Parallel()
	p := newPrefixRun(t, 6)
	for i := 0; i < 40; i++ {
		p.storeLine(t, "p1", i, fmt.Sprintf("旧话 %d", i))
	}
	round := func(r int, gap time.Duration) {
		t.Helper()
		p.clock.add(gap)
		s, l := p.storeLine(t, "p1", 0, fmt.Sprintf("第 %d 轮", r))
		o := wait(t, p.h.hub.Respond(Input{CharacterID: p.h.c.CharacterID, Conversation: groupConv, Seqs: []int64{s}, Text: l,
			Audience: groupAud, Addressed: true, Scope: memory.ScopeKey(groupAud)}))
		p.h.commitSaid(groupConv, o, nil)
	}
	round(0, time.Minute)    // 第 1 次：新建
	round(1, time.Minute)    // 第 2 次：接着用
	round(2, 50*time.Minute) // 第 3 次：闲置后重建
	round(3, 50*time.Minute) // 第 4 次：又重建
	p.collect(t, "")
	if !p.reqs[2].rebuild || !p.reqs[3].rebuild || p.reqs[1].rebuild {
		t.Fatalf("剧本没走到想要的重建：%v %v %v", p.reqs[1].rebuild, p.reqs[2].rebuild, p.reqs[3].rebuild)
	}
	firstBlock := func(ms []string) int {
		for i, m := range ms {
			if strings.Contains(m, strings.Trim(jsjson.Quote(ContextHeader), `"`)) {
				return i
			}
		}
		return len(ms)
	}
	same := func(a, b []string, k int, what string) {
		t.Helper()
		if k < 30 {
			t.Fatalf("%s：第一块参考信息之前应有整段历史，只有 %d 条", what, k)
		}
		if len(b) < k || !isPrefix(a[:k], b) {
			n := 0
			for n < k && n < len(b) && a[n] == b[n] {
				n++
			}
			t.Fatalf("%s：前 %d 条应逐字节相同，断在第 %d 条：\n%s\n%s", what, k, n, at(a, n), at(b, n))
		}
	}
	m0, m1, m2, m3 := p.h.messages(0), p.h.messages(1), p.h.messages(2), p.h.messages(3)
	same(m0, m2, firstBlock(m0), "新建的和重建的")
	same(m2, m3, firstBlock(m2), "两次重建")
	// 参考信息块一块不少地插回来：接着用的第 2 次请求整个是重建出来的第 3 次的前缀，第 3 次也是第 4 次的前缀
	same(m1, m2, len(m1), "接着用的和闲置后重建的（参考信息块一并插回）")
	same(m2, m3, len(m2), "两次重建（参考信息块一并插回）")
	// 她第 0 轮那句：接着用的实例（第 2 次请求）和重建出来的（第 3 次）逐字节一样，都不带思考
	reply := func(ms []string) string {
		for _, m := range ms {
			if strings.Contains(m, "好的，第 0 句") {
				return m
			}
		}
		t.Fatalf("请求里没有她第 0 轮那句")
		return ""
	}
	if a, b := reply(p.h.messages(1)), reply(m2); a != b || strings.Contains(a, "reasoning") {
		t.Fatalf("交回以后她那句应和重建的一样、不带思考：\n接着用 %s\n重建的 %s", a, b)
	}
}

// 开口时排的和库里存的那一行不一样（比如被引那条的链接摘要入库以后才有）：照旧拼成一条，她这一轮看到的不比以前少
func TestChannelBatchFallsBackWhenLinesDiffer(t *testing.T) {
	t.Parallel()
	p := newPrefixRun(t, 2)
	s1, l1 := p.storeLine(t, "p1", 0, "第一句")
	s2, l2 := p.storeLine(t, "p2", 1, "第二句")
	text := l1 + "\n" + l2 + "｜链接：开口时才有的摘要"
	wait(t, p.h.hub.Respond(Input{CharacterID: p.h.c.CharacterID, Conversation: groupConv, Seqs: []int64{s1, s2}, Text: text,
		Audience: groupAud, Addressed: true, Scope: memory.ScopeKey(groupAud)}))
	ms := p.h.messages(0)
	if want := `{"role":"user","content":` + jsjson.Quote(text) + `}`; !strings.Contains(strings.Join(ms, "\n"), want) {
		t.Fatalf("应照旧拼成一条带开口时的全部内容：%q", ms)
	}
	if !p.h.logged("和开口时排的不一样") {
		t.Fatal("退回拼成一条应记一行日志")
	}
}

// 离线剧本的契约（TestPrefixReport 只出数字，这个有断言）：QQ 群里说话人换来换去、闲置后重建，
// 参考信息块存了库以后，重建出来的请求和接着用的实例一样——每一次请求都以上一次请求整个为前缀，
// 只有窗口起点往后跳的那一格（ADR-366）例外。以前重建那几次丢了参考块，前缀停在第一块参考信息前面
func TestPrefixRebuildEqualsReuse(t *testing.T) {
	t.Parallel()
	const rounds = 14
	p := newPrefixRun(t, rounds)
	groupScript(t, p, rounds)
	rebuilds, jumps := 0, 0
	for i := 1; i < len(p.reqs); i++ {
		prev, cur := p.reqs[i-1], p.reqs[i]
		_, m := cur.common(prev)
		if cur.rebuild {
			rebuilds++
		}
		if cur.rebuild && m <= 1 {
			jumps++
			continue // 窗口起点往后跳了一格：历史第一条就对不上，这一次本来就接不上
		}
		if m != len(prev.msgs) {
			t.Fatalf("第 %d 次请求（重建=%v）只和上一次相同到第 %d/%d 条：\n上一次 %s\n这一次 %s", i+1, cur.rebuild, m, len(prev.msgs), at(prev.msgs, m), at(cur.msgs, m))
		}
	}
	if rebuilds < 5 {
		t.Fatalf("剧本里重建太少（%d 次），测不到东西", rebuilds)
	}
	if jumps > 3 {
		t.Fatalf("窗口跳变 %d 次，比预想的多：剧本或窗口算法变了，先看是不是别的断开被当成了跳变", jumps)
	}
}
