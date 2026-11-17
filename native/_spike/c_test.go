package spike

// 第零步 c 的富文本 spike，按规格第十节 c 的做法重建（原来的跑完删掉了）：
// 10 000 行、每行 2 个小黄脸，从底到顶滚一遍（每步 ScrollTo 往上 6 行，共 1667 步），窗口 1640×900，Tester，开着"减少动态效果"。
// 数据全是合成的、名字全是编的；不连 7720，不读 data/。
//
// 三种做法（环境变量 SPIKE_VARIANT 选，一次一种，每次新起进程）：
//   P1  chat.Bubble + ui.Text（原型现在的做法，纯文字基线）
//   E2  自写壳 + ui.RichText + 2 个行内框 + Draw 画图；框的位置在 Draw 里第一次画到这一行时现排（原来的 E2）
//   E3  同 E2，但冷遍开始前先用 ShapeRichText 把 10 000 行的行内框位置全部预排好，存在我们自己的行缓存里；
//       mygo 自己的缓存不动（ShapeRichText 走 System.Shape，源码注释 "caches nothing"）。冷遍里 Draw 只查我们的缓存
//
// 跑法见 run.sh；输出一行 JSON 风格的数，由 run.sh 汇总。

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/chat"
	"github.com/ELY-Open-Source/CrychicUI/kit"
	"github.com/egoist/mygo/ui"
	"golang.org/x/sys/windows"
)

const (
	rows     = 10000
	stepRows = 6
	faceSize = 19
)

// 行的内容：编的文字，长短不一，两个脸插在 1/3 和 2/3 处
type row struct {
	a, b, c string
	mine    bool
	// 我们自己的行缓存（E2 / E3 用）
	objs   []ui.TextObject
	shaped float32 // 排版时用的宽度
	layW   float32 // 排出来的版面宽度（Size().W）
	wrap   bool    // 排出来的版面不止一行
}

var pieces = []string{
	"今天天气很好", "我们去公园散步", "看到了很多鸫在树上唱歌", "晚上想吃火锅还是烧烤", "这个原型能不能用",
	"要先看中文输入法和长列表顺不顺", "刚才那张图是谁发的", "哈哈哈哈哈哈哈哈", "好的收到", "我再想想怎么回",
	"周末有空的话一起去看展", "路上有点堵可能要晚十分钟", "把文件发我一下", "已经改好了你再看看",
}

func makeRows() []*row {
	out := make([]*row, rows)
	for i := range out {
		var parts []string
		n := 1 + i%5 // 1 到 5 句，长短不一
		for k := 0; k < n; k++ {
			parts = append(parts, pieces[(i*7+k*3)%len(pieces)])
		}
		s := strings.Join(parts, "，")
		r := []rune(s)
		x, y := len(r)/3, len(r)*2/3
		out[i] = &row{a: string(r[:x]), b: string(r[x:y]), c: string(r[y:]), mine: i%3 == 0}
	}
	return out
}

func (r *row) spans() []ui.Span {
	obj := ui.Span{Text: string(rune(0xFFFC)), ObjectWidth: faceSize, ObjectHeight: faceSize}
	return []ui.Span{{Text: r.a}, obj, {Text: r.b}, obj, {Text: r.c}}
}

// shell 是 chat/helpers.go 里 shell() 逐值抄的副本（它未导出）
func shell(c *ui.Context, mine bool) ui.Element {
	t := c.Theme()
	rad := t.Radius * 2
	b := ui.Column(c).MaxWidth(t.Space(80)).Padding(t.Space(2), t.Space(3)).Radius(rad).Gap(t.Space(1))
	if mine {
		return b.AlignSelf(ui.End).Background(t.Accent).TextColor(t.AccentText)
	}
	return b.AlignSelf(ui.Start).Background(t.Surface).Border(1, t.Border)
}

var (
	bodyW   atomic.Uint32 // 正文区的可用宽度（float32 的位）：气泡最大宽度减左右内边距，在视图里从主题取
	oneLine float32       // 一行（含脸）的版面高度，判断"换没换行"用
)

// border 是气泡边框占的宽度：别人的气泡有 1 DIP 的边框（左右共 2），我的没有，正文区因此窄 2 DIP
func (r *row) border() float32 {
	if r.mine {
		return 0
	}
	return 2
}

