package memory

import (
	"context"
	"strings"
	"testing"
)

func TestADR242RecallEveryPlace(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	mustApply(t, m, remember(ownerFact("主人喜欢星港咖啡"), FactOp{Kind: "boundary", Text: "不叫主人小朋友", Subject: "owner", Status: "active", ConfirmedBy: "owner", Scopes: []string{"owner"}}))
	for _, a := range []Audience{
		ownerHere,
		{Channel: "console", Conversation: "console:group:home", Scene: "group", PrivateOK: true},
		{Channel: "qq", Conversation: "qq:private:101:202", Scene: "private"},
		{Channel: "qq", Conversation: "qq:group:303", Scene: "group"},
	} {
		t.Run(a.Conversation, func(t *testing.T) {
			r := m.Recall(context.Background(), a, Query{Text: "星港咖啡", Mode: ModeExplicit})
			if len(r.Hits) != 1 || r.Hits[0].Text != "主人喜欢星港咖啡" || len(r.Pinned) != 1 {
				t.Fatalf("跨场合召回/固定要点缺失：%+v", r)
			}
			s, err := m.Search(context.Background(), a, "星港咖啡")
			if err != nil || !strings.Contains(s.Text, "主人喜欢星港咖啡") {
				t.Fatalf("Search=%+v %v", s, err)
			}
		})
	}
	// 能想起不代表能向群友披露。
	blocked, err := m.CheckOutbound(context.Background(), "主人喜欢星港咖啡", Audience{Channel: "qq", Conversation: "qq:group:303", Scene: "group"})
	if err != nil || len(blocked) != 1 {
		t.Fatalf("群友套话未拦：%v %v", blocked, err)
	}
}

func TestADR242RawEveryPlace(t *testing.T) {
	t.Parallel()
	ev := newSQLEvents(t)
	seedRaw(t, ev, ownerSays(tlMain, "今晚去星港观星", 0))
	m, _ := openTools(t, ev)
	for _, a := range []Audience{ownerHere, {Channel: "console", Conversation: "console:group:home", Scene: "group"}, {Channel: "qq", Conversation: "qq:private:101:202", Scene: "private"}, {Channel: "qq", Conversation: "qq:group:303", Scene: "group"}} {
		res := recallRaw(t, m, a, "星港")
		if !strings.Contains(res.Text, "今晚去星港观星") {
			t.Fatalf("%s=%+v 查询=%q", a.Conversation, res, ev.lastConv)
		}
	}
}
