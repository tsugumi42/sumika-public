package linkplugin

// 链接插件的插件日志（规格-插件两层 第二点五节第 4 条）和 2026-11-12 读码发现的几处没记录的跳过。
// 号、群、链接都是编的；取数是假的。展开出口用零值：这些用例都走不到真发送（表情全关，结果为空或没进队列）

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/plugintrace"
)

type traceSink struct {
	mu   sync.Mutex
	rows []plugintrace.Event
}

func (s *traceSink) add(e plugintrace.Event) { s.mu.Lock(); s.rows = append(s.rows, e); s.mu.Unlock() }
func (s *traceSink) snapshot() []plugintrace.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]plugintrace.Event(nil), s.rows...)
}
func (s *traceSink) find(f func(plugintrace.Event) bool) (plugintrace.Event, bool) {
	for _, e := range s.snapshot() {
		if f(e) {
			return e, true
		}
	}
	return plugintrace.Event{}, false
}

// expandSettings 是只在 g1 展开、三个表情都关掉的设置（不会碰出口）
func expandSettings(mut func(*config.LinkExpand)) func() Snapshot {
	return func() Snapshot {
		s := config.DefaultLinks()
		s.Context.Enabled = false
		s.Expand.Enabled, s.Expand.Mode, s.Expand.Conversations = true, "whitelist", []string{g1.String()}
		s.Expand.StartEmoji, s.Expand.DoneEmoji, s.Expand.FailEmoji = "", "", ""
		if mut != nil {
			mut(&s.Expand)
		}
		return Snapshot{Settings: s, Version: "t"}
	}
}

func stubExpand(admit bool) *ExpandOptions {
	return &ExpandOptions{Outlet: &channel.AutomationOutlet{},
		Admit:  func(channel.Stored) bool { return admit },
		Permit: func(channel.Stored, []types.Part) error { return nil },
		Prepare: func(context.Context, channel.Stored, Snapshot, *linkreader.Result, *linkexpand.Draft) (linkexpand.Prepared, error) {
			panic("这些用例不该走到准备结果")
		},
		Outcomes: func(types.Conversation, string) ([]PartOutcome, error) { return nil, nil },
		Block:    func(types.Conversation, types.Part, string) {}}
}

func newTraceRig(t *testing.T, f *fakeFetcher, opt Options) (*rig, *traceSink) {
	t.Helper()
	r := newRig(t, f, opt)
	sink := &traceSink{}
	r.p.setTrace(sink.add)
	return r, sink
}

