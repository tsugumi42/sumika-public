package winshell

import (
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/remotepick"
)

// pickRig 是搜索下拉的测试台：假的取数（按词出结果、可卡住、可失败）、假的“回到界面线程”、虚拟时间
type pickRig struct {
	t       *testing.T
	k       *Kit
	tt      *ui.Tester
	st      *remotepick.State
	value   string
	picked  []remotepick.Candidate
	cleared int

	mu      sync.Mutex
	queue   []func()
	asked   []string
	results map[string][]remotepick.Candidate
	errs    map[string]error
	hold    map[string]chan struct{}
	gone    map[string]bool // 被取消的请求
}

func newPickRig(t *testing.T) *pickRig {
	r := &pickRig{t: t, k: NewKit(chatui.DefaultSettings(), ""), results: map[string][]remotepick.Candidate{}, errs: map[string]error{}, hold: map[string]chan struct{}{}, gone: map[string]bool{}}
	r.st = &remotepick.State{Post: func(f func()) { r.mu.Lock(); r.queue = append(r.queue, f); r.mu.Unlock() }}
	r.st.Fetch = func(ctx context.Context, q string) ([]remotepick.Candidate, error) {
		r.mu.Lock()
		r.asked = append(r.asked, q)
		h := r.hold[q]
		r.mu.Unlock()
		if h != nil {
			select {
			case <-h:
			case <-ctx.Done():
				r.mu.Lock()
				r.gone[q] = true
				r.mu.Unlock()
				return nil, ctx.Err()
			}
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.results[q], r.errs[q]
	}
	r.tt = ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&r.k.Theme)
		ui.Column(c.Key("root")).Width(420).Padding(16).Children(func() {
			r.k.RemotePicker(c, RemotePicker{Key: "place", State: r.st, Value: r.value, Empty: "没有设置", Placeholder: "搜索城市或地区",
				OnPick:  func(cd remotepick.Candidate) { r.picked = append(r.picked, cd); r.value = cd.Label },
				OnClear: func() { r.cleared++; r.value = "" }})
		})
	}, 520, 560)
	r.frames()
	return r
}

func (r *pickRig) frames() { r.tt.Frame(); r.tt.Frame() }

func (r *pickRig) asks() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.asked...)
}

