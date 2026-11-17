package store

import (
	"reflect"
	"strings"
	"testing"
)

// 测试里的会话、号、名字都是编的（LESSONS：不用真实群名、昵称、号码）

func inbound(conv, platformID, text string, observers ...string) NewInbound {
	return NewInbound{
		Event:      NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:10001", Origin: "peer", Kind: "message", Text: text, CreatedAt: 1_700_000_000_000},
		PlatformID: platformID,
		Observers:  observers,
	}
}

func count(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Events.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 新迁移保留 Bun 基线的表和列，保证 CheckSchema 和既有事件读取仍兼容。
// 派生表（包含 FTS5 内部表）的名字不是兼容性契约，其维护行为由迁移测试覆盖。
func TestMigrationOnlyAddsTables(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	base, _, err := Baseline()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ColumnsOf(s.Events)
	if err != nil {
		t.Fatal(err)
	}
	for _, goOnly := range []string{"channel_msgs", "outbound_plans", "outbound_parts"} {
		if _, ok := base[goOnly]; ok {
			t.Fatalf("Baseline 不该含 %s：它是 Bun 版的镜像，真实库没有这张表", goOnly)
		}
	}
	for tbl, cols := range base {
		if !reflect.DeepEqual(got[tbl], cols) {
			t.Errorf("旧表 %s 被改了：基线 %v，现在 %v", tbl, cols, got[tbl])
		}
	}
	if err := s.CheckSchema(); err != nil {
		t.Fatalf("Go 自己的库过不了表结构检查：%v", err)
	}
}

// 同一个键到达两次：事件只存一条，"看到了"有两条（两个号各一条）；第二次的结果指向已有的那一条
func TestAppendInboundDedupAddsObservation(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	first, err := s.AppendInbound(inbound("qq:group:900", "5001", "你好", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || first.Seq == 0 || first.EventID == "" {
		t.Fatalf("第一次不该是重复：%+v", first)
	}
	second, err := s.AppendInbound(inbound("qq:group:900", "5001", "你好（另一个号收到的版本，内容可以不同）", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.Seq != first.Seq || second.EventID != first.EventID {
		t.Fatalf("第二次应该指向第一条：first=%+v second=%+v", first, second)
	}
	// 重复到达回报的是库里那一条的身份和来源（这一份交来的是别的，不能按这一份算）
	other := inbound("qq:group:900", "5001", "x", "b")
	other.Event.ActorKind, other.Event.ActorID, other.Event.Origin = "owner", "owner", "owner"
	if r, err := s.AppendInbound(other); err != nil || r.ActorKind != "human" || r.ActorID != "qq:10001" || r.Origin != "peer" {
		t.Fatalf("重复到达应该回报库里已有那条的身份：%+v %v", r, err)
	}
	if first.NewObservations != 1 || second.NewObservations != 1 {
		t.Fatalf("a、b 各是第一次看到：first=%d second=%d", first.NewObservations, second.NewObservations)
	}
	if n := count(t, s, `SELECT count(*) FROM events`); n != 1 {
		t.Fatalf("事件应该只有 1 条，有 %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM observations WHERE event_seq = ?`, first.Seq); n != 2 {
		t.Fatalf("看到了应该有 2 条（a、b 各一），有 %d", n)
	}
	// 同一个号再来一次（补录和实时重叠）：看到了不重复，也没有新记的
	if r, err := s.AppendInbound(inbound("qq:group:900", "5001", "你好", "a")); err != nil || r.NewObservations != 0 {
		t.Fatalf("早就记过的不算新记：%+v %v", r, err)
	}
	if n := count(t, s, `SELECT count(*) FROM observations`); n != 2 {
		t.Fatalf("同一个号重复来，看到了不该增加：%d", n)
	}
	// 第二次的内容（可以和第一次不同）不覆盖已有的事件
	var text string
	s.Events.QueryRow(`SELECT text FROM events WHERE seq = ?`, first.Seq).Scan(&text)
	if text != "你好" {
		t.Fatalf("已有事件被第二次的内容改了：%q", text)
	}
}

// 重复到达时带回库里已有那条的时间和是不是补录来的（接管时刻的缓存靠它精确更新，不整个作废）；
// 说的是库里的那条，不是这一份交来的；新存的那一次不填
func TestAppendInboundDuplicateCarriesExistingTimeAndBackfill(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	live := inbound("qq:group:900", "1", "实时来的", "a")
	live.Event.CreatedAt = 5000
	backfill := inbound("qq:group:900", "2", "补录来的", "a")
	backfill.Event.CreatedAt = 3000
	backfill.Event.Segments = map[string]any{"segs": []any{}, "tags": []string{"fromSelf", "backfill"}}
	tagged := inbound("qq:group:900", "3", "有别的标签、不是补录", "a")
	tagged.Event.CreatedAt = 4000
	tagged.Event.Segments = map[string]any{"tags": []string{"stranger"}}
	for _, in := range []NewInbound{live, backfill, tagged} {
		r, err := s.AppendInbound(in)
		if err != nil || r.Duplicate || r.DupCreatedAt != 0 || r.DupBackfill {
			t.Fatalf("新存的不填这两项：%+v %v", r, err)
		}
	}
	// 第二个号交来的这一份时间、标记都和已有的不同：回报的仍是库里那条的
	for _, c := range []struct {
		id       string
		at       int64
		backfill bool
	}{{"1", 5000, false}, {"2", 3000, true}, {"3", 4000, false}} {
		again := inbound("qq:group:900", c.id, "另一个号交来的", "b")
		again.Event.CreatedAt = 1
		if c.backfill {
			again.Event.Segments = nil // 这一份没有补录标记，库里那条有
		} else {
			again.Event.Segments = map[string]any{"tags": []string{"backfill"}} // 这一份是补录，库里那条不是
		}
		r, err := s.AppendInbound(again)
		if err != nil || !r.Duplicate || r.DupCreatedAt != c.at || r.DupBackfill != c.backfill || r.NewObservations != 1 {
			t.Errorf("号 %s：%+v %v，库里那条是 at=%d backfill=%v", c.id, r, err, c.at, c.backfill)
		}
	}
	// segments_json 不是合法 JSON 的行当作不是补录（和 FirstLiveAt 同一个口径）
	if _, err := s.Events.Exec(`UPDATE events SET segments_json = '{坏的' WHERE id = (SELECT e.id FROM events e WHERE e.created_at = 3000)`); err != nil {
		t.Fatal(err)
	}
	r, err := s.AppendInbound(inbound("qq:group:900", "2", "x", "c"))
	if err != nil || !r.Duplicate || r.DupBackfill || r.DupCreatedAt != 3000 {
		t.Fatalf("坏 JSON 的行：%+v %v", r, err)
	}
}

// 键是（渠道、会话、平台消息号）三样：号相同、别的不同，不是同一条
func TestAppendInboundKeyScope(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	a, _ := s.AppendInbound(inbound("qq:group:900", "7", "群 900", "a"))
	b, err := s.AppendInbound(inbound("qq:group:901", "7", "群 901", "a"))
	if err != nil || b.Duplicate || b.Seq == a.Seq {
		t.Fatalf("不同会话里的同一个号不是重复：%+v %v", b, err)
	}
	other := inbound("qq:group:900", "7", "另一个渠道", "a")
	other.Event.Channel = "tg"
	c, err := s.AppendInbound(other)
	if err != nil || c.Duplicate || c.Seq == a.Seq {
		t.Fatalf("不同渠道里的同一个号不是重复：%+v %v", c, err)
	}
	if n := count(t, s, `SELECT count(*) FROM events`); n != 3 {
		t.Fatalf("应该 3 条，有 %d", n)
	}
}

// 号为空或全是 0：不去重，每条都入库，也不进映射表（清单⑯）
func TestAppendInboundNoIDNoDedup(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	for i, id := range []string{"", "", "0", "0", "000"} {
		r, err := s.AppendInbound(inbound("qq:group:900", id, strings.Repeat("字", i+1), "a"))
		if err != nil || r.Duplicate {
			t.Fatalf("号 %q 的第 %d 条不该是重复：%+v %v", id, i, r, err)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM events`); n != 5 {
		t.Fatalf("5 条不同的消息都该入库，有 %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM channel_msgs`); n != 0 {
		t.Fatalf("没有号的消息不该进映射表，有 %d 行", n)
	}
	for id, want := range map[string]bool{"": false, "0": false, "00": false, "1": true, "10": true, "007": true, "-1": true} {
		if got := HasPlatformID(id); got != want {
			t.Errorf("HasPlatformID(%q) = %v，应该是 %v", id, got, want)
		}
	}
}

// 超过 2^53 的号往返不变，而且 2^53 和 2^53+1 是两条不同的消息（转成浮点会把它们并成一个）
func TestAppendInboundBigIDRoundTrip(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	ids := []string{"9007199254740992", "9007199254740993", "18446744073709551615", "0123"}
	for _, id := range ids {
		r, err := s.AppendInbound(inbound("qq:group:900", id, "x", "a"))
		if err != nil || r.Duplicate {
			t.Fatalf("号 %s 不该是重复：%+v %v", id, r, err)
		}
	}
	rows, err := s.Events.Query(`SELECT platform_id FROM channel_msgs ORDER BY event_seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		got = append(got, id)
	}
	if !reflect.DeepEqual(got, ids) {
		t.Fatalf("号往返变了：存进去 %v，读出来 %v", ids, got)
	}
}

// 事务：映射表写到一半失败，事件、看到了都不能留下（Bun 里是两次写入，中间崩了补录会再存一份）
func TestAppendInboundIsOneTransaction(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, table string }{{"映射表写失败", "channel_msgs"}, {"看到了写失败", "observations"}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s, _ := fresh(t)
			if _, err := s.Events.Exec(`CREATE TRIGGER boom BEFORE INSERT ON ` + c.table + ` BEGIN SELECT RAISE(ABORT, '模拟写入失败'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AppendInbound(inbound("qq:group:900", "5001", "不该留下", "a")); err == nil {
				t.Fatal("触发器会让写入失败，却没报错")
			}
			for _, tbl := range []string{"events", "channel_msgs", "observations"} {
				if n := count(t, s, `SELECT count(*) FROM `+tbl); n != 0 {
					t.Errorf("失败以后 %s 里还有 %d 行", tbl, n)
				}
			}
			// 失败不卡住后面的：拿掉触发器，同一条能正常入库
			if _, err := s.Events.Exec(`DROP TRIGGER boom`); err != nil {
				t.Fatal(err)
			}
			if r, err := s.AppendInbound(inbound("qq:group:900", "5001", "重来", "a")); err != nil || r.Duplicate {
				t.Fatalf("失败以后重来应该能入库：%+v %v", r, err)
			}
		})
	}
}

// 重复到达时"看到了"写失败：已有的事件和映射不受影响
func TestAppendInboundDuplicateFailureKeepsOriginal(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	first, _ := s.AppendInbound(inbound("qq:group:900", "5001", "原来的", "a"))
	if _, err := s.Events.Exec(`CREATE TRIGGER boom BEFORE INSERT ON observations BEGIN SELECT RAISE(ABORT, '模拟写入失败'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendInbound(inbound("qq:group:900", "5001", "原来的", "b")); err == nil {
		t.Fatal("应该报错")
	}
	if n := count(t, s, `SELECT count(*) FROM events WHERE seq = ?`, first.Seq); n != 1 {
		t.Fatal("已有的事件不见了")
	}
	if n := count(t, s, `SELECT count(*) FROM observations`); n != 1 {
		t.Fatalf("看到了应该还是原来的 1 条，有 %d", n)
	}
}

// 引用：被引用的那条库里有，reply_to 连上它的事件 id；没有就空着，不挡入库
func TestAppendInboundReplyLink(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	quoted, _ := s.AppendInbound(inbound("qq:group:900", "5001", "被引用的", "a"))
	r := inbound("qq:group:900", "5002", "回复", "a")
	r.ReplyPlatformID = "5001"
	got, err := s.AppendInbound(r)
	if err != nil {
		t.Fatal(err)
	}
	var replyTo *string
	s.Events.QueryRow(`SELECT reply_to FROM events WHERE seq = ?`, got.Seq).Scan(&replyTo)
	if replyTo == nil || *replyTo != quoted.EventID {
		t.Fatalf("reply_to 应该是 %s，是 %v", quoted.EventID, replyTo)
	}
	// 引用的那条不在库里：入库照常，reply_to 为空
	miss := inbound("qq:group:900", "5003", "引用了一条没收到的", "a")
	miss.ReplyPlatformID = "4999"
	got, err = s.AppendInbound(miss)
	if err != nil {
		t.Fatal(err)
	}
	replyTo = nil
	s.Events.QueryRow(`SELECT reply_to FROM events WHERE seq = ?`, got.Seq).Scan(&replyTo)
	if replyTo != nil {
		t.Fatalf("引用的那条没收到，reply_to 应该是空，是 %q", *replyTo)
	}
	// 别的会话里同号的那条不算
	cross := inbound("qq:group:901", "5004", "引用了别的群里同号的", "a")
	cross.ReplyPlatformID = "5001"
	got, _ = s.AppendInbound(cross)
	replyTo = nil
	s.Events.QueryRow(`SELECT reply_to FROM events WHERE seq = ?`, got.Seq).Scan(&replyTo)
	if replyTo != nil {
		t.Fatal("引用要在同一个会话里找，不能连到别的群的同号消息")
	}
}

// 渠道或会话是空的：拒绝，什么也不写
func TestAppendInboundRejectsEmptyKey(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	bad := inbound("", "1", "x", "a")
	if _, err := s.AppendInbound(bad); err == nil {
		t.Fatal("会话为空应该拒绝")
	}
	bad = inbound("qq:group:1", "1", "x", "a")
	bad.Event.Channel = ""
	if _, err := s.AppendInbound(bad); err == nil {
		t.Fatal("渠道为空应该拒绝")
	}
	if n := count(t, s, `SELECT count(*) FROM events`); n != 0 {
		t.Fatalf("拒绝以后库里还有 %d 条", n)
	}
}

// 实时接管的时刻：只看不是补录的事件，补录的（tags 里有 backfill）不算
func TestFirstLiveAt(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	if _, ok, err := s.FirstLiveAt("qq", "a"); err != nil || ok {
		t.Fatalf("还没有事件：ok=%v err=%v", ok, err)
	}
	add := func(id string, at int64, seg any, observer string) {
		t.Helper()
		in := inbound("qq:group:900", id, "x", observer)
		in.Event.CreatedAt = at
		in.Event.Segments = seg
		if _, err := s.AppendInbound(in); err != nil {
			t.Fatal(err)
		}
	}
	add("1", 1000, map[string]any{"tags": []string{"backfill"}}, "a") // 补录的，不算
	add("2", 5000, nil, "a")                                          // 没有 segments，算实时
	add("3", 3000, map[string]any{"segs": []any{}, "tags": []string{"fromSelf"}}, "a")
	add("4", 100, map[string]any{"tags": []string{"backfill", "stranger"}}, "a") // 补录的，不算
	add("5", 200, nil, "b")                                                      // 别的角色看到的，不算
	got, ok, err := s.FirstLiveAt("qq", "a")
	if err != nil || !ok || got != 3000 {
		t.Fatalf("a 的第一条实时事件应该是 3000：%d %v %v", got, ok, err)
	}
	if _, ok, _ := s.FirstLiveAt("tg", "a"); ok {
		t.Fatal("别的渠道没有事件")
	}
	// segments_json 不是合法 JSON 的行（别的写法留下的）当作不是补录，不能让整个查询报错
	if _, err := s.Events.Exec(`UPDATE events SET segments_json = '{坏的' WHERE id = (SELECT id FROM events WHERE created_at = 1000)`); err != nil {
		t.Fatal(err)
	}
	got, ok, err = s.FirstLiveAt("qq", "a")
	if err != nil || !ok || got != 1000 {
		t.Fatalf("坏 JSON 的行当作实时：%d %v %v", got, ok, err)
	}
}

// AppendEvent 拆出共用函数以后行为不变：事件和看到了在一个事务里，返回 seq
func TestAppendEventStillWorks(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	seq, err := s.AppendEvent(NewEvent{Channel: "console", Conversation: "console:a", ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "你好", Segments: map[string]any{"x": 1}, CreatedAt: 1_700_000_000_000}, []string{"a", "a"})
	if err != nil || seq != 1 {
		t.Fatalf("seq=%d err=%v", seq, err)
	}
	if n := count(t, s, `SELECT count(*) FROM observations WHERE event_seq = 1`); n != 1 {
		t.Fatalf("同一个角色记两次也只有一条看到了，有 %d", n)
	}
	var seg string
	s.Events.QueryRow(`SELECT segments_json FROM events WHERE seq = 1`).Scan(&seg)
	if seg != `{"x":1}` {
		t.Fatalf("segments_json = %q", seg)
	}
}
