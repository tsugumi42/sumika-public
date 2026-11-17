package main

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/config"
	"sumika-native/internal/settingsreg"
)

// webRegReader 是只带联网几项的假设置注册表：顺序存在内存里，写入要带最近读到的版本，旧了就拒收；写入可以让它失败。
type webRegReader struct {
	api.Reader
	mu      sync.Mutex
	vals    map[string]any
	version int
	fail    error
	writes  []string
	balance map[string]string
}

func newWebReg() *webRegReader {
	return &webRegReader{vals: map[string]any{
		"web.enabled":         true,
		"web.searchOrder":     []any{"deepseek", "tavily", "exa", "brave", "firecrawl", "searxng", "duckduckgo"},
		"web.fetchOrder":      []any{"http", "deepseek", "tavily", "firecrawl"},
		"web.deepseekKeyRef":  "已存",
		"web.keyRef":          "没存",
		"web.exaKeyRef":       "没存",
		"web.braveKeyRef":     "没存",
		"web.firecrawlKeyRef": "已存",
		"web.searxng.url":     "",
	}, balance: map[string]string{"firecrawl": "剩余 880"}}
}

func (r *webRegReader) view() api.SettingsView {
	v := api.SettingsView{Version: api.SettingsVersion{Config: strconv.Itoa(r.version)}}
	for k, val := range r.vals {
		v.Items = append(v.Items, settingsreg.Value{Entry: settingsreg.Entry{Key: k, Effect: "立即"}, Value: val})
	}
	return v
}

func (r *webRegReader) ReadSettingsRegistry(context.Context) (api.SettingsView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.view(), nil
}

func (r *webRegReader) WriteSetting(_ context.Context, ch api.SettingChange, ver api.SettingsVersion) (api.SettingsView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, ch.Key+"="+string(ch.Value))
	if r.fail != nil {
		return api.SettingsView{}, r.fail
	}
	if ver.Config != strconv.Itoa(r.version) {
		return api.SettingsView{}, errors.New("版本旧了")
	}
	var ids []string
	if err := json.Unmarshal(ch.Value, &ids); err != nil || config.ValidWebOrder(ch.Key, ids) != nil {
		return api.SettingsView{}, errors.New("值不合法")
	}
	list := make([]any, len(ids))
	for i, id := range ids {
		list[i] = id
	}
	r.vals[ch.Key] = list
	r.version++
	return r.view(), nil
}

func (r *webRegReader) WebBalanceTexts() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.balance
}

func (r *webRegReader) snapshot() (map[string][]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string][]string{}
	for _, k := range []string{"web.searchOrder", "web.fetchOrder"} {
		out[k] = stringList(r.vals[k])
	}
	return out, append([]string(nil), r.writes...)
}

// webUIApp 是停在“设置 → 工具与审批”、后台带联网设置的新界面。
func webUIApp(t *testing.T, reg *webRegReader) (*app, *ui.Tester, func()) {
	t.Helper()
	a, tt, pump := remoteUIApp(t, &regReader{})
	a.realTab = tabTools
	a.reader = reg
	pump()
	pump()
	// 设置是后台线程读回来再排进界面队列的：机器忙时两次 pump 可能还没等到，别在“找不到元素”上随机红。
	// 等到两张表的行都画出来（上限由 remoteWait 给，只防卡死）
	remoteWait(t, "联网顺序两张表画出来", func() bool {
		pump()
		return hasKey(tt, "web-row-web.searchOrder-deepseek") && hasKey(tt, "web-row-web.fetchOrder-tavily")
	})
	return a, tt, pump
}

// 等写入到达后台、结果也回到界面（busy 清掉）再往下，理由同 notifyWaitWrites
func webWaitWrites(t *testing.T, r *webRegReader, n int, pump func(), a *app) {
	t.Helper()
	remoteWait(t, "写入到达后台、结果回到界面", func() bool { pump(); _, w := r.snapshot(); return len(w) >= n && !a.webUI.busy })
	pump()
}

