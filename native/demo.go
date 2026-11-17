package main

// 本地演示：发一句只加在这个窗口里，再回一条预先写好的流式回复（中文按字喂，看流式显示）；Mermaid 用一小块网页视图 + Pele 画。

import (
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

var demoReplies = []string{
	"好呀，我把刚才说的整理成一张图：\n\n```mermaid\nflowchart LR\n  A[QQ 收到消息] --> B{闸门：值不值得想一想}\n  B -- 值得 --> C[她想一想]\n  B -- 不值得 --> D[只记下来]\n  C --> E[回一句]\n  C --> F[不说话]\n```\n\n- **闸门**先挡一道，不是每句都回\n- 想好了也可以选择**不说**\n\n要不要我再画一张记忆那边的？",
	"这段是识图的预处理，长边缩到 1024：\n\n```ts\nexport async function prepareImage(bytes: Uint8Array) {\n  const img = sharp(bytes).rotate()\n  const { width = 0, height = 0 } = await img.metadata()\n  const scale = Math.min(1, 1024 / Math.max(width, height))\n  return img.resize(Math.round(width * scale)).jpeg({ quality: 85 }).toBuffer()\n}\n```\n\n| 模型 | 中位 | p95 |\n|---|--:|--:|\n| 旧提示词 | 12.9 秒 | 37.5 秒 |\n| 短提示词 | 3.3 秒 | 9.8 秒 |\n\n短提示词快了差不多四倍～",
	"今天天气很好，我们去公园散步，看到了很多鸫在树上唱歌。\n\n> 示例角色说：这个原型能不能用，要先看中文输入法和长列表顺不顺。\n\n1. 先打几句中文，看候选框跟不跟光标\n2. 再点「压测：加载到 1 万条」，往上猛滚\n3. 最后看任务管理器里一共占多少内存",
}

// send 把输入框里的话加进本窗口（不发出去），再流式回一条演示回复
func (a *app) send() {
	if !a.demoMode { // 演示回复只在演示数据里生成；正式发送走 localchat.go。
		return
	}
	text := strings.TrimSpace(a.input)
	a.input = ""
	now := time.Now().UnixMilli()
	// 演示消息是在窗口开着时到的：标 live，序号接在已有的最大序号后面，入场判定（arrive.go）才认它是新到的
	a.demoSeq = max(a.demoSeq, a.arr.seen)
	for _, m := range a.msgs {
		a.demoSeq = max(a.demoSeq, m.seq)
	}
	message := &msg{id: fmt.Sprintf("demo-me-%d", now), seq: a.demoSeq + 1, live: true, mine: true, text: text, blocks: segview.TextBlocks(text), at: now}
	message.links = linksOf(message)
	a.msgs = append(a.msgs, message)
	who, color := "她", ""
	if id, ok := strings.CutPrefix(a.sel, "local:"); ok {
		if ch, ok := a.charOf(id); ok {
			who, color = ch.Name, ch.Color
		}
	}
	reply := &msg{id: fmt.Sprintf("demo-re-%d", now), who: who, color: color, md: true, streaming: true, at: now, seq: a.demoSeq + 2, live: true}
	a.msgs = append(a.msgs, reply)
	src := []rune(demoReplies[a.demo%len(demoReplies)])
	// 压测时从文件读一段长回复、固定节奏吐字（每 30ms 4 个字，和 Flutter 版一样），不用随机
	file := os.Getenv("SUMIKA_STREAM_FILE")
	if file != "" {
		if b, err := os.ReadFile(file); err == nil {
			src = []rune(string(b))
		}
	}
	a.demo++
	go func() {
		t0 := time.Now()
		defer func() { a.streamSecs = time.Since(t0).Seconds() }()
		for i := 0; i < len(src); {
			if file != "" {
				i = min(len(src), i+4)
			} else {
				i = min(len(src), i+1+rand.IntN(3)) // 一次一到三个字，像模型吐 token
			}
			text := string(src[:i])
			if file != "" {
				time.Sleep(30 * time.Millisecond)
			} else {
				time.Sleep(time.Duration(25+rand.IntN(35)) * time.Millisecond)
			}
			stop := false
			blocks := segview.TextBlocks(text)
			links := linksOf(&msg{blocks: blocks})
			a.win.Update(func() {
				if !reply.streaming {
					stop = true
					return
				}
				reply.text, reply.blocks, reply.links = text, blocks, links
			})
			if stop {
				return
			}
		}
		a.win.Update(func() { reply.streaming = false })
	}()
}

// autoDemo 按 SUMIKA_DEMO 自动演示：等第一页到了，发一句（第 1 条演示回复带 Mermaid），说完以后按需切到最大的 QQ 群压测
func (a *app) autoDemo() {
	if a.auto == "" {
		return
	}
	go func() {
		for {
			time.Sleep(300 * time.Millisecond)
			ready := false
			a.do(func() { ready = len(a.msgs) > 0 && !a.loading })
			if ready {
				break
			}
		}
		if a.auto == "stream" {
			time.Sleep(time.Second)
			a.streamTest()
			return
		}
		if a.auto == "open" { // 只打开 SUMIKA_CONV 那个会话、停在最新的地方（界面对比截图用）
			a.win.Update(func() { a.rail = 1; a.open(os.Getenv("SUMIKA_CONV")) })
			return
		}
		if a.auto == "switch" || a.auto == "settings" {
			time.Sleep(time.Second)
			a.switchTest(a.auto == "settings")
			return
		}
		if a.auto != "scroll" && a.auto != "jump" {
			a.win.Update(func() { a.input = "画一张图给我看看闸门是怎么挡的"; a.send() })
		}
		if a.auto != "stress" && a.auto != "scroll" && a.auto != "jump" {
			return
		}
		for a.auto == "stress" {
			time.Sleep(500 * time.Millisecond)
			done := false
			a.do(func() { done = !a.msgs[len(a.msgs)-1].streaming })
			if done {
				break
			}
		}
		time.Sleep(3 * time.Second)
		a.win.Update(func() {
			if len(a.convs) > 0 {
				a.rail = 1
				conv := a.convs[0].Key.Conversation
				if c := os.Getenv("SUMIKA_CONV"); c != "" { // 压测对比时指定同一个会话（列表第一个会随新消息换）
					conv = c
				}
				a.open(conv)
			}
		})
		for {
			time.Sleep(500 * time.Millisecond)
			ready := false
			a.do(func() { ready = len(a.msgs) > 0 && !a.loading })
			if ready {
				break
			}
		}
		// stress 在"正在加载"时会直接返回、什么都不做；2026-10-24 压测第 1 遍就这样只滚了最初的 200 条（没复现出是谁占着）。
		// 所以叫完以后核对真加载到了没有，没有就补叫，并往标准错误写一行，下次出现时留下证据
		start := 0
		a.do(func() { start = len(a.msgs) })
		for try := 1; try <= 5; try++ {
			a.win.Update(func() { a.stress(10000) })
			time.Sleep(2 * time.Second)
			done, n := false, 0
			for !done {
				time.Sleep(500 * time.Millisecond)
				a.do(func() { done, n = !a.loading, len(a.msgs) })
			}
			if n >= 10000 || n > start { // 会话可能本来就没有 1 万条：加载到了比开头多就算跑过
				break
			}
			log.Printf("自动压测：第 %d 次叫加载到 1 万条后只有 %d 条，再叫一次", try, n)
		}
		if a.auto == "jump" {
			time.Sleep(2 * time.Second)
			for {
				time.Sleep(500 * time.Millisecond)
				ready := false
				a.do(func() { ready = !a.loading })
				if ready {
					break
				}
			}
			a.jumpTest()
			return
		}
		if a.auto != "scroll" {
			return
		}
		// 等压测加载完，再从底一路滚到顶（像人猛滚），每一步都让它画一帧
		time.Sleep(2 * time.Second)
		for {
			time.Sleep(500 * time.Millisecond)
			ready := false
			a.do(func() { ready = !a.loading })
			if ready {
				break
			}
		}
		n := 0
		a.do(func() { n = len(a.msgs) })
		t0 := time.Now()
		maxBuilt, maxMiss, prev := 0, 0, -1
		for row := n - 1; row >= 0; row -= 6 {
			r := row
			a.do(func() {
				first, last := a.list.Visible()
				if last >= first {
					if built := last - first + 1; built > maxBuilt {
						maxBuilt = built
					}
				}
				if prev >= 0 {
					d := first - prev
					if d < 0 {
						d = -d
					}
					if d > maxMiss {
						maxMiss = d
					}
				}
				a.list.ScrollTo(r, ui.Start)
				prev = r
			})
			time.Sleep(16 * time.Millisecond)
		}
		cache := 0
		a.do(func() {
			if s := a.is(); s != nil {
				cache = s.bytes
			}
			a.status = fmt.Sprintf("自动滚完 %d 条，用了 %.1f 秒", n, time.Since(t0).Seconds())
		})
		if f := os.Getenv("SUMIKA_SCROLLDONE"); f != "" {
			_ = os.WriteFile(f, []byte(fmt.Sprintf("%d %.2f built=%d anchor=%d cache=%d", n, time.Since(t0).Seconds(), maxBuilt, maxMiss, cache)), 0o644)
		}
		if f := os.Getenv("SUMIKA_HEAP"); f != "" { // 滚完写一份堆剖析：go tool pprof -top sumika-native.exe <文件>
			time.Sleep(2 * time.Second)
			runtime.GC()
			if out, err := os.Create(f); err == nil {
				_ = pprof.WriteHeapProfile(out)
				out.Close()
			}
		}
	}()
}

// do 在主线程上跑 fn 并等它跑完（Window.Update 不等，跑完之前读状态会读到旧的）
func (a *app) do(fn func()) {
	done := make(chan struct{})
	a.win.Update(func() { fn(); close(done) })
	<-done
}

// switchTest 切换压测（规格-界面对比第 8、9 项）：settings 为假时在 QQ 会话列表前 5 个之间来回切 4 遍，为真时把设置的每一页挨个切 2 遍；
// 每次记"改了选中的那一项 → 数据读完（切会话要等它读第一页）→ 界面线程再转一圈"的毫秒数，写进 SUMIKA_SCROLLDONE：条数、总秒数、每次的毫秒
func (a *app) switchTest(settings bool) {
	var steps []func()
	if settings {
		a.do(func() { a.rail = 4 })
		time.Sleep(time.Second)
		for r := 0; r < 2; r++ {
			for i := range settingsTabs {
				steps = append(steps, func() { a.set.tab = i })
			}
		}
	} else {
		var keys []string
		a.do(func() {
			a.rail = 1
			for _, c := range a.convs[:min(5, len(a.convs))] {
				keys = append(keys, c.Key.Conversation)
			}
		})
		for r := 0; r < 4; r++ {
			for _, k := range keys {
				steps = append(steps, func() { a.open(k) })
			}
		}
	}
	var ms []string
	t00 := time.Now()
	for i, step := range steps {
		// 标记写进标准错误：MYGO_FRAME_STATS 的每帧耗时也写在那里、按先后排，量的脚本取标记后面第一帧的耗时（切过去以后画新内容花了多久）；
		// 下面记的毫秒只到"数据读完、界面线程转一圈"，不含画
		log.Printf("bench-switch %d", i)
		t0 := time.Now()
		a.do(step)
		for {
			done := false
			a.do(func() { done = !a.loading })
			if done {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		a.do(func() {})
		ms = append(ms, fmt.Sprintf("%.1f", float64(time.Since(t0).Microseconds())/1000))
		time.Sleep(300 * time.Millisecond)
	}
	if f := os.Getenv("SUMIKA_SCROLLDONE"); f != "" {
		_ = os.WriteFile(f, []byte(fmt.Sprintf("%d %.2f %s", len(ms), time.Since(t00).Seconds(), strings.Join(ms, " "))), 0o644)
	}
}

// jumpTest 模拟拖滚动条：同一个种子的 50 个位置比例（和 Flutter 版同一个公式），每次跳到 比例 × 总行数 那一行，跳完等 150ms
func (a *app) jumpTest() {
	n := 0
	a.do(func() { n = len(a.msgs) })
	x := int64(42)
	t0 := time.Now()
	for i := 0; i < 50; i++ {
		x = (x*1103515245 + 12345) % 2147483648
		row := int(float64(x) / 2147483648 * float64(n))
		a.do(func() { a.list.ScrollTo(min(row, n-1), ui.Start) })
		time.Sleep(150 * time.Millisecond)
	}
	secs := time.Since(t0).Seconds()
	a.do(func() { a.status = fmt.Sprintf("跳完 50 次（%d 条），用了 %.1f 秒", n, secs) })
	if f := os.Getenv("SUMIKA_SCROLLDONE"); f != "" {
		_ = os.WriteFile(f, []byte(fmt.Sprintf("%d %.2f", n, secs)), 0o644)
	}
}

// streamTest 流式压测：在当前（本地）会话里发一句，等这条长回复吐完，记一笔
func (a *app) streamTest() {
	var reply *msg
	a.do(func() {
		a.input = "把识图慢的事完整讲一遍"
		a.send()
		reply = a.msgs[len(a.msgs)-1]
	})
	for {
		time.Sleep(300 * time.Millisecond)
		done := false
		a.do(func() { done = !reply.streaming })
		if done {
			break
		}
	}
	time.Sleep(time.Second)
	n := 0
	a.do(func() { n = len([]rune(reply.text)) })
	if f := os.Getenv("SUMIKA_SCROLLDONE"); f != "" {
		_ = os.WriteFile(f, []byte(fmt.Sprintf("%d %.2f", n, a.streamSecs)), 0o644)
	}
}