// deliver 等请求的结果排上队再放行，然后画一帧
func (r *pickRig) deliver() {
	r.t.Helper()
	for i := 0; i < 400; i++ {
		r.mu.Lock()
		q := r.queue
		r.queue = nil
		r.mu.Unlock()
		if len(q) > 0 {
			for _, f := range q {
				f()
			}
			r.frames()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatal("结果没回来")
}

func (r *pickRig) waitAsked(n int) {
	r.t.Helper()
	for i := 0; i < 400; i++ {
		if len(r.asks()) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("请求数没到 %d：%v", n, r.asks())
}

// typeAndSettle 在搜索框里打字、等过去抖
func (r *pickRig) typeAndSettle(s string) {
	r.t.Helper()
	r.tt.Type(s)
	r.frames()
	r.tt.Advance(DebounceForTest + 50*time.Millisecond)
	r.frames()
}

const DebounceForTest = remotepick.DefaultDebounce

func (r *pickRig) focusInput() {
	r.t.Helper()
	if err := r.tt.Click("搜索城市或地区"); err != nil {
		r.t.Fatal(err)
	}
	r.frames()
}

func (r *pickRig) shot(name string) {
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		r.t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, r.tt.Image()); err != nil {
		r.t.Fatal(err)
	}
}

func haidian() []remotepick.Candidate {
	return []remotepick.Candidate{
		{ID: "1", Label: "海淀，北京市，中国", Detail: "城区"},
		{ID: "2", Label: "海淀，某省，某国", Detail: "村"},
	}
}

// 打字出候选：去抖之前不发请求，停手后只发最后的词；候选按“海淀，北京市，中国”这样显示；方向键加回车选中，显示规范结果
func TestRemotePickerTypeKeyboardPick(t *testing.T) {
	r := newPickRig(t)
	r.results["haidian"] = haidian()
	r.focusInput()
	r.tt.Type("hai")
	r.frames()
	r.tt.Advance(100 * time.Millisecond)
	r.frames()
	r.tt.Type("dian")
	r.frames()
	if len(r.asks()) != 0 {
		t.Fatalf("去抖期间不该发请求：%v", r.asks())
	}
	r.tt.Advance(DebounceForTest + 50*time.Millisecond)
	r.frames()
	r.waitAsked(1)
	if got := r.asks(); len(got) != 1 || got[0] != "haidian" {
		t.Fatalf("只该发最后的词：%v", got)
	}
	r.deliver()
	for _, want := range []string{"海淀，北京市，中国", "海淀，某省，某国"} {
		if !r.tt.HasText(want) {
			t.Fatalf("候选里没有 %q：%q", want, r.tt.Texts())
		}
	}
	r.shot("搜索下拉-候选.png")
	// 方向键：下（高亮第一项）、下（第二项）、上（回到第一项），回车选中第一项
	r.tt.Key(0, ui.KeyDown)
	r.frames()
	r.tt.Key(0, ui.KeyDown)
	r.frames()
	r.tt.Key(0, ui.KeyUp)
	r.frames()
	r.tt.Key(0, ui.KeyEnter)
	r.frames()
	if len(r.picked) != 1 || r.picked[0].ID != "1" {
		t.Fatalf("回车没选中：%+v", r.picked)
	}
	// 选中后显示规范结果，不是打的原文；搜索字清掉，候选面板收起
	if !r.tt.HasText(r.picked[0].Label) || r.tt.HasText("haidian") {
		t.Fatalf("选中后该显示规范结果：%q", r.tt.Texts())
	}
	if r.tt.HasText("海淀，某省，某国") {
		t.Fatal("选中后候选面板该收起")
	}
	r.shot("搜索下拉-选中后.png")
}

// 鼠标点选；方向键的落点要对（两下是第二个）
func TestRemotePickerMouseAndArrowTarget(t *testing.T) {
	r := newPickRig(t)
	r.results["haidian"] = haidian()
	r.focusInput()
	r.typeAndSettle("haidian")
	r.waitAsked(1)
	r.deliver()
	if err := r.tt.Click("海淀，某省，某国"); err != nil {
		t.Fatal(err)
	}
	r.frames()
	if len(r.picked) != 1 || r.picked[0].ID != "2" {
		t.Fatalf("鼠标该选中第二个：%+v", r.picked)
	}
	// 键盘：候选出来后按一下下高亮第一项，再按一下到第二项，回车选中
	r.value = ""
	r.frames()
	r.focusInput()
	r.typeAndSettle("haidian")
	r.waitAsked(2)
	r.deliver()
	r.tt.Key(0, ui.KeyDown)
	r.frames()
	r.tt.Key(0, ui.KeyDown)
	r.frames()
	r.tt.Key(0, ui.KeyEnter)
	r.frames()
	if len(r.picked) != 2 || r.picked[1].ID != "2" {
		t.Fatalf("按两下该选第二项：%+v", r.picked)
	}
}

// 字变了：旧请求被取消、晚到的旧结果不显示
func TestRemotePickerCancelsStaleRequest(t *testing.T) {
	r := newPickRig(t)
	gate := make(chan struct{})
	r.hold["bei"] = gate
	r.results["bei"] = []remotepick.Candidate{{ID: "old", Label: "旧结果，不该出现"}}
	r.results["beijing"] = []remotepick.Candidate{{ID: "new", Label: "北京市，中国"}}
	r.focusInput()
	r.typeAndSettle("bei")
	r.waitAsked(1)
	r.typeAndSettle("jing") // 变成 beijing：旧请求要被取消
	r.waitAsked(2)
	for i := 0; i < 200; i++ {
		r.mu.Lock()
		c := r.gone["bei"]
		r.mu.Unlock()
		if c {
			break
		}
		if i == 199 {
			t.Fatal("旧请求没被取消")
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.deliver()
	if !r.tt.HasText("北京市，中国") || r.tt.HasText("旧结果，不该出现") {
		t.Fatalf("候选：%q", r.tt.Texts())
	}
}

// 失败有文字，点重试再发；没有结果有文字
func TestRemotePickerFailureEmptyAndRetry(t *testing.T) {
	r := newPickRig(t)
	r.errs["xyz"] = errors.New("没取到候选：网络不通")
	r.focusInput()
	r.typeAndSettle("xyz")
	r.waitAsked(1)
	r.deliver()
	if !r.tt.HasText("没取到候选：网络不通") || !r.tt.HasText("重试") {
		t.Fatalf("失败要有文字和重试：%q", r.tt.Texts())
	}
	r.shot("搜索下拉-失败.png")
	delete(r.errs, "xyz")
	r.results["xyz"] = nil
	if err := r.tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	r.frames()
	r.waitAsked(2)
	r.deliver()
	if !r.tt.HasText("没有匹配的结果") || r.tt.HasText("没取到候选：网络不通") {
		t.Fatalf("重试后该显示没有匹配：%q", r.tt.Texts())
	}
	r.shot("搜索下拉-没有结果.png")
}

// 清空：当前值有时有“清空”，点了回调一次，值变成“没有设置”
func TestRemotePickerClear(t *testing.T) {
	r := newPickRig(t)
	if r.tt.HasText("清空") || !r.tt.HasText("没有设置") {
		t.Fatalf("没设时不该有清空：%q", r.tt.Texts())
	}
	r.value = "海淀，北京市，中国"
	r.frames()
	if !r.tt.HasText("海淀，北京市，中国") {
		t.Fatalf("该显示规范结果：%q", r.tt.Texts())
	}
	r.shot("搜索下拉-已设置.png")
	if err := r.tt.Click("清空"); err != nil {
		t.Fatal(err)
	}
	r.frames()
	if r.cleared != 1 || !r.tt.HasText("没有设置") || r.tt.HasText("清空") {
		t.Fatalf("清空：%d %q", r.cleared, r.tt.Texts())
	}
}

// 保存中不让选、不让清空
func TestRemotePickerBusyBlocksPick(t *testing.T) {
	r := newPickRig(t)
	r.value = "海淀，北京市，中国"
	k := r.k
	busy := true
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(&k.Theme)
		ui.Column(c.Key("root")).Width(420).Padding(16).Children(func() {
			k.RemotePicker(c, RemotePicker{Key: "place", State: r.st, Value: r.value, Empty: "没有设置", Placeholder: "搜索城市或地区", Busy: busy,
				OnPick: func(cd remotepick.Candidate) { r.picked = append(r.picked, cd) }, OnClear: func() { r.cleared++ }})
		})
	}, 520, 560)
	tt.Frame()
	tt.Frame()
	_ = tt.Click("清空")
	tt.Frame()
	if r.cleared != 0 {
		t.Fatal("保存中不该能清空")
	}
}
