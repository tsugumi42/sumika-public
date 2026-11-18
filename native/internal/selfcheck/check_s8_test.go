package selfcheck_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"sumika-native/internal/paths"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
)

// chatDB 在临时目录建一个 qq 聊天库：读写句柄造数据，只读 Querier 给自检
func chatDB(t *testing.T, platform string) (*sql.DB, selfcheck.Querier) {
	t.Helper()
	file := filepath.Join(t.TempDir(), platform+".db")
	rw, err := store.OpenChatFile(file, platform)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rw.Close() })
	ro, err := store.OpenReadOnlyFile(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	return rw, ro
}

var idSeq int

func addEvents(t *testing.T, db *sql.DB, conv string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		idSeq++
		mustExec(t, db, `INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, created_at) VALUES (?, 'qq', ?, 'user', 'u', 'message', 'x', 1)`,
			fmt.Sprintf("e-%d", idSeq), conv)
	}
}

type s8env struct {
	t   *testing.T
	c   selfcheck.Check
	kv  *selfcheck.KV
	dbs map[string]selfcheck.Querier
}

func newS8(t *testing.T, dbs map[string]selfcheck.Querier) *s8env {
	return &s8env{t: t, c: findCheck(t, "events.seq_gap", selfcheck.DefaultThresholds()), kv: &selfcheck.KV{}, dbs: dbs}
}

func (s *s8env) eval() selfcheck.Result {
	s.t.Helper()
	e := envAt(t0, selfcheck.NewCounters())
	e.KV = s.kv
	e.Events = func() map[string]selfcheck.Querier { return s.dbs }
	return s.c.Eval(context.Background(), e)
}

// B6：历史上有缺口 → 基线记下、不报；水位以后新出现缺口 → 报 bad；同一天之后仍是 bad
func TestS8HistoricalGapIsBaselineNewGapIsReported(t *testing.T) {
	rw, ro := chatDB(t, "qq")
	addEvents(t, rw, "qq:group:1", 10)
	mustExec(t, rw, `DELETE FROM events WHERE seq = 4`) // 历史上就缺的一个
	s := newS8(t, map[string]selfcheck.Querier{"qq": ro})
	if r := s.eval(); r.State != selfcheck.Ok {
		t.Fatalf("历史缺口只做基线，不报：%+v", r)
	}
	if v, ok := s.kv.Get("baseline.seqGapsBefore"); !ok || v != 1 {
		t.Fatalf("历史缺口数应记 1：%d %v", v, ok)
	}
	addEvents(t, rw, "qq:group:2", 5) // 正常新增，连续
	if r := s.eval(); r.State != selfcheck.Ok {
		t.Fatalf("新增连续的不报：%+v", r)
	}
	addEvents(t, rw, "qq:group:1", 4)
	mustExec(t, rw, `DELETE FROM events WHERE seq = 17`) // 水位以后新出现的缺口
	r := s.eval()
	if r.State != selfcheck.Bad || r.Value != 1 || r.Example["platform"] != "qq" || r.Example["missing"] != "1" {
		t.Fatalf("新缺口应 bad：%+v", r)
	}
	if r := s.eval(); r.State != selfcheck.Bad || r.Example["platform"] != "qq" {
		t.Fatalf("当天已发现的新缺口之后几轮仍是 bad（巡检才看得到），例子保留：%+v", r)
	}
	if v, _ := s.kv.Get("baseline.seqGapsBefore"); v != 1 {
		t.Fatalf("新缺口不该混进历史基线：%d", v)
	}
}

// 多个会话交错写同一个库，单个会话的 seq 不连续是正常的，不报（规格字面按会话判会常红）
func TestS8InterleavedConversationsAreNotGaps(t *testing.T) {
	rw, ro := chatDB(t, "qq")
	s := newS8(t, map[string]selfcheck.Querier{"qq": ro})
	s.eval()
	for i := 0; i < 5; i++ {
		addEvents(t, rw, "qq:group:1", 1)
		addEvents(t, rw, "qq:group:2", 1)
		if r := s.eval(); r.State != selfcheck.Ok {
			t.Fatalf("交错写入第 %d 轮：%+v", i, r)
		}
	}
}

