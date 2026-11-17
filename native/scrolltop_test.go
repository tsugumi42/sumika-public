package main

// 滚到顶自动加载的测试（scrolltop.go）：触发的条件、取回来以后正在看的那一行不动（量跳变像素）、翻到头的字、失败后停下。
// 数据读演示数据（编的）：800 条的会话 qq:group:1002，先载最新的 50 条，更早的在后台。

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

// topEnv 一个读演示数据的 app，只加载了会话最新的 n 条；回界面线程的活进队列，由 drive 取出来执行
type topEnv struct {
	a     *app
	tt    *ui.Tester
	p     *probes
	queue chan func()
}

func newTopEnv(t *testing.T, conv string, n int) *topEnv {
	t.Helper()
	e := &topEnv{queue: make(chan func(), 64)}
	a := &app{recalled: map[string]bool{}, speakers: map[string]bool{}, started: time.Now(), demoMode: true}
	a.imgFetch = func(context.Context, api.MediaRef, int) ([]byte, error) { return nil, errNotFound }
	a.postFn = func(f func()) { e.queue <- f }
	a.reader = demoReader{}
	k := demoKey(conv)
	a.keys = map[string]api.ConversationKey{conv: k}
	a.sel, a.title, a.group, a.list = conv, "测试", true, a.newList()
	a.firstDone = true
	res, err := a.reader.ReadMessages(context.Background(), api.ConversationRequest{Key: k, Limit: n}, "")
	if err != nil || !res.Value.HasMore {
		t.Fatalf("演示数据读不出一页或没有更早的：%v %+v", err, res.State)
	}
	page := a.build(k, res.Value.Items, charOf)
	a.takePage(page.msgs, page.gone, false)
	a.cursor, a.hasMore = res.Value.NextCursor, res.Value.HasMore
	a.topPrev = prevOf(k, res.Value.Before, charOf)
	e.a = a
	e.tt, e.p = chatTester(a, 900, 700)
	return e
}

