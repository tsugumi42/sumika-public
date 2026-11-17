package main

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/segview"
)

// scaleWhisperReader 只让界面认出“后台能收悄悄话”；规模测试里不发送。
type scaleWhisperReader struct{ api.Reader }

func (scaleWhisperReader) SendWhisper(context.Context, api.ConversationKey, []api.LocalPart) (api.WhisperReceipt, error) {
	return api.WhisperReceipt{}, nil
}

// 十个角色的本地群、一万条消息（ADR-056）：悄悄话入口打开时元素数不涨（菜单、对象表只在打开时算）；
// 进多选每行只多一个点击层和勾选圈，行高不变、锚点不跳；记录帧耗时 P95。
func TestWhisperScaleTenCharactersTenThousand(t *testing.T) {
	// 合并前的快测用 2000 条（锚点放在正中），整套里跑一万条（ADR-056 的规模数）
	n := 10000
	if testing.Short() {
		n = 2000
	}
	mid := n / 2
	anchor := fmt.Sprintf("锚点句子-%d", mid)
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: "console:group:scale"}
	var chars []character
	var ids []string
	for i := range 10 {
		id := fmt.Sprintf("char%02d", i)
		chars = append(chars, character{ID: id, Name: "编造角色" + id})
		ids = append(ids, id)
	}
	msgs := make([]*msg, n)
	for i := range n {
		text := fmt.Sprintf("编的群消息 %d", i)
		if i == mid {
			text = anchor
		}
		m := &msg{id: fmt.Sprintf("m%d", i), text: text, blocks: segview.TextBlocks(text), at: int64(i) * 61000}
		if i%3 == 0 {
			m.who, m.owner, m.actor = "主人", true, "owner"
		} else {
			m.who, m.isChar, m.actor = chars[i%10].Name, true, ids[i%10]
		}
		msgs[i] = m
	}
	a := &app{sel: key.Conversation, group: true, title: "编造的十人群", msgs: msgs, chars: chars, keys: map[string]api.ConversationKey{key.Conversation: key},
		reader: scaleWhisperReader{}, host: hostState{Mode: "receive"}}
	a.homes = []conv{{Key: key, Kind: "home", Title: "编造的十人群", Observers: ids}}
	a.list = a.newList()
	if !a.canWhisper() || len(a.whisperTargets("m1")) != 10 {
		t.Fatalf("规模夹具没打开悄悄话：%v %d", a.canWhisper(), len(a.whisperTargets("m1")))
	}
	tt := ui.NewTester(func(c *ui.Context) {
		a.applyLook(c)
		a.workbenchChat(c)
	}, 2048, 1030)
	tt.SetScale(1.25)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	a.list.ScrollTo(mid, ui.Start)
	tt.Frame()
	tt.Frame()
	// 帧耗时 P95 只记数不判红（上面的断言是元素数和锚点不跳）：合并前的快测每组滚 6 帧够走通路径，
	// 60 帧的量测放在每日整套，一帧要光栅化整个窗口，120 帧占了这条测试的大半。滚动次数保持偶数，进出一遍回到原位。
	sample := 60
	if testing.Short() {
		sample = 6
	}
	frames := func(k int) (p95, worst time.Duration) {
		var ds []time.Duration
		for i := range k {
			if i%2 == 0 {
				tt.Scroll(1000, 500, 0, 40)
			} else {
				tt.Scroll(1000, 500, 0, -40)
			}
			start := time.Now()
			tt.Frame()
			ds = append(ds, time.Since(start))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)*95/100], ds[len(ds)-1]
	}
	plain := countElements(t, tt)
	before, ok := tt.Find(anchor)
	if !ok {
		t.Fatal("锚点不在视野里")
	}
	p95, worst := frames(sample)
	// 进多选
	a.pick.Start(key.Conversation, fmt.Sprintf("m%d", mid))
	tt.Frame()
	tt.Frame()
	picked := countElements(t, tt)
	after, ok := tt.Find(anchor)
	if !ok {
		t.Fatal("进多选后锚点不见了")
	}
	jump := after.Y - before.Y
	if jump < -1 || jump > 1 {
		t.Fatalf("进多选锚点跳了 %.2f", jump)
	}
	pp95, pworst := frames(sample)
	// 选满 20 条
	for i := mid + 1; i < mid+25; i++ { // 点 24 条，满 20 后点不上
		a.pick.Click(fmt.Sprintf("m%d", i), false, nil)
	}
	if len(a.pick.IDs) != 20 {
		t.Fatalf("上限 20：%d", len(a.pick.IDs))
	}
	first, last := a.list.Visible()
	t.Logf("十个角色、%d 条：元素 平时 %d / 多选 %d；可见行 %d；帧 P95 平时 %v（最慢 %v）/ 多选 %v（最慢 %v）；进多选锚点位移 %.2f",
		n, plain, picked, last-first+1, p95, worst, pp95, pworst, jump)
	if picked > 4000 || plain > 4000 {
		t.Fatalf("元素太多：%d %d", plain, picked)
	}
}

// whisperShot 设了 WHISPER_SHOT（目录）时把几个元素的包围框外扩 24 DIP 截下来（像素比 1.25），只给人看，不做断言。
func whisperShot(t *testing.T, tt *ui.Tester, name string, labels ...string) {
	t.Helper()
	dir := os.Getenv("WHISPER_SHOT")
	if dir == "" {
		return
	}
	tt.Frame()
	var box ui.Rect
	found := false
	for _, l := range labels {
		r, ok := tt.Find(l)
		if !ok {
			t.Logf("截图 %s：找不到 %q", name, l)
			continue
		}
		if !found {
			box, found = r, true
			continue
		}
		x0, y0 := min(box.X, r.X), min(box.Y, r.Y)
		x1, y1 := max(box.X+box.W, r.X+r.W), max(box.Y+box.H, r.Y+r.H)
		box = ui.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
	}
	if !found {
		return
	}
	img := tt.Image()
	const s, pad = 1.25, 24
	rect := image.Rect(int((box.X-pad)*s), int((box.Y-pad)*s), int((box.X+box.W+pad)*s), int((box.Y+box.H+pad)*s)).Intersect(img.Bounds())
	sub := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(sub, sub.Bounds(), img, rect.Min, draw.Src)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, sub); err != nil {
		t.Fatal(err)
	}
	t.Logf("截图 %s：%dx%d 像素，区域 %+v", name, rect.Dx(), rect.Dy(), box)
}