// dragWebRow 按住 from 那一行，拖到 to 那一行的下半（放在它后面）或上半（放在它前面）。
func dragWebRow(t *testing.T, tt *ui.Tester, key, from, to string, after bool) {
	t.Helper()
	src, dst := boxOf(t, tt, "web-row-"+key+"-"+from), boxOf(t, tt, "web-row-"+key+"-"+to)
	x, y := src.Box.X+src.Box.W/2, src.Box.Y+src.Box.H/2
	ty := dst.Box.Y + 2
	if after {
		ty = dst.Box.Y + dst.Box.H - 2
	}
	tt.Press(x, y)
	for k := 1; k <= 4; k++ {
		f := float32(k) / 4
		tt.Move(x, y+(ty-y)*f)
	}
	tt.Release(x, ty)
	tt.Frame()
	tt.Frame()
}

func shotWebLists(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		return
	}
	first, last := boxOf(t, tt, "web-title-web.searchOrder"), boxOf(t, tt, "web-row-web.fetchOrder-firecrawl")
	if hasKey(tt, "web-row-web.fetchOrder-tavily") {
		if b := boxOf(t, tt, "web-row-web.fetchOrder-tavily"); b.Box.Y > last.Box.Y {
			last = b
		}
	}
	const scale, pad = 1.25, 16
	rect := image.Rect(int(float64(first.Box.X)*scale)-pad, int(float64(first.Box.Y)*scale)-pad, int(float64(first.Box.X+first.Box.W)*scale)+pad, int(float64(last.Box.Y+last.Box.H)*scale)+pad)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, tt.Image().SubImage(rect.Intersect(tt.Image().Bounds()))); err != nil {
		t.Fatal(err)
	}
}

func TestMoveRows(t *testing.T) {
	t.Parallel()
	l := []string{"a", "b", "c", "d", "e"}
	for _, tc := range []struct {
		rows []int
		to   int
		want string
	}{
		{[]int{0}, 3, "bcade"}, {[]int{3}, 1, "adbce"}, {[]int{1}, 5, "acdeb"}, {[]int{1, 3}, 0, "bdace"}, {[]int{2}, 2, "abcde"}, {[]int{2}, 3, "abcde"}, {[]int{4}, 0, "eabcd"},
	} {
		if got := strings.Join(moveRows(l, tc.rows, tc.to), ""); got != tc.want {
			t.Errorf("moveRows(%v,%d)=%s 应为 %s", tc.rows, tc.to, got, tc.want)
		}
	}
}

// 设置 → 工具与审批 → 联网：两张顺序表按配置里的顺序列出；没填 key 的灰显并写明会跳过；
// 能查到余额的写“剩余 N”；不要 key 的写“免费”。
func TestWebOrderListsStatuses(t *testing.T) {
	reg := newWebReg()
	a, tt, _ := webUIApp(t, reg)
	st := &a.webUI
	if !st.known || !st.enabled {
		t.Fatalf("应读到设置：%+v", st)
	}
	for _, want := range []string{"联网", "搜索服务的先后", "网页读取的先后", "DeepSeek", "Tavily", "SearXNG", "DuckDuckGo", "本机直接读取", "剩余 880", "免费", "已填 key", "没填 key", "没配地址"} {
		if !tt.HasText(want) {
			t.Errorf("缺“%s”：%q", want, tt.Texts())
		}
	}
	order := st.shown("web.searchOrder")
	if !slices.Equal(order, []string{"deepseek", "tavily", "exa", "brave", "firecrawl", "searxng", "duckduckgo"}) {
		t.Fatalf("搜索顺序：%v", order)
	}
	// 行按顺序从上往下排
	prev := float32(-1)
	for _, id := range order {
		b := boxOf(t, tt, "web-row-web.searchOrder-"+id)
		if b.Box.Y <= prev {
			t.Errorf("%s 排得不对：%v <= %v", id, b.Box.Y, prev)
		}
		prev = b.Box.Y
	}
	if !st.available("deepseek") || st.available("tavily") || !st.available("duckduckgo") || st.available("searxng") || !st.available("http") {
		t.Errorf("可用判断不对：%+v", st.hasKey)
	}
	shotWebLists(t, tt, "联网顺序-默认.png")
}

