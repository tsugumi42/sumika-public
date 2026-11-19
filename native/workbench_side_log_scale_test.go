package main

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/segview"
)

// scaleLogReader 是规模测试的假后台：2000 个批次、十个角色，每批每位角色带思考、工具、处理记录；按页（每页 5 批）给。
type scaleLogReader struct {
	api.Reader
	batches int
	mu      sync.Mutex
	reads   int
}

const scaleBatchesPerPage = 5

func (*scaleLogReader) SendWhisper(context.Context, api.ConversationKey, []api.LocalPart) (api.WhisperReceipt, error) {
	return api.WhisperReceipt{}, nil
}

// page 给第 n 页（0 是最新）：批号从大到小，页内从早到晚。
func (s *scaleLogReader) page(n int) ctlproto.LogPage {
	var es []ctlproto.LogEntry
	top := s.batches - n*scaleBatchesPerPage
	lo := max(top-scaleBatchesPerPage, 0)
	for b := lo; b < top; b++ {
		batch := fmt.Sprintf("B%06d", b)
		at := int64(1_000_000 + b*1000)
		var targets []string
		for c := range 10 {
			targets = append(targets, fmt.Sprintf("char%02d", c))
		}
		es = append(es, ctlproto.LogEntry{ID: "say:" + batch, Batch: batch, Kind: ctlproto.LogWhisper, At: at, Targets: targets, Summary: "帮我查一下 " + batch,
			Quotes: []ctlproto.LogQuote{{Name: "编造群友", Text: "编的被引原文\n第二行\n第三行"}}})
		for c, id := range targets {
			arr := "arr:" + batch + ":" + id
			es = append(es,
				ctlproto.LogEntry{ID: arr, Batch: batch, Kind: ctlproto.LogArrange, Character: id, Parent: "say:" + batch, At: at + 1, State: ctlproto.WhisperDone},
				ctlproto.LogEntry{ID: "step:th-" + batch + id, Batch: batch, Kind: ctlproto.LogThink, Character: id, Parent: arr, At: at + 20 + int64(c), Runes: 120, Summary: "编的思考内容，比较长的一段话，用来模拟真实的思考摘要"},
				ctlproto.LogEntry{ID: "step:to-" + batch + id, Batch: batch, Kind: ctlproto.LogTool, Character: id, Parent: arr, At: at + 40 + int64(c), State: "done", Summary: "web_search：搜到三条结果"},
				ctlproto.LogEntry{ID: "note:" + batch + ":" + id, Batch: batch, Kind: ctlproto.LogNote, Character: id, Parent: arr, At: at + 500, Summary: "编的处理记录 " + batch})
		}
	}
	p := ctlproto.LogPage{Entries: es}
	if lo > 0 {
		p.Next = "p" + strconv.Itoa(n+1)
	}
	return p
}

func (s *scaleLogReader) ReadWhisperLog(_ context.Context, _ api.ConversationKey, cursor string, _ int) (ctlproto.LogPage, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	n, _ := strconv.Atoi(strings.TrimPrefix(cursor, "p"))
	return s.page(n), nil
}
func (s *scaleLogReader) ReadWhisperLogEntry(context.Context, api.ConversationKey, string, bool) (ctlproto.LogDetail, error) {
	return ctlproto.LogDetail{}, nil
}
func (s *scaleLogReader) WatchWhisperLog(context.Context, api.ConversationKey) (api.Subscription[ctlproto.LogPage], error) {
	sub := &fakeLogSub{ch: make(chan api.Snapshot[ctlproto.LogPage], 2)}
	sub.ch <- api.Snapshot[ctlproto.LogPage]{Version: 1, Result: api.Result[ctlproto.LogPage]{State: api.Ready, Value: s.page(0)}}
	return sub, nil
}
func (s *scaleLogReader) CancelWhisper(context.Context, api.ConversationKey, string, string) (int, error) {
	return 0, nil
}

// sideShot 设了 WHISPER_SHOT（目录）时截右栏整条（像素比 1.25），只给人看，不做断言。
func sideShot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := os.Getenv("WHISPER_SHOT")
	if dir == "" {
		return
	}
	tt.Frame()
	img := tt.Image()
	rect := image.Rect(2113, 0, img.Bounds().Dx(), img.Bounds().Dy()).Intersect(img.Bounds())
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
	t.Logf("截图 %s：%dx%d 像素（右栏整条）", name, rect.Dx(), rect.Dy())
}

