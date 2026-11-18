package desktopread

import (
	"context"
	"errors"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/localgroup"
	"sumika-native/internal/store"
)

func addGapFixture(t *testing.T, st *store.Store, conv string, lo, hi int64) {
	t.Helper()
	if err := st.RecordChannelGap(context.Background(), store.ChannelGap{Channel: "qq", Conversation: conv, FromMS: lo, ToMS: hi, Reason: "outage", LastTryMS: 100}); err != nil {
		t.Fatal(err)
	}
}

// A01：四种场合、伪造 self、遗留群与观察筛选；缺口元数据不制造观察关系。
func TestGapReadScenesAndScope(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	defer s.Close()
	ctx := context.Background()
	qq, _ := set.For("qq")
	local, _ := set.For("console")
	groups := localgroup.New(local, nil, []api.CharacterView{{ID: "a"}, {ID: "b"}}, time.Now)
	defer groups.Close()
	s.WithLocalGroups(groups)
	group, err := s.CreateLocalGroup(ctx, api.LocalGroupCreate{Name: "编造空群", Characters: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	s.WithBackfills(BackfillProvider{Account: "91001", Project: GapProjection})
	for _, conv := range []string{"qq:group:92001", "qq:group:92002", "qq:private:91001:93001", "qq:private:91002:93001"} {
		addGapFixture(t, qq, conv, 10, 20)
	}
	add(t, qq, "qq:group:92001", "message", "human", 1, []string{"a"})
	add(t, qq, "qq:group:92002", "message", "human", 1, []string{"b"})
	for _, tc := range []struct {
		k    api.ConversationKey
		char string
		want int
		bad  bool
	}{
		{keyOf("console:a"), "", 0, false}, {group.Key, "", 0, false},
		{keyOf("qq:private:91001:93001"), "", 1, false}, {keyOf("qq:private:91002:93001"), "", 0, true},
		{keyOf("qq:group:92002"), "", 1, false}, {keyOf("qq:group:92002"), "a", 0, false}, {keyOf("qq:group:92001"), "a", 1, false}, {keyOf("qq:group:92001"), "pretender", 0, true},
	} {
		v, err := s.ReadGaps(ctx, api.GapQuery{ConversationKey: tc.k, Character: tc.char})
		if tc.bad {
			if err == nil {
				t.Errorf("未拒绝 %+v", tc)
			}
			continue
		}
		if err != nil || len(v.Value.Items) != tc.want {
			t.Errorf("场合 %+v: %+v %v", tc, v, err)
		}
	}
	conv := "qq:group:92001"
	addGapFixture(t, qq, conv, 30, 40)
	addGapFixture(t, qq, conv, 50, 60)
	q := api.GapQuery{ConversationKey: keyOf(conv), FromMS: 20, ToMS: 50, Limit: 1}
	first, err := s.ReadGaps(ctx, q)
	if err != nil || len(first.Value.Items) != 1 || !first.Value.HasMore || first.Value.Items[0].FromMS != 10 {
		t.Fatalf("相交第一页 %+v %v", first, err)
	}
	q.Cursor = first.Value.NextCursor
	second, err := s.ReadGaps(ctx, q)
	if err != nil || second.Value.Items[0].FromMS != 30 {
		t.Fatalf("第二页 %+v %v", second, err)
	}
	q.ConversationKey = keyOf("qq:group:92002")
	if _, err = s.ReadGaps(ctx, q); err == nil {
		t.Fatal("跨会话游标被接受")
	}
	q = api.GapQuery{ConversationKey: keyOf(conv), FromMS: 50, ToMS: 20}
	if _, err = s.ReadGaps(ctx, q); err == nil {
		t.Fatal("反向窗口被接受")
	}
}

// A03：只有缺口事务发生也必须刷新；关闭等待被阻塞的查询退出，不再发布。
func TestGapSubscriptionMergeAndLifecycle(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	defer s.Close()
	qq, _ := set.For("qq")
	ctx := context.Background()
	conv := "qq:group:92003"
	addGapFixture(t, qq, conv, 10, 20)
	addGapFixture(t, qq, conv, 30, 40)
	sub, err := s.WatchGaps(ctx, api.GapQuery{ConversationKey: keyOf(conv)})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	first := next(t, sub)
	if len(first.Value.Items) != 2 {
		t.Fatal(first)
	}
	var seq int64
	if err = qq.Events.QueryRow(`SELECT coalesce(max(seq),0) FROM events`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	addGapFixture(t, qq, conv, 21, 29)
	merged := next(t, sub)
	if merged.Version <= first.Version || len(merged.Value.Items) != 1 || merged.Value.Items[0].ID != first.Value.Items[0].ID || merged.Value.Items[0].ToMS != 40 {
		t.Fatalf("合并未替换旧行 %+v", merged)
	}
	var after int64
	if err = qq.Events.QueryRow(`SELECT coalesce(max(seq),0) FROM events`).Scan(&after); err != nil || after != seq {
		t.Fatal("缺口写入了消息", after, err)
	}
	sub.Close()
	select {
	case _, ok := <-sub.Updates():
		if ok {
			t.Fatal("Close 后仍发布")
		}
	case <-time.After(time.Second):
		t.Fatal("Close 后通道未关")
	}
	s.queryGate <- struct{}{}
	blocked, err := s.WatchGaps(ctx, api.GapQuery{ConversationKey: keyOf(conv)})
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("关闭未取消排队查询")
	}
	<-s.queryGate
	blocked.Close()
	select {
	case _, ok := <-blocked.Updates():
		if ok {
			t.Fatal("关闭后的查询发布旧账号")
		}
	default:
		t.Fatal("订阅未关闭")
	}
	if _, err = s.ReadGaps(ctx, api.GapQuery{ConversationKey: keyOf(conv)}); !errors.Is(err, ErrClosed) {
		t.Fatal("关闭后仍查库", err)
	}
}