func (r *rig) logged(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.log {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func storedMsg(seq int64, segs []types.Seg, tags ...string) channel.Stored {
	return channel.Stored{Character: "c1", Observed: true, Seq: seq, Tags: tags,
		Inbound: types.Inbound{Kind: types.InboundMessage, Conv: g1, PlatformID: "123", Segs: segs}}
}

// 取数一条：成功记来源和清洗后的链接，再取同一个命中缓存记 note cache，失败记 kind 和带原因的 error
func TestTraceFetchRows(t *testing.T) {
	t.Parallel()
	f := &fakeFetcher{fail: map[string]bool{"44": true}}
	r, sink := newTraceRig(t, f, Options{})
	r.msg(1, g1, "看 https://x.com/a/status/42?s=20 和 https://x.com/a/status/44", "c1")
	r.idle()
	r.msg(2, g1, "再看 https://x.com/a/status/42", "c1")
	r.idle()
	var ok, cache, fail int
	for _, e := range sink.snapshot() {
		switch {
		case e.Result == plugintrace.OK && e.Note == "cache":
			cache++
		case e.Result == plugintrace.OK:
			ok++
			if e.Site != "x" || e.Route != "fake" || strings.Contains(e.URL, "?") || !e.Timed {
				t.Fatalf("成功那行不对：%+v", e)
			}
		case e.Result == plugintrace.Fail:
			fail++
			if e.Kind != "not_found" || !strings.Contains(e.Error, "not_found") || e.Site != "x" {
				t.Fatalf("失败那行不对：%+v", e)
			}
		}
	}
	if ok != 1 || cache != 1 || fail != 1 {
		t.Fatalf("应当各一行：ok %d cache %d fail %d：%+v", ok, cache, fail, sink.snapshot())
	}
}

// 全部超时后整条重试过一次：取数那一条照旧一行，成功、失败的 note 都写“超时后重试了一次”。
func TestTraceFetchTimeoutRetryNote(t *testing.T) {
	t.Parallel()
	f := &fakeFetcher{fail: map[string]bool{"44": true}, retried: true}
	r, sink := newTraceRig(t, f, Options{})
	r.msg(1, g1, "看 https://x.com/a/status/42 和 https://x.com/a/status/44", "c1")
	r.idle()
	ok, found := sink.find(func(e plugintrace.Event) bool { return e.Result == plugintrace.OK && e.Route == "fake" })
	if !found || ok.Note != noteRetryTimeout {
		t.Fatalf("成功那次该写重试：%+v", ok)
	}
	fail, found := sink.find(func(e plugintrace.Event) bool { return e.Result == plugintrace.Fail && e.Kind == "not_found" })
	if !found || fail.Note != noteRetryTimeout {
		t.Fatalf("失败那次该写重试：%+v", fail)
	}
}

// bug（plugin.go 约 273 行）：卡片载荷解析不了被吞掉，查不到为什么卡片里的链接没处理。改后记日志和一行 fail（kind card）。
// 放回旧代码（FromCard 的结果直接丢掉）就红：没有日志、没有这一行
func TestCardParseErrorIsLoggedAndTraced(t *testing.T) {
	t.Parallel()
	r, sink := newTraceRig(t, &fakeFetcher{}, Options{})
	s := storedMsg(5, []types.Seg{{Kind: types.SegCard, CardJSON: `{"meta":`}})
	r.p.OnStored(s)
	if !r.logged("卡片解析不了") {
		t.Fatalf("应当记一行日志：%v", r.log)
	}
	if _, ok := sink.find(func(e plugintrace.Event) bool {
		return e.Kind == "card" && e.Result == plugintrace.Fail && e.Error != ""
	}); !ok {
		t.Fatalf("应当记一行 card 失败：%+v", sink.snapshot())
	}
}

// bug（plugin.go 约 166 行）：断线补录进来的链接不展开（规格-QQ收消息与补录 I-27：补录不触发回复），以前也不留任何记录。
// 改后照样不取不展开，这个会话本来会展开时记一行 skip（note 补录）。放回旧代码就红：没有这一行
func TestBackfillLinkSkipIsTraced(t *testing.T) {
	t.Parallel()
	f := &fakeFetcher{}
	r, sink := newTraceRig(t, f, Options{Settings: expandSettings(nil), Expand: stubExpand(true)})
	r.p.OnStored(storedMsg(7, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/77"}}, channel.TagBackfill))
	r.p.OnStored(channel.Stored{Character: "c1", Duplicate: true, Seq: 8, Inbound: types.Inbound{Conv: g1, Segs: []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/78"}}}})
	r.idle()
	if f.calls.Load() != 0 {
		t.Fatal("补录、重复的不该取数", f.calls.Load())
	}
	rows := sink.snapshot()
	if len(rows) != 2 || rows[0].Note != noteBackfill || rows[1].Note != noteDuplicate || rows[0].Route != RouteSend || rows[0].Result != plugintrace.Skip || rows[0].URL != "https://x.com/a/status/77" {
		t.Fatalf("补录、重复各记一行 skip：%+v", rows)
	}
	// 没在名单里的会话：补录也不写，免得每条都一行
	r2, sink2 := newTraceRig(t, &fakeFetcher{}, Options{Settings: expandSettings(func(e *config.LinkExpand) { e.Conversations = nil }), Expand: stubExpand(true)})
	r2.p.OnStored(storedMsg(7, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/77"}}, channel.TagBackfill))
	if n := len(sink2.snapshot()); n != 0 {
		t.Fatal("本来就不展开的会话不记补录", sink2.snapshot())
	}
}

// 没展开的原因：开关没开、白名单外、黑名单内、发的人不能触发，各记一行 skip；没有链接的消息不写
func TestNotExpandedReasons(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mut   func(*config.LinkExpand)
		admit bool
		want  string
	}{
		{"开关没开", func(e *config.LinkExpand) { e.Enabled = false }, true, noteExpandOff},
		{"白名单外", func(e *config.LinkExpand) { e.Conversations = nil }, true, noteNotListed},
		{"黑名单内", func(e *config.LinkExpand) { e.Mode = "blacklist" }, true, noteBlacklist},
		{"不能触发", nil, false, noteNotAdmit},
		{"发的人不在群的白名单里", func(e *config.LinkExpand) { e.Senders.Mode = config.SendersWhitelist }, true, noteSender},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r, sink := newTraceRig(t, &fakeFetcher{}, Options{Settings: expandSettings(c.mut), Expand: stubExpand(c.admit)})
			r.p.OnStored(storedMsg(3, []types.Seg{{Kind: types.SegText, Text: "没有链接"}}))
			r.p.OnStored(storedMsg(4, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/9"}}))
			rows := sink.snapshot()
			if len(rows) != 1 || rows[0].Note != c.want || rows[0].Result != plugintrace.Skip {
				t.Fatalf("应当一行 %s：%+v", c.want, rows)
			}
		})
	}
}

// shortToFetchOnly 把短链解到一个只取数、不展开的页面（GitHub issue）
type shortToFetchOnly struct{ fakeFetcher }

func (f *shortToFetchOnly) Resolve(_ context.Context, l linkreader.Link) (linkreader.Link, error) {
	if l.Kind == "short" {
		out, _ := linkreader.Recognize("https://github.com/paper-lab/boat/issues/7")
		return out, nil
	}
	return l, nil
}

// bug（expand.go 约 349 行）：linkexpand.Build 返回空段时静默跳过。改后记日志和一行 skip（结果为空），收尾照常。
// 短链解析成只取数、不展开的页面类型时就是空段。放回旧代码就红：没有日志、没有这一行
func TestEmptyDraftIsLoggedAndTraced(t *testing.T) {
	t.Parallel()
	f := &shortToFetchOnly{}
	r := newRig(t, &f.fakeFetcher, Options{Settings: expandSettings(nil), Expand: stubExpand(true)}, f)
	sink := &traceSink{}
	r.p.setTrace(sink.add)
	r.p.OnStored(storedMsg(9, []types.Seg{{Kind: types.SegText, Text: "https://t.co/AbCdEf"}}))
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := sink.find(func(e plugintrace.Event) bool { return e.Route == RouteSend }); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("等不到发送那一行：%+v %v", sink.snapshot(), r.log)
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.idle()
	e, _ := sink.find(func(e plugintrace.Event) bool { return e.Route == RouteSend })
	if e.Result != plugintrace.Skip || e.Note != noteEmpty || e.Site != "github" {
		t.Fatalf("空结果应当记 skip（结果为空）：%+v", sink.snapshot())
	}
	if !r.logged("展开结果为空") {
		t.Fatalf("应当记一行日志：%v", r.log)
	}
}