// countElementsOnce 数元素，数完把无障碍树关回去：开着它每帧都要多导出一整棵树，会把之后量的帧耗时量大。
func countElementsOnce(t *testing.T, tt *ui.Tester) int {
	t.Helper()
	n := countElements(t, tt)
	rt := reflect.ValueOf(tt).Elem().FieldByName("rt")
	access := rt.Elem().FieldByName("access")
	reflect.NewAt(access.Type(), unsafe.Pointer(access.UnsafeAddr())).Elem().SetBool(false)
	tt.Frame()
	return n
}

// 十个角色、一万条聊天消息、2000 批悄悄话的对话日志（ADR-056、规格 15.4）：只画读到的页，元素数随读到的页涨、不随总量涨；
// 记录元素数和帧耗时 P95（日志标签开着时在日志里滚动、在聊天区里滚动）。
func TestSideLogScaleTenCharacters(t *testing.T) {
	n, batches := 10000, 2000
	if testing.Short() {
		n, batches = 2000, 400
	}
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
		m := &msg{id: fmt.Sprintf("m%d", i), text: text, blocks: segview.TextBlocks(text), at: int64(i) * 61000}
		if i%3 == 0 {
			m.who, m.owner, m.actor = "主人", true, "owner"
		} else {
			m.who, m.isChar, m.actor = chars[i%10].Name, true, ids[i%10]
		}
		msgs[i] = m
	}
	rd := &scaleLogReader{batches: batches}
	q := &uiQueue{}
	a := &app{sel: key.Conversation, group: true, title: "编造的十人群", msgs: msgs, chars: chars, keys: map[string]api.ConversationKey{key.Conversation: key},
		reader: rd, host: hostState{Mode: "receive"}, recalled: map[string]bool{}, speakers: map[string]bool{}, started: time.Now()}
	a.postFn = q.post
	a.homes = []conv{{Key: key, Kind: "home", Title: "编造的十人群", Observers: ids}}
	a.mainView = "workbench"
	a.list = a.newList()
	a.workbench.shell.st.Panes.SideOpen = true
	tt := shellTester(a, 2048, 1030)
	frames := func() {
		for range 3 {
			settleMem(q)
			tt.Frame()
		}
	}
	if err := tt.Click("对话日志"); err != nil {
		t.Fatalf("没有对话日志标签：%v %q", err, tt.Texts())
	}
	frames()
	if !tt.HasText("载入更早的") {
		t.Fatalf("日志没出来：%q", tt.Texts())
	}
	// 帧耗时 P95 只记数不判红（判红的是元素数）：合并前的快测每组滚 6 帧够走通路径，60 帧的量测放在每日整套
	sample := 60
	if testing.Short() {
		sample = 6
	}
	measure := func(x, y float32, k int) (p95, worst time.Duration) {
		var ds []time.Duration
		for i := range k {
			d := float32(40)
			if i%2 == 1 {
				d = -40
			}
			tt.Scroll(x, y, 0, d)
			start := time.Now()
			tt.Frame()
			ds = append(ds, time.Since(start))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)*95/100], ds[len(ds)-1]
	}
	sideShot(t, tt, "9-十个角色的对话日志")
	// 先量帧、再数元素：数元素要打开无障碍树，之后每帧都多一份整棵树的导出，会把帧耗时量大
	for range 10 { // 先滚进内容中间：滚动要真的挪动内容才算
		tt.Scroll(1900, 500, 0, 600)
		tt.Frame()
	}
	sideP95, sideWorst := measure(1900, 500, sample)
	chatP95, chatWorst := measure(900, 500, sample)
	first := countElementsOnce(t, tt)
	// 载入更早的三页（每页 5 批、约 205 条）：按钮的点击在别处测过；这里直接调，避免鼠标停在按钮上引起的悬停动画把帧耗时量大
	for range 3 {
		a.sideLogOlder(&a.workbench.shell.sideLog, rd, key)
		frames()
	}
	loaded := countElementsOnce(t, tt)
	sideP95b, sideWorstb := measure(1900, 500, sample)
	rd.mu.Lock()
	reads := rd.reads
	rd.mu.Unlock()
	t.Logf("十个角色、%d 条聊天、%d 批悄悄话：对话日志元素 第一页 %d / 再载入 3 页后 %d（读了 %d 次）；帧 P95 第一页在日志里滚 %v（最慢 %v）、在聊天区滚 %v（最慢 %v）、载入 3 页后在日志里滚 %v（最慢 %v）",
		n, batches, first, loaded, reads, sideP95, sideWorst, chatP95, chatWorst, sideP95b, sideWorstb)
	if first > 4000 || loaded > 12000 {
		t.Fatalf("元素太多：%d %d", first, loaded)
	}
}
