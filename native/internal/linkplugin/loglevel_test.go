package linkplugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
)

type levels struct {
	mu                sync.Mutex
	info, warn, error []string
}

func (l *levels) hook(p *Plugin) {
	add := func(to *[]string) func(string, ...any) {
		return func(f string, a ...any) { l.mu.Lock(); *to = append(*to, fmt.Sprintf(f, a...)); l.mu.Unlock() }
	}
	p.opt.Logf, p.opt.Warnf, p.opt.Errorf = add(&l.info), add(&l.warn), add(&l.error)
}

func (l *levels) in(level, frag string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := map[string][]string{"info": l.info, "warn": l.warn, "error": l.error}[level]
	return strings.Contains(strings.Join(lines, "\n"), frag)
}

// 第二次提交没能入队：后面的段没发出去、用户收不到，记 ERROR（规格-日志分级 5.1）；
// 读不到回执是“结果不明、不换形式”，记 WARN。这两类以前都是 INFO，--level warn 看不到。
type flakyOut struct {
	*scriptOut
	mu sync.Mutex
	n  int
}

func (o *flakyOut) Submit(ctx context.Context, a string, c types.Conversation, parts []types.Part, res []*types.MediaResource) (channel.Held, error) {
	o.mu.Lock()
	o.n++
	n := o.n
	o.mu.Unlock()
	if n > 1 {
		return channel.Held{}, errors.New("排队满了")
	}
	return o.scriptOut.Submit(ctx, a, c, parts, res)
}

func TestDeliveryFailureLevels(t *testing.T) {
	t.Run("换形式的发送没入队是 ERROR", func(t *testing.T) {
		f := newFlow(t, func(n int, p sentPart) echoReply { return rejected() }, nil)
		f.r.p.opt.Expand.Outlet = &flakyOut{scriptOut: f.out}
		var lv levels
		lv.hook(f.r.p)
		f.deliver(nil, textAndPics("正文", picture(t, 32)))
		if !lv.in("error", "未入队") || lv.in("info", "未入队") {
			t.Errorf("该是 ERROR 不是 INFO：%+v", &lv)
		}
	})
	t.Run("读不到回执是 WARN", func(t *testing.T) {
		f := newFlow(t, func(n int, p sentPart) echoReply { return ok("m1") }, nil)
		f.r.p.opt.Expand.Outcomes = func(types.Conversation, string) ([]PartOutcome, error) { return nil, errors.New("库读不出来") }
		var lv levels
		lv.hook(f.r.p)
		f.deliver(nil, textAndPics("正文", picture(t, 32)))
		if !lv.in("warn", "读结果计划的回执失败") || lv.in("info", "读结果计划的回执失败") {
			t.Errorf("该是 WARN 不是 INFO：%+v", &lv)
		}
	})
}

// 展开时结果没能入队（第一次提交就失败）、结果准备不出来：用户什么也收不到，记 ERROR
func TestExpandFailuresAreErrors(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, want string
		mod        func(*ExpandOptions)
	}{
		{"结果没入队", "结果未入队", func(e *ExpandOptions) { e.Outlet = &alwaysFail{} }},
		{"结果准备不出来", "准备结果失败", func(e *ExpandOptions) {
			e.Prepare = func(context.Context, channel.Stored, Snapshot, *linkreader.Result, *linkexpand.Draft) (linkexpand.Prepared, error) {
				return linkexpand.Prepared{}, errors.New("编的准备错误")
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ex := sendingExpand(confirmed, nil)
			c.mod(ex)
			r, sink := newTraceRig(t, &fakeFetcher{}, Options{Settings: expandSettings(nil), Expand: ex})
			var lv levels
			lv.hook(r.p)
			r.p.OnStored(storedMsg(41, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/1141"}}))
			waitSendRows(t, sink, 1)
			if !lv.in("error", c.want) || lv.in("info", c.want) {
				t.Errorf("%s该是 ERROR 不是 INFO：%+v", c.want, &lv)
			}
		})
	}
}

type alwaysFail struct{}

func (*alwaysFail) Submit(context.Context, string, types.Conversation, []types.Part, []*types.MediaResource) (channel.Held, error) {
	return channel.Held{}, errors.New("排队满了")
}
func (*alwaysFail) CancelPlan(string) {}
