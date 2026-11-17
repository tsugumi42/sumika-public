package store

import (
	"context"
	"testing"
)

func TestChannelGapMergesOpenIntervals(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	s, err := set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	add := func(lo, hi int64) {
		t.Helper()
		if err := s.RecordChannelGap(ctx, ChannelGap{Channel: "qq", Conversation: "qq:group:800", FromMS: lo, ToMS: hi, Reason: "unverified-head", LastTryMS: 100}); err != nil {
			t.Fatal(err)
		}
	}
	add(0, 3)
	add(6, 9)
	gaps, err := s.ChannelGaps(ctx, "qq")
	if err != nil {
		t.Fatal(err)
	}
	g := gaps[0]
	g.Tries = 2
	g.UpdatedMS = 200
	g.LastTryMS = 200
	if err := s.UpdateChannelGap(ctx, g); err != nil {
		t.Fatal(err)
	}
	add(4, 5)
	gaps, err = s.ChannelGaps(ctx, "qq")
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].FromMS != 0 || gaps[0].ToMS != 9 || gaps[0].Tries != 2 || gaps[0].ID != g.ID || gaps[0].LastTryMS != 200 {
		t.Fatalf("桥接合并丢区间或重试信息: %+v", gaps)
	}
	g = gaps[0]
	g.State = "abandoned"
	if err := s.UpdateChannelGap(ctx, g); err != nil {
		t.Fatal(err)
	}
	add(0, 9)
	add(10, 12)
	gaps, err = s.ChannelGaps(ctx, "qq")
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 2 || gaps[0].State != "abandoned" || gaps[0].Tries != 2 || gaps[1].FromMS != 10 {
		t.Fatalf("合并复活终局记录: %+v", gaps)
	}
}

// 合并结果撞上同范围的终局行时不能把参与合并的 open 行删掉
func TestChannelGapMergeKeepsOpenOnTerminalClash(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	s, err := set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	add := func(lo, hi int64) {
		t.Helper()
		if err := s.RecordChannelGap(ctx, ChannelGap{Channel: "qq", Conversation: "qq:group:801", FromMS: lo, ToMS: hi, Reason: "page-cap", LastTryMS: 100}); err != nil {
			t.Fatal(err)
		}
	}
	add(20, 29)
	gaps, err := s.ChannelGaps(ctx, "qq")
	if err != nil {
		t.Fatal(err)
	}
	g := gaps[0]
	g.State = "abandoned"
	if err := s.UpdateChannelGap(ctx, g); err != nil {
		t.Fatal(err)
	}
	add(20, 24)
	add(26, 29)
	add(25, 25) // 桥接后正好是 20～29，和放弃的那行同范围
	gaps, err = s.ChannelGaps(ctx, "qq")
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, g := range gaps {
		if g.State == "open" {
			open++
		}
	}
	if open != 2 {
		t.Fatalf("参与合并的 open 行被删了：%+v", gaps)
	}
}

// 已填平、已放弃的缺口不能被旧副本改回去；不存在的 id 也要报错，不能悄悄成功
func TestUpdateChannelGapRefusesSettledRows(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	s, err := set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.RecordChannelGap(ctx, ChannelGap{Channel: "qq", Conversation: "qq:group:802", FromMS: 10, ToMS: 19, Reason: "page-cap", LastTryMS: 100}); err != nil {
		t.Fatal(err)
	}
	gaps, err := s.ChannelGaps(ctx, "qq")
	if err != nil || len(gaps) != 1 {
		t.Fatalf("缺口 %+v %v", gaps, err)
	}
	stale := gaps[0] // 补录轮次读到的旧副本
	if _, err := s.Events.ExecContext(ctx, `UPDATE channel_gaps SET state='filled' WHERE id=?`, stale.ID); err != nil {
		t.Fatal(err)
	}
	stale.State, stale.Tries = "open", 5
	if err := s.UpdateChannelGap(ctx, stale); err == nil {
		t.Fatal("已填平的缺口被旧副本改回了 open")
	}
	var state string
	if err := s.Events.QueryRowContext(ctx, `SELECT state FROM channel_gaps WHERE id=?`, stale.ID).Scan(&state); err != nil || state != "filled" {
		t.Fatalf("已填平的行被改了：%q %v", state, err)
	}
	if err := s.UpdateChannelGap(ctx, ChannelGap{ID: 9999, Conversation: "qq:group:802", State: "abandoned"}); err == nil {
		t.Fatal("不存在的 id 没报错")
	}
}