// valid 报告缓存的框位置能不能用在宽度为 w 的元素上
func (r *row) valid(w float32) bool {
	if r.objs == nil {
		return false
	}
	if r.wrap {
		return math.Abs(float64(w-r.shaped)) < 0.5
	}
	return math.Abs(float64(w-r.layW)) < 0.5
}

// shape 按宽度 w 排一次版，把框的位置存在这一行上
func (r *row) shape(spans []ui.Span, f ui.Font, w float32) {
	l := ui.ShapeRichText(spans, f, w)
	lw, lh := l.Size()
	r.objs, r.shaped, r.layW, r.wrap = l.Objects(), w, lw, lh > oneLine+1
}

var (
	drawShaped atomic.Int64 // Draw 里现排的次数
	drawHit    atomic.Int64 // Draw 里直接用缓存的次数
	themeFont  atomic.Pointer[ui.Font]

	// 缓存校验（SPIKE_CHECK=1 时才开，开着不计时、不输出 RESULT）：Draw 命中缓存时，按元素实际宽度 rc.W 再排一次，
	// 和缓存里的框位置逐项比。这才是在测"缓存可用的条件"：缓存是按排版宽 shaped 排的，元素实际宽度 rc.W 是 mygo 布局给的。
	checkOn     = os.Getenv("SPIKE_CHECK") == "1"
	checkWrap   atomic.Int64 // 比过的次数：换行的行
	checkSingle atomic.Int64 // 比过的次数：单行的行
	checkBad    atomic.Int64 // 对不上的次数
)

