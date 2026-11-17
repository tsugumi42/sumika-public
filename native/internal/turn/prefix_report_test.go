package turn

// 提示词前缀的稳定程度（data/dev/cache-audit-20261113.md 第 5 节第 1 条）：假网关记下每次请求，
// 跑一段固定的群聊和本地私聊（多条批次、交回写库、闲了 45 分钟以上从库里重建），算相邻两次请求逐字节相同的前缀占多少。
// 服务商的缓存按字节前缀认，这个比例就是“服务商一直留着缓存时”的命中率上限。
//
//	SUMIKA_PREFIX_REPORT=1 bun ../scripts/slot.ts go test ./internal/turn/ -run TestPrefixReport -v -count=1
//
// 不设环境变量时跳过（数字只给人看）；契约断言在 prefix_contract_test.go。

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/memory"
	"sumika-native/internal/store"
)

// prefixClock 是能往前拨的钟（闲置重建要它）
type prefixClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *prefixClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *prefixClock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// prefixReq 是一次请求：messages 逐条原样、tools 原样，和它是不是实例重建后的第一次
type prefixReq struct {
	msgs    []string
	tools   string
	rebuild bool
	label   string
}

func (r prefixReq) bytes() int {
	n := len(r.tools)
	for _, m := range r.msgs {
		n += len(m)
	}
	return n
}

// common 是 r 和上一次 prev 逐条相同的前缀字节数（tools 相同也算进去：服务商把它排在消息前面）
func (r prefixReq) common(prev prefixReq) (bytes, msgs int) {
	if r.tools == prev.tools {
		bytes = len(r.tools)
	} else {
		return 0, 0
	}
	for i := 0; i < len(r.msgs) && i < len(prev.msgs); i++ {
		if r.msgs[i] != prev.msgs[i] {
			break
		}
		bytes += len(r.msgs[i])
		msgs++
	}
	return bytes, msgs
}

func parseBody(t *testing.T, body string) (msgs []string, tools string) {
	t.Helper()
	var r struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    json.RawMessage   `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	for _, m := range r.Messages {
		msgs = append(msgs, string(m))
	}
	return msgs, string(r.Tools)
}

// prefixRun 是一段固定剧本跑下来的全部请求
type prefixRun struct {
	h     *harness
	clock *prefixClock
	reqs  []prefixReq
}

func newPrefixRun(t *testing.T, rounds int) *prefixRun {
	clock := &prefixClock{}
	replies := make([]string, rounds+8)
	for i := range replies {
		// 带思考：看交回、重建以后思考字段在不在
		replies[i] = fmt.Sprintf(`{"reasoning":["想一想第 %d 句"],"content":["好的，第 %d 句"],%s}`, i, i, usage)
	}
	h := newHarnessOpts(t, harnessOpts{replies: "[" + strings.Join(replies, ",") + "]", mutate: func(c *Config) {
		clock.now = c.Now()
		c.Now = clock.Now
	}})
	return &prefixRun{h: h, clock: clock}
}

// collect 把第 from 次以后的请求收进来，rebuild 从 model_runs 的 rebuild 列读（实例新建后的第一步才有）
func (p *prefixRun) collect(t *testing.T, label string) {
	t.Helper()
	p.h.mu.Lock()
	bodies := append([]string(nil), p.h.bodies...)
	p.h.mu.Unlock()
	rows, err := p.h.st.Trace.Query(`SELECT coalesce(rebuild, '') FROM model_runs WHERE stage = 'reply' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var rebuilds []string
	for rows.Next() {
		var r string
		rows.Scan(&r)
		rebuilds = append(rebuilds, r)
	}
	rows.Close()
	for i := len(p.reqs); i < len(bodies); i++ {
		msgs, tools := parseBody(t, bodies[i])
		r := prefixReq{msgs: msgs, tools: tools, label: label}
		if i < len(rebuilds) {
			r.rebuild = rebuilds[i] != ""
		}
		p.reqs = append(p.reqs, r)
	}
}

// 群里一个人说的一行：照渠道中间件存（segments.line 是排好的那一行），返回 seq 和那一行
func (p *prefixRun) storeLine(t *testing.T, who string, n int, text string) (int64, string) {
	t.Helper()
	at := p.clock.Now()
	line := fmt.Sprintf("[%s] %s(群友)：%s", at.Format("01-02 15:04"), who, text)
	seq, err := p.h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: groupConv, ActorKind: "human", ActorID: who, Origin: "human", Kind: "message", Text: text,
		Segments: map[string]any{"line": line}, CreatedAt: at.UnixMilli() + int64(n)}, []string{p.h.c.CharacterID})
	if err != nil {
		t.Fatal(err)
	}
	return seq, line
}

