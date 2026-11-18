package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

// TestScaleFrames 是第七步的十倍验收：同一份数据，旧界面和新界面各跑一遍。
// 数据是十个角色轮流说话的一万条消息，长短不一，夹着多行。
// 它要配合 MYGO_FRAME_STATS=all 使用：每一帧的 build、layout、paint、present 耗时由 mygo 写到标准错误，
// 本测试只在中间插分段标记，再由 scratchpad 里的脚本按标记分段算 p95。
// 没设 SUMIKA_S7_FRAMES 就跳过，日常短测不跑。
func TestScaleFrames(t *testing.T) {
	order := os.Getenv("SUMIKA_S7_FRAMES")
	if order == "" {
		t.Skip("SUMIKA_S7_FRAMES 没设")
	}
	tag := os.Getenv("SUMIKA_S7_RUN")
	// SUMIKA_S7_MD=1：她的回复（非 mine）按 Markdown 画，并夹一些 Markdown 语法，测接上 Markdown 后的帧耗时。
	withMD := os.Getenv("SUMIKA_S7_MD") == "1"
	const n = 10000
	names := []string{"甲", "乙", "丙", "丁", "戊", "己", "庚", "辛", "壬", "癸"}
	rng := rand.New(rand.NewSource(7))
	pieces := []string{"今天的进度", "这一段我没看懂", "可以再试一次", "先别合并", "图在上面那条", "收到", "好的", "等我一下", "这个参数是 3425", "再看看日志里的时间戳"}
	msgs := make([]*msg, n)
	who := 0
	for i := range n {
		if i%(1+rng.Intn(4)) == 0 {
			who = rng.Intn(len(names))
		}
		var b strings.Builder
		fmt.Fprintf(&b, "#%d ", i)
		for k := rng.Intn(6) + 1; k > 0; k-- {
			b.WriteString(pieces[rng.Intn(len(pieces))])
			b.WriteString("，")
		}
		if rng.Intn(20) == 0 {
			b.WriteString("\n第二行\n第三行")
		}
		if rng.Intn(50) == 0 {
			for k := 0; k < 12; k++ {
				b.WriteString(pieces[rng.Intn(len(pieces))])
			}
		}
		if withMD && i%37 != 0 {
			switch {
			case i%100 == 3:
				b.WriteString("\n\n```go\nfunc f() int {\n\treturn 42 // 这一行很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长很长\n}\n```")
			case i%40 == 7:
				b.WriteString("\n\n- 第一项\n- 第二项 [链接](https://example.com)\n- 第三项")
			case i%10 == 1:
				b.WriteString(" **加粗** 和 `行内代码`")
			}
		}
		text := b.String()
		msgs[i] = &msg{id: fmt.Sprintf("m%d", i), who: names[who], actor: "a" + names[who], mine: i%37 == 0, md: withMD && i%37 != 0, text: text, blocks: segview.TextBlocks(text), at: int64(i) * 60000}
	}
	views := map[string]func(*app, *ui.Context){
		"workbench": func(a *app, c *ui.Context) { a.applyLook(c); a.workbenchChat(c) },
		"legacy":    func(a *app, c *ui.Context) { a.applyLook(c); a.chatView(c) },
		// 整个新界面外壳（导航、会话列表、标题栏、右栏、聊天）：十个角色各一个本地私聊，三栏并排
		"shell": func(a *app, c *ui.Context) { a.applyLook(c); a.workbenchView(c, a.look.dark) },
	}
	for _, name := range strings.Split(order, ",") {
		view, ok := views[name]
		if !ok {
			t.Fatalf("没有这个视图 %q", name)
		}
		runtime.GC()
		var m0 runtime.MemStats
		runtime.ReadMemStats(&m0)
		a := &app{sel: "c", group: true, title: "测试群", msgs: append([]*msg(nil), msgs...)}
		a.workbench.shell.material = os.Getenv("SUMIKA_S7_MATERIAL") // 材质：liquid、blur、solid；Tester 里玻璃和模糊走 CPU，present 一栏会比真窗口慢很多
		a.list = a.newList()
		if name == "shell" {
			for _, nm := range names {
				a.local = append(a.local, conv{Key: demoKey("local:" + nm), Kind: "local", Title: nm, Last: lastLine{Text: "最后一句", CreatedAt: t0}, Observers: []string{"a" + nm}})
			}
			a.sel = "c"
			a.workbench.shell.st.Panes.SideOpen = true
		}
		tt := ui.NewTester(func(c *ui.Context) { view(a, c) }, 2048, 1030)
		tt.SetScale(1.25)
		mark := func(phase string) { log.Printf("S7 PHASE view=%s run=%s phase=%s", name, tag, phase) }

		mark("open")
		tt.Frame()
		a.list.ScrollToEnd()
		tt.Frame()
		tt.Frame()

		// 锚点跳变：滚一步前后，同一条可见消息的位置变化应该等于滚动量。
		// 取各步位移的中位数当预期，偏差最大的一步就是跳变。
		var shifts []float64
		step := func(dy float32) {
			first, last := a.list.Visible()
			if last < first {
				return
			}
			mid := (first + last) / 2
			text := a.msgs[mid].text
			before, ok := tt.Find(text)
			tt.Scroll(1000, 500, 0, dy)
			tt.Frame()
			after, ok2 := tt.Find(text)
			if ok && ok2 {
				shifts = append(shifts, float64(after.Y-before.Y))
			}
		}
		mark("wheel-from-end")
		for range 400 {
			step(-300)
		}
		a.list.ScrollTo(5000, ui.Start)
		tt.Frame()
		tt.Frame()
		mark("wheel-from-middle")
		for range 300 {
			step(-300)
		}
		maxJump, median := 0.0, 0.0
		if len(shifts) > 0 {
			sorted := append([]float64(nil), shifts...)
			sort.Float64s(sorted)
			median = sorted[len(sorted)/2]
			for _, s := range shifts {
				d := s - median
				if d < 0 {
					d = -d
				}
				if d > maxJump {
					maxJump = d
				}
			}
		}

		// 猛滚：像 SUMIKA_DEMO=scroll 那样，从底一路按行跳到顶，每步一帧。
		mark("sweep")
		maxBuilt, landing, settle := 0, 0, 0
		for row := n - 1; row >= 0; row -= 25 {
			a.list.ScrollTo(row, ui.Start)
			tt.Frame()
			first, last := a.list.Visible()
			if last >= first && last-first+1 > maxBuilt {
				maxBuilt = last - first + 1
			}
			d := first - row
			if d < 0 {
				d = -d
			}
			if d > landing {
				landing = d
			}
			tt.Frame()
			again, _ := a.list.Visible()
			d = again - first
			if d < 0 {
				d = -d
			}
			if d > settle {
				settle = d
			}
		}
		mark("end")

		runtime.GC()
		var m1 runtime.MemStats
		runtime.ReadMemStats(&m1)
		cache := 0
		if s := a.is(); s != nil {
			cache = s.bytes
		}
		// 元素数放在计时之后数：数元素要开无障碍树，会让帧变慢。
		elements := map[string]int{}
		for label, row := range map[string]int{"bottom": n - 1, "middle": 5000, "top": 0} {
			a.list.ScrollTo(row, ui.Start)
			tt.Frame()
			tt.Frame()
			elements[label] = countElements(t, tt)
		}
		log.Printf("S7 RESULT view=%s run=%s elements(bottom/middle/top)=%d/%d/%d built=%d anchorJump=%.2f scrollShift=%.1f landing=%d settle=%d imageCacheBytes=%d heapInUseDelta=%d",
			name, tag, elements["bottom"], elements["middle"], elements["top"], maxBuilt, maxJump, median, landing, settle, cache, int64(m1.HeapInuse)-int64(m0.HeapInuse))
	}
}