// sameObjs 逐项比两份框位置
func sameObjs(a, b []ui.TextObject) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestC_ScrollBench(t *testing.T) {
	variant := os.Getenv("SPIKE_VARIANT")
	if variant == "" {
		t.Skip("没设 SPIKE_VARIANT")
	}
	data := makeRows()
	var state ui.ListState
	state.FollowEnd = true
	state.Key = func(i int) any { return i }
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)

	view := func(c *ui.Context) {
		th := c.Theme()
		f := ui.Font{Size: th.FontSize}
		themeFont.Store(&f)
		bodyW.Store(math.Float32bits(th.Space(80) - 2*th.Space(3)))
		ui.Column(c).Grow(1).Children(func() {
			ui.List(c, &state, rows, func(i int) {
				r := data[i]
				switch variant {
				case "P1":
					chat.Bubble(c, chat.Message{Sender: "某人", Text: r.a + r.b + r.c, Mine: r.mine, At: now}, chat.BubbleOptions{})
				default:
					root := shell(c, r.mine)
					kit.Arrive(root, ui.Pose{Y: th.Space(2), Fade: 1}, 0) // 和 P1 一样带入场（减少动态效果开着时它不动）
					root.Children(func() {
						spans := r.spans()
						ui.RichText(c, spans...).Wrap().Draw(func(p *ui.Painter, rc ui.Rect) {
							// 缓存可用的条件：换行的行，元素宽等于排版时的宽（可用宽度）；单行的行，元素宽等于版面宽度（元素按内容收紧）
							if r.valid(rc.W) {
								drawHit.Add(1)
								if checkOn {
									if r.wrap {
										checkWrap.Add(1)
									} else {
										checkSingle.Add(1)
									}
									if !sameObjs(ui.ShapeRichText(spans, *themeFont.Load(), rc.W).Objects(), r.objs) {
										checkBad.Add(1)
									}
								}
							} else {
								r.shape(spans, *themeFont.Load(), rc.W)
								drawShaped.Add(1)
							}
							for _, o := range r.objs {
								p.Fill(ui.Rect{X: rc.X + o.Rect.X, Y: rc.Y + o.Rect.Y, W: o.Rect.W, H: o.Rect.H}, th.Accent, 4)
							}
						})
					})
				}
			}).Grow(1).Padding(th.Space(4), th.Space(5)).Justify(ui.End)
		})
	}

	tt := ui.NewTester(view, 1640, 900)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	{ // 一行（含两个脸）的高度：用一条短句量
		l := ui.ShapeRichText((&row{a: "好", b: "的", c: "收到"}).spans(), *themeFont.Load(), math.Float32frombits(bodyW.Load()))
		_, oneLine = l.Size()
	}

	if variant == "E3" {
		// 预排：模拟取页时在后台预排（这里直接在本线程做，不计入冷遍）。宽度取气泡正文区的最大宽度
		f := *themeFont.Load()
		t0 := time.Now()
		w := bodyW.Load()
		for _, r := range data {
			r.shape(r.spans(), f, math.Float32frombits(w)-r.border())
		}
		t.Logf("E3 预排 %d 行用了 %.0f ms（宽 %.1f DIP，字号 %.1f，单行高 %.1f），不计入冷遍", rows, float64(time.Since(t0).Milliseconds()), math.Float32frombits(w), f.Size, oneLine)
		drawShaped.Store(0)
		drawHit.Store(0)
		checkWrap.Store(0)
		checkSingle.Store(0)
		checkBad.Store(0)
	}

	scrollEnd := func() {
		state.ScrollTo(rows-1, ui.End)
		for i := 0; i < 5; i++ {
			tt.Frame()
		}
	}
	scrollEnd()

	pass := func(name string) {
		steps := 0
		var ms []float64
		var over int
		heap0 := heapMB()
		cpu0 := cpuSeconds()
		t0 := time.Now()
		for row := rows - 1; row >= 0; row -= stepRows {
			s := time.Now()
			state.ScrollTo(row, ui.Start)
			tt.Frame()
			d := float64(time.Since(s).Microseconds()) / 1000
			ms = append(ms, d)
			if d > 16.7 {
				over++
			}
			steps++
		}
		total := time.Since(t0)
		cpu := cpuSeconds() - cpu0
		sorted := slices.Clone(ms)
		slices.Sort(sorted)
		sum := 0.0
		for _, v := range ms {
			sum += v
		}
		q := func(p float64) float64 {
			return sorted[int(math.Min(float64(len(sorted)-1), math.Ceil(p*float64(len(sorted)))-1))]
		}
		if checkOn {
			return // 校验开着时每次 Draw 多排一遍，耗时没有意义
		}
		fmt.Printf("RESULT variant=%s pass=%s steps=%d mean=%.3f median=%.3f p95=%.3f p99=%.3f max=%.1f over16.7=%d heapDelta=%.1fMB total=%dms cpuPerFrame=%.3f drawShaped=%d drawHit=%d\n",
			variant, name, steps, sum/float64(len(ms)), q(0.5), q(0.95), q(0.99), sorted[len(sorted)-1], over, heapMB()-heap0, total.Milliseconds(), cpu*1000/float64(steps),
			drawShaped.Load(), drawHit.Load())
	}
	pass("cold")
	scrollEnd()
	pass("hot")

	if checkOn && variant != "P1" {
		fmt.Printf("CHECK variant=%s 命中缓存后按元素实际宽度现排对比：共 %d 次（换行的行 %d，单行的行 %d），对不上 %d 次；Draw 现排 %d 次，命中 %d 次\n",
			variant, checkWrap.Load()+checkSingle.Load(), checkWrap.Load(), checkSingle.Load(), checkBad.Load(), drawShaped.Load(), drawHit.Load())
	}
}

func heapMB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / 1e6
}

// filetimeSum 把内核态和用户态两个 FILETIME（高低各 32 位，单位 100 纳秒）加起来。
// 注意 Go 里 | 和 + 同级、从左往右算，每个 FILETIME 要先单独拼成一个数再相加。
// out/ 里 6 轮（r1 到 r6）的 cpuPerFrame 是旧写法 a<<32|b+c<<32|d 算的（算成了 ((a<<32|b)+(c<<32))|d），全部作废；
// review-c3/ 出自不在仓库里的另一版，算式查不到，一并作废。它们的耗时（均值、p95 等）不受影响。修正后重跑的在 out/cpu-fix/。
func filetimeSum(k, u windows.Filetime) uint64 {
	return (uint64(k.HighDateTime)<<32 | uint64(k.LowDateTime)) + (uint64(u.HighDateTime)<<32 | uint64(u.LowDateTime))
}

// cpuSeconds 是进程的 CPU 时间（用户态加内核态，含软件光栅的多线程）
func cpuSeconds() float64 {
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &c, &e, &k, &u); err != nil {
		return 0
	}
	return float64(filetimeSum(k, u)) / 1e7
}