// groupScript：先在库里放 45 条旧群聊（她每 4 条回一句），再跑 rounds 轮：每轮一批 1～5 条，回完交回写库；
// 每轮之间过 3 分钟，第 6、12、18… 轮之前过 50 分钟（闲了 45 分钟以上，从库里重建）
func groupScript(t *testing.T, p *prefixRun, rounds int) {
	t.Helper()
	people := []string{"p1", "p2", "p3"}
	for i := 0; i < 45; i++ {
		if i%4 == 3 {
			text := fmt.Sprintf("旧回复 %d", i)
			if _, err := p.h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: groupConv, ActorKind: "character", ActorID: p.h.c.CharacterID, Origin: "character", Kind: "message", Text: text, CreatedAt: p.clock.Now().UnixMilli() + int64(i)}, []string{p.h.c.CharacterID}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		p.storeLine(t, people[i%3], i, fmt.Sprintf("旧话 %d", i))
	}
	sizes := []int{1, 3, 2, 4, 1, 5}
	for r := 0; r < rounds; r++ {
		if r > 0 && r%6 == 0 {
			p.clock.add(50 * time.Minute)
		} else {
			p.clock.add(3 * time.Minute)
		}
		var seqs []int64
		var lines []string
		for k := 0; k < sizes[r%len(sizes)]; k++ {
			s, l := p.storeLine(t, people[(r+k)%3], k, fmt.Sprintf("第 %d 轮第 %d 句", r, k))
			seqs, lines = append(seqs, s), append(lines, l)
		}
		o := wait(t, p.h.hub.Respond(Input{CharacterID: p.h.c.CharacterID, Conversation: groupConv, Seqs: seqs, Text: strings.Join(lines, "\n"),
			Audience: groupAud, Addressed: true, Scope: memory.ScopeKey(groupAud)}))
		p.h.commitSaid(groupConv, o, nil)
	}
	p.collect(t, "QQ 群")
}

// localScript：本地私聊先放 45 条旧话，再跑 rounds 轮，节奏同 groupScript
func localScript(t *testing.T, p *prefixRun, rounds int) {
	t.Helper()
	conv := consoleConv(p.h.c.CharacterID)
	for i := 0; i < 45; i++ {
		kind, origin := "owner", "owner"
		if i%2 == 1 {
			kind, origin = "character", "character"
		}
		actor := "owner"
		if kind == "character" {
			actor = p.h.c.CharacterID
		}
		if _, err := p.h.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: kind, ActorID: actor, Origin: origin, Kind: "message", Text: fmt.Sprintf("旧话 %d", i), CreatedAt: p.clock.Now().UnixMilli() + int64(i)}, []string{p.h.c.CharacterID}); err != nil {
			t.Fatal(err)
		}
	}
	for r := 0; r < rounds; r++ {
		if r > 0 && r%6 == 0 {
			p.clock.add(50 * time.Minute)
		} else {
			p.clock.add(3 * time.Minute)
		}
		p.h.send(fmt.Sprintf("本地第 %d 句", r))
	}
	p.collect(t, "本地私聊")
}

type prefixStats struct {
	label                      string
	n, nRebuild                int
	common, total              int // 全部（第一次请求不算）
	commonReuse, totalReuse    int
	commonRebuild, totalRebuld int
	// 窗口跳变：重建时历史窗口的起点往后跳了一格（ADR-366），第一条历史就和上一次对不上，前缀只剩固定部分。
	// 参考块插回以后重建只剩这一种断开；单列出来，看去掉它以后的数
	nJump                  int
	commonNoJump, totalNoJ int
}

func (s prefixStats) pct(a, b int) string {
	if b == 0 {
		return "  -  "
	}
	return fmt.Sprintf("%5.1f%%", 100*float64(a)/float64(b))
}

func statsOf(label string, reqs []prefixReq) prefixStats {
	s := prefixStats{label: label}
	for i := 1; i < len(reqs); i++ {
		c, _ := reqs[i].common(reqs[i-1])
		b := reqs[i].bytes()
		s.n++
		s.common += c
		s.total += b
		if _, m := reqs[i].common(reqs[i-1]); reqs[i].rebuild && m <= 1 {
			s.nJump++
		} else {
			s.commonNoJump += c
			s.totalNoJ += b
		}
		if reqs[i].rebuild {
			s.nRebuild++
			s.commonRebuild += c
			s.totalRebuld += b
		} else {
			s.commonReuse += c
			s.totalReuse += b
		}
	}
	return s
}

func TestPrefixReport(t *testing.T) {
	if os.Getenv("SUMIKA_PREFIX_REPORT") != "1" {
		t.Skip("设 SUMIKA_PREFIX_REPORT=1 才跑（只出数字）")
	}
	const rounds = 24
	g := newPrefixRun(t, rounds)
	groupScript(t, g, rounds)
	l := newPrefixRun(t, rounds)
	localScript(t, l, rounds)
	fmt.Println("场合        请求  其中重建  全部前缀相同  复用实例  重建那次")
	for _, x := range []struct {
		label string
		reqs  []prefixReq
	}{{"QQ 群", g.reqs}, {"本地私聊", l.reqs}} {
		s := statsOf(x.label, x.reqs)
		fmt.Printf("%-10s  %4d  %8d  %12s  %8s  %8s  （窗口跳变 %d 次；去掉它们 %s）\n", s.label, s.n, s.nRebuild, s.pct(s.common, s.total), s.pct(s.commonReuse, s.totalReuse), s.pct(s.commonRebuild, s.totalRebuld), s.nJump, s.pct(s.commonNoJump, s.totalNoJ))
		if os.Getenv("SUMIKA_PREFIX_DETAIL") == "1" {
			for i := 1; i < len(x.reqs); i++ {
				c, m := x.reqs[i].common(x.reqs[i-1])
				fmt.Printf("  #%02d 重建=%-5v 前缀 %3d/%3d 条 %6d/%6d 字节\n", i, x.reqs[i].rebuild, m, len(x.reqs[i].msgs), c, x.reqs[i].bytes())
			}
		}
	}
}