// 拖一行到另一行后面：整张表写回设置注册表，顺序当场变；没读到的家补在最后一并写回。
func TestWebOrderDragWritesBack(t *testing.T) {
	reg := newWebReg()
	a, tt, pump := webUIApp(t, reg)
	dragWebRow(t, tt, "web.searchOrder", "duckduckgo", "deepseek", false)
	webWaitWrites(t, reg, 1, pump, a)
	got, writes := reg.snapshot()
	want := []string{"duckduckgo", "deepseek", "tavily", "exa", "brave", "firecrawl", "searxng"}
	if !slices.Equal(got["web.searchOrder"], want) {
		t.Fatalf("写回的顺序：%v，应为 %v（写入记录 %v）", got["web.searchOrder"], want, writes)
	}
	if !slices.Equal(got["web.fetchOrder"], []string{"http", "deepseek", "tavily", "firecrawl"}) {
		t.Errorf("另一张表不该动：%v", got["web.fetchOrder"])
	}
	if shown := a.webUI.shown("web.searchOrder"); !slices.Equal(shown, want) {
		t.Errorf("页面显示：%v", shown)
	}
	first, second := boxOf(t, tt, "web-row-web.searchOrder-duckduckgo"), boxOf(t, tt, "web-row-web.searchOrder-deepseek")
	if first.Box.Y >= second.Box.Y {
		t.Errorf("拖完 DuckDuckGo 应排在 DeepSeek 前面：%v %v", first.Box.Y, second.Box.Y)
	}
	// 接着拖第二张表：版本号不能被挡（上一次写成功后版本已更新）
	dragWebRow(t, tt, "web.fetchOrder", "firecrawl", "http", false)
	webWaitWrites(t, reg, 2, pump, a)
	got, _ = reg.snapshot()
	if !slices.Equal(got["web.fetchOrder"], []string{"firecrawl", "http", "deepseek", "tavily"}) {
		t.Fatalf("第二张表：%v", got["web.fetchOrder"])
	}
	shotWebLists(t, tt, "联网顺序-拖完.png")
}

// 写失败：写明没改成，页面回到后台真实的顺序，不假装成功。
func TestWebOrderWriteFailureRevertsAndSaysSo(t *testing.T) {
	reg := newWebReg()
	reg.fail = errors.New("配置文件被占用")
	a, tt, pump := webUIApp(t, reg)
	dragWebRow(t, tt, "web.fetchOrder", "tavily", "http", false)
	webWaitWrites(t, reg, 1, pump, a)
	// 写失败的结果异步回到界面：刷到说明和顺序都回来为止，不按固定帧数等（负载下 4 帧不够，2026-11-12 合并队列红过）
	want := []string{"http", "deepseek", "tavily", "firecrawl"}
	remoteWait(t, "写失败后回到真实顺序并写明", func() bool {
		pump()
		return tt.HasText("没有改成：配置文件被占用") && slices.Equal(a.webUI.shown("web.fetchOrder"), want)
	})
	got, _ := reg.snapshot()
	if !slices.Equal(got["web.fetchOrder"], []string{"http", "deepseek", "tavily", "firecrawl"}) {
		t.Errorf("后台不该变：%v", got["web.fetchOrder"])
	}
}

// 用户已经给了顺序列表里没有的家（旧配置只写了几家）：补在最后显示，拖动时一并写回。
func TestWebOrderShowsMissingProvidersAtEnd(t *testing.T) {
	reg := newWebReg()
	reg.vals["web.searchOrder"] = []any{"tavily", "nope", "tavily", "deepseek"}
	a, tt, _ := webUIApp(t, reg)
	want := []string{"tavily", "deepseek", "exa", "brave", "firecrawl", "searxng", "duckduckgo"}
	if shown := a.webUI.shown("web.searchOrder"); !slices.Equal(shown, want) {
		t.Fatalf("显示顺序：%v，应为 %v", shown, want)
	}
	if !hasKey(tt, "web-row-web.searchOrder-duckduckgo") {
		t.Error("补上的家也要画出来")
	}
}

// 联网没开：照样能排，但写明还没开。后台没有设置注册表：整节不画。
func TestWebOrderDisabledAndNoRegistry(t *testing.T) {
	reg := newWebReg()
	reg.vals["web.enabled"] = false
	_, tt, _ := webUIApp(t, reg)
	if !tt.HasText("联网还没开（配置里的 web.enabled）。顺序可以先排好。") || !hasKey(tt, "web-row-web.searchOrder-deepseek") {
		t.Errorf("联网没开的提示：%q", tt.Texts())
	}
	a, tt2, pump := remoteUIApp(t, &regReader{})
	a.realTab = tabTools
	a.reader = api.Reader(nil)
	pump()
	if hasKey(tt2, "web-title-web.searchOrder") {
		t.Error("没有设置注册表不该画联网顺序")
	}
}
