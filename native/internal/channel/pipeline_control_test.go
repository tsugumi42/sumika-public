package channel

import (
	"sync"
	"testing"

	"sumika-native/internal/channel/types"
)

// Control 返回 true 的消息（审批回答）到此为止：不进观察者、拦截链、闸门；返回 false 的照常往下走。
// 消息本身已经入库（Control 在入库之后），所以界面和记录还在。
func TestPipelineControlConsumesBeforeChainAndGate(t *testing.T) {
	t.Parallel()
	s := &spy{}
	var mu sync.Mutex
	var asked []string
	ob := &obs{}
	h := newPH(t, []types.Interceptor{s.plugin("p", 10)}, func(_ *Config, p *PipelineConfig, _ *ChainConfig) {
		p.Control = func(st Stored) bool {
			mu.Lock()
			defer mu.Unlock()
			text := st.Inbound.Segs[0].Text
			asked = append(asked, text)
			return text == "同意"
		}
		p.Observers = []Observer{ob}
	})
	g := group("5001")
	h.push(msg(acctA, g, "101", friend, "同意"))
	h.push(msg(acctA, g, "102", friend, "别的话"))
	h.idle()
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 {
		t.Fatalf("两条都要问到 Control：%v", asked)
	}
	d := h.dispatches()
	if len(d) != 1 || d[0].Batch.Msgs[0].Segs[0].Text != "别的话" {
		t.Fatalf("被截走的不能交给闸门：%+v", d)
	}
	if b := s.seen(); len(b) != 1 || b[0].Msgs[0].Segs[0].Text != "别的话" {
		t.Fatalf("被截走的不能进拦截链：%+v", b)
	}
	if got := ob.texts(); len(got) != 1 || got[0] != "别的话" {
		t.Fatalf("被截走的不能交给观察者：%v", got)
	}
	// 已入库
	if n := len(h.events(g)); n != 2 {
		t.Fatalf("两条都要在库里：%d", n)
	}
}

type obs struct {
	mu sync.Mutex
	t  []string
}

func (o *obs) Name() string { return "obs" }
func (o *obs) Observe(x Observation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.t = append(o.t, x.Segs[0].Text)
}
func (o *obs) texts() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.t...)
}

// 没有 Control（默认）和以前一样。
func TestPipelineWithoutControlUnchanged(t *testing.T) {
	t.Parallel()
	h := newPH(t, nil)
	g := group("5001")
	h.push(msg(acctA, g, "101", friend, "同意"))
	h.idle()
	if len(h.dispatches()) != 1 {
		t.Fatal("没有 Control 时照常交闸门")
	}
}