// drive 一直画帧、做回界面线程的活，直到 cond 成立
func (e *topEnv) drive(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等了 20 秒还没有：%s（msgs=%d loading=%v hasMore=%v status=%q）", what, len(e.a.msgs), e.a.loading, e.a.hasMore, e.a.status)
		}
		e.tt.Frame()
		for {
			select {
			case f := <-e.queue:
				f()
				continue
			default:
			}
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// frames 画 n 帧并做完排着的活，不等任何条件
func (e *topEnv) frames(n int) {
	for range n {
		e.tt.Frame()
		for {
			select {
			case f := <-e.queue:
				f()
				continue
			default:
			}
			break
		}
	}
}

// wheelUpTo 像人一样用滚轮往上滚，直到可见的第一行不大于 want（want ≤ 0 就滚到最顶）。用滚轮不用 ScrollTo：
// ScrollTo 的请求会一直等着“那一行量出来”，前面加了行以后它会再把列表拉回第 want 行，不是人会做的事
func (e *topEnv) wheelUpTo(t *testing.T, want int) {
	t.Helper()
	dir := float32(1)
	for i := 0; i < 600; i++ {
		first, _ := e.a.list.Visible()
		if first <= want {
			if want <= 0 { // 第 0 行只露出一部分时再推几下，真正到最顶
				for range 3 {
					e.tt.Scroll(450, 350, 0, dir*240)
					e.frames(2)
				}
			}
			return
		}
		e.tt.Scroll(450, 350, 0, dir*240)
		e.frames(2)
		if i == 2 { // 三下以后第一行没往上走，说明方向反了
			if f2, _ := e.a.list.Visible(); f2 >= first {
				dir = -1
			}
		}
	}
	first, last := e.a.list.Visible()
	t.Fatalf("滚了 600 下还没到第 %d 行（可见 %d～%d）", want, first, last)
}

// shellY 这一行的气泡壳在窗口里的纵坐标
func (e *topEnv) shellY(t *testing.T, id string) float32 {
	t.Helper()
	return e.p.box(t, "shell:"+id+":0").Y
}

const topConv = "qq:group:1002" // 800 条

// 在底部（最新）不取；滚到接近顶部自动取一页（200 条）；取回来以后离开顶部，不再接着取
func TestScrollTopAutoLoadsOnePage(t *testing.T) {
	e := newTopEnv(t, topConv, 50)
	e.frames(6)
	if len(e.a.msgs) != 50 || e.a.loading {
		t.Fatalf("在最新一条时不该取：%d 条 loading=%v", len(e.a.msgs), e.a.loading)
	}
	e.a.olderOff = true // 先挡住自动取，滚到接近顶部再放开
	e.wheelUpTo(t, olderAhead-5)
	e.a.olderOff = false
	e.drive(t, "自动取回一页", func() bool { return len(e.a.msgs) == 250 && !e.a.loading })
	e.frames(4)
	first, _ := e.a.list.Visible()
	if first <= olderAhead {
		t.Fatalf("取回来以后应离开顶部（第一行 %d），否则会接着取", first)
	}
	e.frames(10)
	if len(e.a.msgs) != 250 {
		t.Fatalf("离开顶部以后不该再取：%d 条", len(e.a.msgs))
	}
	t.Logf("取回一页：50 → %d 条，可见第一行 %d", len(e.a.msgs), first)
}

// 正在看的那一行不动：从接近顶部（第 15 行起）取回来，前三行的气泡位置前后差多少像素。
// 另量最坏的一种：已经在最顶上（第 0 行可见）时取回来
func TestScrollTopKeepsAnchor(t *testing.T) {
	for _, at := range []int{15, 0} {
		t.Run(fmt.Sprintf("从第%d行起", at), func(t *testing.T) {
			e := newTopEnv(t, topConv, 50)
			e.frames(3)
			e.a.olderOff = true // 先挡住自动取，用滚轮滚到位置、记下位置，再放开
			e.wheelUpTo(t, at)
			e.frames(4)
			first, last := e.a.list.Visible()
			if first > at+1 {
				t.Fatalf("起点不对：想让第 %d 行在最上面，实际可见 %d～%d", at, first, last)
			}
			// 取窗口里看得见、有气泡壳的前三行（有的行没有壳，比如只有图）
			var ids []string
			before := map[string]float32{}
			for i := first; i <= last && len(ids) < 3; i++ {
				id := e.a.msgs[i].id
				if h := e.p.hs["shell:"+id+":0"]; h == nil {
					continue
				}
				if b := e.p.box(t, "shell:"+id+":0"); b.Y >= 0 && b.Y+b.H <= 700 {
					ids = append(ids, id)
					before[id] = b.Y
				}
			}
			if len(ids) < 2 {
				first, last = e.a.list.Visible()
				t.Fatalf("窗口里找不到两行以上有气泡壳的消息（可见 %d～%d，找到 %v）", first, last, ids)
			}
			e.a.olderOff = false
			e.drive(t, "取回一页", func() bool { return len(e.a.msgs) == 250 && !e.a.loading })
			e.frames(4)
			worst := float32(0)
			for _, id := range ids {
				d := e.shellY(t, id) - before[id]
				t.Logf("%s：前 %.1f 后 %.1f 差 %+.2f", id, before[id], e.shellY(t, id), d)
				if float32(math.Abs(float64(d))) > worst {
					worst = float32(math.Abs(float64(d)))
				}
			}
			// 两个起点都一点不动：已经在最顶上（第 0 行可见）时，原来的第 0 行带日期分隔、名字和组首上边距，
			// 靠后台给的"再往前的那条消息"（a.topPrev）判断，取回前面的页以后这些不会凭空消失
			if worst >= 1 {
				t.Fatalf("取回一页以后正在看的行动了 %.2f 像素（要 < 1）", worst)
			}
		})
	}
}

// 翻到头：一直滚到顶、自动取，直到本地没有更早的；标题栏写明，按钮灰掉，不再取
func TestScrollTopReachesLocalEnd(t *testing.T) {
	e := newTopEnv(t, topConv, 50)
	e.frames(3)
	if e.tt.HasText("本地没有更早的了") {
		t.Fatal("还有更早的，不该写翻到头了")
	}
	for range 10 {
		if !e.a.hasMore {
			break
		}
		e.a.olderOff = true
		e.wheelUpTo(t, 0)
		e.a.olderOff = false
		e.drive(t, "这一页取完", func() bool { return !e.a.loading })
		e.frames(3)
	}
	if e.a.hasMore || len(e.a.msgs) != 800 {
		t.Fatalf("应翻到头（800 条）：%d 条 hasMore=%v", len(e.a.msgs), e.a.hasMore)
	}
	e.frames(3)
	if !e.tt.HasText("本地没有更早的了") {
		t.Fatalf("翻到头了标题栏应写明：%q", e.tt.Texts())
	}
	e.a.list.ScrollTo(0, ui.Start)
	e.frames(8)
	if len(e.a.msgs) != 800 || e.a.loading {
		t.Fatalf("翻到头以后不再取：%d 条 loading=%v", len(e.a.msgs), e.a.loading)
	}
}

type failingOlder struct{ demoReader }

func (failingOlder) ReadMessages(context.Context, api.ConversationRequest, api.Cursor) (api.Result[api.MessagePage], error) {
	return api.Result[api.MessagePage]{}, errors.New("编的取页失败")
}

// 自动取失败：写明失败、停下不再自动取（同一个位置不停重试只会刷屏）；点按钮（兜底）再试一次
func TestScrollTopFailureStopsAutoLoad(t *testing.T) {
	e := newTopEnv(t, topConv, 50)
	reads := 0
	e.a.reader = countingOlder{failingOlder{}, &reads}
	e.a.list.ScrollTo(0, ui.Start)
	e.drive(t, "失败写进状态栏", func() bool { return e.a.status != "" && !e.a.loading })
	if !e.a.olderOff || reads != 1 {
		t.Fatalf("失败后应停下：olderOff=%v 读了 %d 次", e.a.olderOff, reads)
	}
	e.frames(20)
	if reads != 1 {
		t.Fatalf("停下以后不该再自动取，读了 %d 次", reads)
	}
	// 兜底的按钮：点一下清掉停下的标记并再试
	if err := e.tt.Click("加载更早的消息"); err != nil {
		t.Fatal(err)
	}
	e.drive(t, "再试一次", func() bool { return reads == 2 && !e.a.loading })
	if !e.a.olderOff {
		t.Fatal("手动试又失败，应再停下")
	}
}

type countingOlder struct {
	failingOlder
	n *int
}

func (c countingOlder) ReadMessages(ctx context.Context, r api.ConversationRequest, cur api.Cursor) (api.Result[api.MessagePage], error) {
	*c.n++
	return c.failingOlder.ReadMessages(ctx, r, cur)
}

// 点引用找原消息（跳转）在翻页时，自动取不插手；换会话以后重新算
func TestScrollTopYieldsToJumpAndResets(t *testing.T) {
	e := newTopEnv(t, topConv, 50)
	e.a.jump = jumpState{id: "不存在的", left: 3}
	e.a.list.ScrollTo(0, ui.Start)
	e.frames(8)
	if e.a.loading || len(e.a.msgs) != 50 {
		t.Fatalf("跳转在找的时候自动取不该插手：loading=%v %d 条", e.a.loading, len(e.a.msgs))
	}
	e.a.olderOff = true
	e.a.switchTo("qq:group:1003")
	if e.a.olderOff {
		t.Fatal("换会话应清掉停下的标记")
	}
}

// 列表还没排过版时 Visible 回 (0, -1)，"第 0 行"不是真的在顶上：不能因此就取
func TestScrollTopIgnoresUnlaidList(t *testing.T) {
	e := newTopEnv(t, topConv, 50)
	e.a.list = e.a.newList()      // 全新的列表，没排过版
	e.a.olderTop = e.a.msgs[0].id // 不是"刚加了行"的那一帧
	e.a.loadOlderNearTop()
	for {
		select {
		case f := <-e.queue:
			f()
			continue
		default:
		}
		break
	}
	if e.a.loading || len(e.a.msgs) != 50 {
		t.Fatalf("没排过版的列表不该触发取页：loading=%v %d 条", e.a.loading, len(e.a.msgs))
	}
}

// 最老那一行的日期分隔、组首由后台给的"再往前的那条消息"（topPrev）定：前面是前一天的人，这一行就带日期分隔；
// 前面是同一天同一个人的话，就没有。取回前面的页以后这一行的样子不变（见 TestScrollTopKeepsAnchor），靠的就是前后算出来一致
func TestTopRowFollowsPrevMessage(t *testing.T) {
	e := newTopEnv(t, topConv, 50)
	e.frames(3)
	e.a.olderOff = true
	e.wheelUpTo(t, 0)
	e.frames(4)
	first := e.a.msgs[0]
	day := time.UnixMilli(first.at).Format("1 月 2 日")
	// 前一天别人说的：第 0 行是新的一天、一组的第一条，带日期分隔
	e.a.topPrev = &msg{id: "prev", actor: first.actor + "x", at: first.at - 36*3600_000}
	e.frames(4)
	if !e.tt.HasText(day) {
		t.Fatalf("前面是前一天的消息，第 0 行应带日期分隔 %q：%q", day, e.tt.Texts())
	}
	// 同一天、同一个人、几秒前：同一组，第 0 行没有日期分隔
	e.a.topPrev = &msg{id: "prev", actor: first.actor, mine: first.mine, at: first.at - 3000}
	e.frames(4)
	if e.tt.HasText(day) {
		t.Fatalf("前面是同一天同一个人几秒前说的，第 0 行不该有日期分隔 %q", day)
	}
	// 时间倒过来的（补录乱序）不信：当作前面没有，仍是第一行
	e.a.topPrev = &msg{id: "prev", actor: first.actor, mine: first.mine, at: first.at + 3000}
	e.frames(4)
	if !e.tt.HasText(day) {
		t.Fatalf("前面那条比第 0 行还晚，不该采信，第 0 行仍带日期分隔 %q", day)
	}
}
