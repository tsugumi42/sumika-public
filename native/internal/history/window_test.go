package history_test

import (
	"fmt"
	"testing"

	"sumika-native/internal/history"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func TestAnchoredStart(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ total, n, want int }{
		{0, 30, 0}, {10, 30, 0}, {30, 30, 0}, {31, 30, 0}, {59, 30, 0}, {60, 30, 30}, {89, 30, 30}, {90, 30, 60}, {5, 0, 0}, {5, -1, 0},
	} {
		if got := history.AnchoredStart(c.total, c.n); got != c.want {
			t.Errorf("AnchoredStart(%d, %d) = %d，应是 %d", c.total, c.n, got, c.want)
		}
		if c.n > 0 && c.total >= c.n {
			if kept := c.total - history.AnchoredStart(c.total, c.n); kept < c.n || kept >= 2*c.n {
				t.Errorf("total=%d 留下 %d 条，不在 [n, 2n)", c.total, kept)
			}
		}
	}
}

// 新消息进来时窗口起点不动，攒够 n 条整体跳 n 条；不算数的（通知、没说出口的、这一批）不占编号
func TestMessageWindowAnchored(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const conv, n = "qq:group:w", 4
	var seqs []int64
	add := func(kind, origin string) int64 {
		t.Helper()
		s, err := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "p", Origin: origin, Kind: kind, Text: fmt.Sprint(len(seqs)), CreatedAt: int64(len(seqs) + 1)}, []string{"c"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	first := func(skip []int64) int64 {
		t.Helper()
		w, err := history.MessageWindow(st.Events, conv, n, skip)
		if err != nil || len(w) == 0 {
			t.Fatalf("窗口是空的：%v", err)
		}
		return w[0].Seq
	}
	for i := 0; i < 7; i++ { // 0..6：一共 7 条算数的，起点在第 0 条
		seqs = append(seqs, add("message", "human"))
	}
	add("notice", "system")              // 不算数
	add("message", "automation:dry-run") // 没说出口，不算数
	if got := first(nil); got != seqs[0] {
		t.Fatalf("7 条（n=4）应从第 0 条起：%d", got)
	}
	batch := add("message", "human") // 这一批（skip）不算数：仍是 7 条
	if got := first([]int64{batch}); got != seqs[0] {
		t.Fatalf("这一批不占编号，起点不该动：%d", got)
	}
	// 第 8 条算数的进来：8 条，跳到第 4 条
	if got := first(nil); got != seqs[4] {
		t.Fatalf("够 2n 条应整体跳到第 4 条：%d（应是 %d）", got, seqs[4])
	}
}

// WindowFloor：重建最早可能带到的那条。比现在的窗口起点早一个跳点（留余量），没有消息是 0；
// 总数不到 2n 时起点是第 0 条，所以下限也是第 0 条那条
func TestWindowFloor(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const conv, n = "qq:group:wf", 4
	if got, err := history.WindowFloor(st.Events, "c", conv, n); err != nil || got != 0 {
		t.Fatalf("没有消息应是 0：%d %v", got, err)
	}
	var seqs []int64
	for i := 0; i < 20; i++ {
		s, err := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "p", Origin: "human", Kind: "message", Text: fmt.Sprint(i), CreatedAt: int64(i + 1)}, []string{"c"})
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, s)
		floor, err := history.WindowFloor(st.Events, "c", conv, n)
		if err != nil {
			t.Fatal(err)
		}
		w, err := history.MessageWindow(st.Events, conv, n, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(w) == 0 {
			t.Fatal("窗口是空的")
		}
		if floor > w[0].Seq {
			t.Fatalf("共 %d 条：下限 %d 不能比窗口起点 %d 更晚", i+1, floor, w[0].Seq)
		}
		// 下一轮重建会把它那一批（最多 n-1 条）排除在计数之外：不排除时的下限，不能比排除后的窗口起点更晚
		for skip := 1; skip < n && skip <= i; skip++ {
			w2, err := history.MessageWindow(st.Events, conv, n, seqs[len(seqs)-skip:])
			if err != nil || len(w2) == 0 {
				continue
			}
			if floor > w2[0].Seq {
				t.Fatalf("共 %d 条、下一批 %d 条：下限 %d 比重建实际的起点 %d 更晚，这之前的块会被误清", i+1, skip, floor, w2[0].Seq)
			}
		}
	}
	if floor, _ := history.WindowFloor(st.Events, "c", conv, n); floor <= seqs[0] {
		t.Fatalf("20 条以后窗口起点早已往后跳，下限应晚于第一条：%d", floor)
	}
}

// CountBefore 的口径和窗口一样：通知、没说出口的、skip 里的不算数；before <= 0 是 0 条；找不到快照报错
func TestCountBefore(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	const conv = "qq:group:cb"
	add := func(kind, origin string) int64 {
		t.Helper()
		s, err := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "p", Origin: origin, Kind: kind, Text: "x", CreatedAt: 1}, []string{"c"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := add("message", "human"), add("message", "human")
	add("notice", "human")
	add("message", "automation:dry-run")
	c := add("message", "human")
	for _, tc := range []struct {
		before int64
		skip   []int64
		want   int
	}{{0, nil, 0}, {-3, nil, 0}, {a, nil, 0}, {c, nil, 2}, {c + 100, nil, 3}, {c + 100, []int64{b}, 2}} {
		got, err := history.CountBefore(st.Events, conv, tc.before, tc.skip)
		if err != nil || got != tc.want {
			t.Errorf("CountBefore(%d, %v) = %d, %v，应是 %d", tc.before, tc.skip, got, err, tc.want)
		}
	}
	if _, err := history.EpochFromSeq(st.Events, "c", conv, "没有"); err == nil {
		t.Error("找不到的快照要报错")
	}
}