// 事务回滚不留洞：AUTOINCREMENT 的计数在同一个事务里
func TestRollbackLeavesNoGap(t *testing.T) {
	rw, ro := chatDB(t, "qq")
	addEvents(t, rw, "qq:group:1", 3)
	s := newS8(t, map[string]selfcheck.Querier{"qq": ro})
	s.eval()
	tx, err := rw.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, created_at) VALUES ('rb', 'qq', 'c', 'user', 'u', 'message', 'x', 1)`); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	addEvents(t, rw, "qq:group:1", 2)
	if r := s.eval(); r.State != selfcheck.Ok {
		t.Fatalf("回滚的写入不该留下断号：%+v", r)
	}
}

// 状态文件没有水位（新装、读坏了）：重新做历史基线，不许把它当新缺口报
func TestS8LostWatermarkRebuildsBaselineWithoutAlarm(t *testing.T) {
	rw, ro := chatDB(t, "qq")
	addEvents(t, rw, "qq:group:1", 6)
	mustExec(t, rw, `DELETE FROM events WHERE seq IN (2, 5)`)
	s := newS8(t, map[string]selfcheck.Querier{"qq": ro})
	s.eval()
	s.kv = &selfcheck.KV{Fresh: true} // 状态文件丢了
	if r := s.eval(); r.State != selfcheck.Ok {
		t.Fatalf("丢了水位重做基线，不能红：%+v", r)
	}
	if v, _ := s.kv.Get("baseline.seqGapsBefore"); v != 2 {
		t.Fatalf("历史缺口 2 个：%d", v)
	}
}

// 空库、没接库、库关了查不动
func TestS8EmptyAndUnknown(t *testing.T) {
	_, ro := chatDB(t, "qq")
	s := newS8(t, map[string]selfcheck.Querier{"qq": ro})
	for i := 0; i < 2; i++ {
		if r := s.eval(); r.State != selfcheck.Ok {
			t.Fatalf("空库第 %d 轮：%+v", i, r)
		}
	}
	if r := newS8(t, nil).eval(); r.State != selfcheck.Unknown {
		t.Fatalf("没接聊天库：%+v", r)
	}
	rw2, ro2 := chatDB(t, "qq")
	rw2.Close()
	ro2.(*sql.DB).Close()
	if r := newS8(t, map[string]selfcheck.Querier{"qq": ro2}).eval(); r.State != selfcheck.Unknown {
		t.Fatalf("库关了：%+v", r)
	}
}

// 走真实写入路径（store.AppendEvent）：写库失败（触发器拦下）、唯一键冲突都不消耗序号，不留断号；
// 库里没有任何地方删 events（非测试代码里只有两处 INSERT，没有 DELETE），所以序号一直连续，S8 不会因正常写入变红
func TestRealWritePathsLeaveNoGap(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ro, err := store.OpenReadOnlyFile(p.BunEventsDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	s := newS8(t, map[string]selfcheck.Querier{"qq": ro})
	if r := s.eval(); r.State != selfcheck.Ok {
		t.Fatalf("空库基线：%+v", r)
	}
	ev := store.NewEvent{Channel: "qq", Conversation: "qq:group:1", ActorKind: "human", ActorID: "u", Origin: "peer", Kind: "message", Text: "x", CreatedAt: 1}
	for i := 0; i < 3; i++ {
		if _, err := st.AppendEvent(ev, []string{"c1"}); err != nil {
			t.Fatal(err)
		}
	}
	// 写库失败：触发器在插入前拦下
	mustExec(t, st.Events, `CREATE TRIGGER boom BEFORE INSERT ON events WHEN NEW.text = 'boom' BEGIN SELECT RAISE(ABORT, '模拟写不进'); END`)
	bad := ev
	bad.Text = "boom"
	if _, err := st.AppendEvent(bad, nil); err == nil {
		t.Fatal("应写库失败")
	}
	// 唯一键冲突（同一个 id 写两遍）：第二次失败
	ins := `INSERT INTO events (id, channel, conversation, actor_kind, actor_id, kind, text, created_at) VALUES ('dup', 'qq', 'c', 'human', 'u', 'message', 'x', 1)`
	mustExec(t, st.Events, ins)
	if _, err := st.Events.Exec(ins); err == nil {
		t.Fatal("同一个 id 写两遍应失败")
	}
	mustExec(t, st.Events, `DROP TRIGGER boom`)
	for i := 0; i < 3; i++ {
		if _, err := st.AppendEvent(ev, []string{"c1"}); err != nil {
			t.Fatal(err)
		}
	}
	if r := s.eval(); r.State != selfcheck.Ok {
		t.Fatalf("正常写入加失败的写入都不该造成断号：%+v", r)
	}
	var gap int64
	rows, err := ro.QueryContext(context.Background(), `SELECT COALESCE(MAX(seq), 0) - COUNT(*) FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		rows.Scan(&gap)
	}
	rows.Close()
	if gap != 0 {
		t.Fatalf("最大 seq 减行数应为 0（没有断号）：%d", gap)
	}
}
