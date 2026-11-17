package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// TestStreamFrame 量真实聊天区（a.workbenchChat）在模型流式输出时的每帧耗时（perf/stream-frame 分支，只量不改产品）。
// 设 SUMIKA_SF_HIST=历史条数 才跑；其余环境变量：
//
//	SUMIKA_SF_CPS     每秒字数，默认 45
//	SUMIKA_SF_SECS    流式时长（虚拟秒），默认 30
//	SUMIKA_SF_PROF    写 CPU profile 的路径（只覆盖流式阶段）
//	SUMIKA_SF_CSV     写每帧耗时 CSV 的路径（微秒）
//
// 配合 MYGO_FRAME_STATS=all 时 mygo 把每帧的 build/layout/paint/present 写到标准错误；本测试在流式开始、结束处插 SF PHASE 标记。
// 窗口 2048x1030、像素比 1.25，Tester 走 CPU 绘制，不含 D3D11 呈现。
func TestStreamFrame(t *testing.T) {
	hs := os.Getenv("SUMIKA_SF_HIST")
	if hs == "" {
		t.Skip("SUMIKA_SF_HIST 没设")
	}
	hist, _ := strconv.Atoi(hs)
	cps := envInt("SUMIKA_SF_CPS", 45)
	secs := envInt("SUMIKA_SF_SECS", 30)

	rng := rand.New(rand.NewSource(11))
	msgs := make([]*msg, 0, hist+2)
	for i := range hist {
		mine := i%2 == 0
		text := sfHistText(rng, i, mine)
		m := &msg{id: fmt.Sprintf("h%d", i), who: "她", actor: "char", isChar: !mine, mine: mine, md: !mine, text: text, version: 1,
			blocks: segview.TextBlocks(text), at: t0 + int64(i)*60000}
		if mine {
			m.actor, m.who = "me", "我"
		}
		prepareChatView(m, "local", nil)
		msgs = append(msgs, m)
	}
	ask := &msg{id: "ask", who: "我", actor: "me", mine: true, text: "给我讲讲流式输出怎么做", version: 1, blocks: segview.TextBlocks("给我讲讲流式输出怎么做"), at: t0 + int64(hist)*60000}
	prepareChatView(ask, "local", nil)
	reply := &msg{id: "live-reply-1", who: "她", actor: "char", isChar: true, md: true, streaming: true, liveKind: liveReply, version: 1, at: t0 + int64(hist+1)*60000, enterNow: true}
	prepareChatView(reply, "local", nil)
	msgs = append(msgs, ask, reply)

	a := &app{sel: "c", title: "她", msgs: msgs, gen: 1, srcGen: 1}
	a.list = a.newList()
	views := 0
	tt := ui.NewTester(func(c *ui.Context) { views++; a.applyLook(c); a.workbenchChat(c) }, 2048, 1030)
	tt.SetScale(1.25)

	tt.Frame()
	a.list.ScrollToEnd()
	for range 5 {
		tt.Frame()
	}

	src := []rune(sfReplyText(rng, cps*secs+400))
	if p := os.Getenv("SUMIKA_SF_DUMP"); p != "" { // 把流式正文写出来，给真窗口的 SUMIKA_STREAM_FILE 用
		_ = os.WriteFile(p, []byte(string(src)), 0o644)
	}
	const tick = 16 * time.Millisecond
	ticks := int(time.Duration(secs) * time.Second / tick)
	var durs []time.Duration // 有新字的帧
	var all []time.Duration  // 每个刻度的 Frame 调用
	var csv strings.Builder
	csv.WriteString("tick,changed,chars,us\n")

	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	if p := os.Getenv("SUMIKA_SF_PROF"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			t.Fatal(err)
		}
		defer func() { pprof.StopCPUProfile(); f.Close() }()
	}
	log.Printf("SF PHASE start hist=%d", hist)
	n, acc, idle, frameCount := 0, 0.0, 0, 0
	for k := range ticks {
		acc += float64(cps) * tick.Seconds()
		add := int(acc)
		acc -= float64(add)
		changed := add > 0
		if changed {
			n = min(len(src), n+add)
			reply.text = string(src[:n])
			reply.version++
		}
		v0 := views
		q0 := qpcNow()
		tt.Advance(tick)
		us := qpcMs(q0, qpcNow()) * 1000
		frames := views - v0
		fmt.Fprintf(&csv, "%d,%v,%d,%d,%.0f\n", k, changed, n, frames, us)
		if frames == 0 {
			idle++
			continue
		}
		per := time.Duration(us / float64(frames) * 1000)
		for range frames {
			all = append(all, per)
			if changed {
				durs = append(durs, per)
			}
		}
		frameCount += frames
	}
	runtime.ReadMemStats(&m1)
	log.Printf("SF PHASE end")
	reply.streaming = false
	tt.Frame()

	row := func(name string, ds []time.Duration) string {
		if len(ds) == 0 {
			return name + " 无样本"
		}
		s := append([]time.Duration(nil), ds...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		pct := func(p float64) float64 {
			return float64(s[min(len(s)-1, int(p*float64(len(s))))].Microseconds()) / 1000
		}
		var le417, le833 int
		var sum time.Duration
		for _, d := range s {
			sum += d
			if d <= 4167*time.Microsecond {
				le417++
			}
			if d <= 8333*time.Microsecond {
				le833++
			}
		}
		return fmt.Sprintf("%s hist=%d 帧=%d 平均=%.2f 中位=%.2f p95=%.2f p99=%.2f 最差=%.2f ms  <=4.17ms:%.1f%%  <=8.33ms:%.1f%%",
			name, hist, len(s), float64(sum.Microseconds())/1000/float64(len(s)), pct(0.5), pct(0.95), pct(0.99), float64(s[len(s)-1].Microseconds())/1000,
			100*float64(le417)/float64(len(s)), 100*float64(le833)/float64(len(s)))
	}
	t.Logf("SF RESULT %s", row("有新字的帧", durs))
	t.Logf("SF RESULT %s", row("全部帧", all))
	t.Logf("SF RESULT hist=%d 刻度 %d，其中没出帧 %d，出帧共 %d", hist, ticks, idle, frameCount)
	t.Logf("SF RESULT hist=%d 分配 %.1f MB / %d 帧，GC %d 次，最终字数 %d", hist, float64(m1.TotalAlloc-m0.TotalAlloc)/1e6, len(all), m1.NumGC-m0.NumGC, n)
	if p := os.Getenv("SUMIKA_SF_CSV"); p != "" {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			_ = os.WriteFile(p, []byte(csv.String()), 0o644)
		}
	}
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}

