package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestADR242TrustedSourceLabels(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	events := newSQLEvents(t)
	peer := peerSays(tlGroup, "5", "主人", "星桥茶很好喝", 0)
	events.add(peer)
	source, ok, err := events.LastSaid(ctx, tlGroup)
	if err != nil || !ok {
		t.Fatalf("来源夹具：%+v %v", source, err)
	}
	m, err := Open(ctx, migratedDB(t, nil), nil, nil, Options{Character: "aria", Writable: true, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, c := range []struct {
		kind, status, text, conv string
		seq                      int64
	}{
		{"fact", "active", "主人常喝星桥茶", tlGroup, source.Seq},
		{"boundary", "active", "星桥茶要先放凉", tlGroup, source.Seq},
		{"fact", "candidate", "星桥茶或许能安神", tlGroup, source.Seq},
		{"fact", "active", "星桥茶的伪造引用", tlOther, source.Seq},
		{"fact", "active", "星桥茶的空来源", tlGroup, -1},
		{"fact", "active", "星桥茶的零序号", tlGroup, 0},
		{"fact", "active", "星桥茶的越界序号", tlGroup, source.Seq + 100},
	} {
		op := FactOp{Kind: c.kind, Status: c.status, Text: c.text, Subject: "owner", Scopes: []string{"owner"}, Conversation: c.conv, SourceSeqs: []int64{c.seq}, Origin: "owner"}
		if c.seq < 0 {
			op.SourceSeqs = nil
		}
		mustApply(t, m, remember(op))
	}
	r := m.Recall(ctx, ownerHere, Query{Text: "星桥茶", Mode: ModeExplicit, TopK: 12})
	if len(r.Hits) != 7 || len(r.Pinned) != 1 {
		t.Fatalf("待测路径没命中：%+v", r)
	}
	for _, h := range append(r.Hits, r.Pinned...) {
		if h.Conversation == tlOther || len(h.SourceSeqs) == 0 || h.SourceSeqs[0] <= 0 || h.SourceSeqs[0] > source.Seq {
			if !strings.Contains(h.Source, "来源未核实") {
				t.Errorf("同 seq 跨会话冒充成功：%+v", h)
			}
		} else if !strings.Contains(h.Source, "主人（群友）") || !strings.Contains(h.Source, tlGroup) || !strings.Contains(h.Source, "群聊") {
			t.Errorf("名字冒充或来源不全：%+v", h)
		}
	}
	block := strings.Join(Render(r, time.Now()).Lines(true), "\n")
	if !strings.Contains(block, "听说：") || strings.Count(block, "〔来源：") != 8 {
		t.Fatalf("渲染缺来源或听说：%s", block)
	}
	result, err := m.Search(ctx, ownerHere, "星桥茶")
	if err != nil || !strings.Contains(result.Text, "主人（群友）") || !strings.Contains(result.Text, tlGroup) {
		t.Fatalf("工具丢来源：%+v %v", result, err)
	}
	raw, err := m.RecallRaw(ctx, Audience{Channel: "qq", Conversation: tlOther, Scene: "group"}, "星桥茶")
	if err != nil || !strings.Contains(raw.Text, "主人（群友）") || !strings.Contains(raw.Text, tlGroup) {
		t.Fatalf("原话丢身份或来源：%+v %v", raw, err)
	}
	m.opt.Events = unreadableSource{events}
	failed := m.Recall(ctx, ownerHere, Query{Text: "星桥茶", Mode: ModeExplicit, TopK: 12})
	for _, h := range append(failed.Hits, failed.Pinned...) {
		if !strings.Contains(h.Source, "来源未核实") {
			t.Errorf("读取失败伪造了来源：%+v", h)
		}
	}
}

type unreadableSource struct{ EventSource }

func (unreadableSource) Events(context.Context, string, int64, int) ([]Event, error) {
	return nil, errors.New("fixture source unavailable")
}