var sfSentences = []string{
	"流式输出的关键是只重排发生变化的那一小段，已经稳定的段落不应该每个字都重新量一遍。",
	"这一点在长对话里尤其明显，因为历史越长，整列重建的代价就越高。",
	"我们可以把消息拆成块，每一块有自己的内容版本，只有版本变了才重新排版。",
	"如果宽度没变、字号没变，旧块的排版结果就可以原样复用。",
	"另外，代码块里的语法高亮也不适合在每个字到来时都重新跑一遍。",
	"Streaming parsers usually keep a stable prefix and only re-parse the open tail of the document.",
	"测量和绘制要分开看：测量慢多半是文字排版，绘制慢多半是阴影、圆角和剪裁。",
}

func sfHistText(rng *rand.Rand, i int, mine bool) string {
	if mine {
		return sfSentences[rng.Intn(len(sfSentences))][:30]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#%d ", i)
	for k := 2 + rng.Intn(4); k > 0; k-- {
		b.WriteString(sfSentences[rng.Intn(len(sfSentences))])
	}
	switch i % 6 {
	case 1:
		b.WriteString("\n\n```go\nfunc render(blocks []Block, width float32) float32 {\n\tvar h float32\n\tfor _, b := range blocks {\n\t\th += b.Measure(width)\n\t}\n\treturn h\n}\n```\n")
	case 3:
		b.WriteString("\n\n- 第一项：**拆块**\n- 第二项：`缓存` 排版结果\n- 第三项：只画可见部分 [说明](https://example.com)\n")
	case 5:
		b.WriteString("\n\n## 小结\n\n1. 先量\n2. 再改\n3. 再量\n")
	}
	return b.String()
}

// sfReplyText 生成流式回复：段落、列表、代码块、行内格式混着。
func sfReplyText(rng *rand.Rand, n int) string {
	var b strings.Builder
	k := 0
	for len([]rune(b.String())) < n {
		switch k % 5 {
		case 0, 1:
			for j := 0; j < 3; j++ {
				b.WriteString(sfSentences[rng.Intn(len(sfSentences))])
			}
			b.WriteString("\n\n")
		case 2:
			b.WriteString("```go\nfunc stream(ch <-chan string, out *Message) {\n\tfor s := range ch {\n\t\tout.Text += s\n\t\tout.Version++\n\t}\n}\n```\n\n")
		case 3:
			b.WriteString("- **要点一**：只重排尾部\n- **要点二**：`版本号` 决定缓存是否有效\n- 要点三：参考 [文档](https://example.com/doc)\n\n")
		default:
			b.WriteString("## 另一个角度\n\n")
			b.WriteString(sfSentences[rng.Intn(len(sfSentences))])
			b.WriteString("\n\n")
		}
		k++
	}
	return b.String()
}
