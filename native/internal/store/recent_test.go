package store

import (
	"reflect"
	"testing"
	"time"
)

// RecentMessages、EventByPlatformID 的测试。会话、号、名字都是编的

func addMsg(t *testing.T, s *Store, conv, platformID, kind, actorKind, actorID, origin, text string, at int64, segs any) (seq int64, id string) {
	t.Helper()
	res, err := s.AppendInbound(NewInbound{
		Event:      NewEvent{Channel: "qq", Conversation: conv, ActorKind: actorKind, ActorID: actorID, Origin: origin, Kind: kind, Text: text, Segments: segs, CreatedAt: at},
		PlatformID: platformID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.Seq, res.EventID
}

func texts(rows []RecentRow, s *Store) []string {
	var out []string
	for _, r := range rows {
		var text string
		s.Events.QueryRow(`SELECT text FROM events WHERE seq = ?`, r.Seq).Scan(&text)
		out = append(out, text)
	}
	return out
}

func TestEventByPlatformID(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	seq, id := addMsg(t, s, "qq:group:1", "501", "message", "character", "ca", "character", "她说的", 1000, nil)
	addMsg(t, s, "qq:group:2", "501", "message", "human", "qq:10001", "peer", "别的群同一个号", 1000, nil)

	ref, ok, err := s.EventByPlatformID("qq", "qq:group:1", "501")
	if err != nil || !ok || ref != (EventRef{Seq: seq, ID: id, ActorKind: "character", ActorID: "ca", Origin: "character"}) {
		t.Fatalf("找得到：%+v %v %v", ref, ok, err)
	}
	// 会话、渠道、号任何一样对不上都找不到；空号和全是 0 的号永远找不到（它们本来就没进映射）
	for name, args := range map[string][3]string{
		"别的会话": {"qq", "qq:group:3", "501"},
		"别的渠道": {"wx", "qq:group:1", "501"},
		"别的号":  {"qq", "qq:group:1", "502"},
		"空号":   {"qq", "qq:group:1", ""},
		"零号":   {"qq", "qq:group:1", "000"},
	} {
		if _, ok, err := s.EventByPlatformID(args[0], args[1], args[2]); ok || err != nil {
			t.Fatalf("%s：不该找到 %v %v", name, ok, err)
		}
	}
}

func TestRecentMessages(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	conv := "qq:group:1"
	// 到达的先后（seq）和平台时间不一样：补录把更早的话晚存进来
	s1, _ := addMsg(t, s, conv, "1", "message", "human", "qq:10001", "peer", "一", 3000, map[string]any{"segs": []map[string]string{{"kind": "text", "text": "一"}}})
	addMsg(t, s, conv, "2", "message", "human", "qq:10002", "peer", "二", 4000, nil)
	addMsg(t, s, conv, "3", "message", "human", "qq:10001", "peer", "补录来的更早的", 1000, nil)     // seq 靠后、时间靠前
	addMsg(t, s, conv, "", "notice", "human", "qq:10003", "peer", "一条通知", 4500, nil)          // 通知不算
	addMsg(t, s, "qq:group:2", "9", "message", "human", "qq:10001", "peer", "别的群", 4000, nil) // 别的会话不算
	seq5, _ := addMsg(t, s, conv, "5", "message", "character", "ca", "automation:repeater", "她跟的", 5000, nil)
	// 同一毫秒的按 seq
	addMsg(t, s, conv, "6", "message", "human", "qq:10002", "peer", "六", 6000, nil)
	last, _ := addMsg(t, s, conv, "7", "message", "human", "qq:10001", "peer", "七", 6000, nil)

	all, err := s.RecentMessages(conv, last+1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"补录来的更早的", "一", "二", "她跟的", "六", "七"}; !reflect.DeepEqual(texts(all, s), want) {
		t.Fatalf("按平台时间排（同一毫秒按 seq）：%v", texts(all, s))
	}
	// 只取最近的 n 条：取的是时间上最近的，不是 seq 最大的
	if got := texts(mustRecent(t, s, conv, last+1, 3), s); !reflect.DeepEqual(got, []string{"她跟的", "六", "七"}) {
		t.Fatalf("最近 3 条：%v", got)
	}
	// beforeSeq 不含它自己，也不含更晚到的
	if got := texts(mustRecent(t, s, conv, last, 100), s); got[len(got)-1] != "六" {
		t.Fatalf("seq 小于 beforeSeq：%v", got)
	}
	if got := mustRecent(t, s, conv, seq5, 100); len(got) != 3 || got[1].Seq != s1 || got[2].Seq != s1+1 {
		t.Fatalf("beforeSeq 之前：%+v", got)
	}
	if got := mustRecent(t, s, conv, last+1, 0); got != nil {
		t.Fatalf("n 为 0 不取：%+v", got)
	}
	// 字段原样带出来
	var rep RecentRow
	for _, r := range all {
		if r.Seq == seq5 {
			rep = r
		}
	}
	if rep.ActorKind != "character" || rep.ActorID != "ca" || rep.Origin != "automation:repeater" || rep.CreatedAt != 5000 || rep.Segments.Valid {
		t.Fatalf("字段：%+v", rep)
	}
	if first := all[1]; !first.Segments.Valid || first.Segments.String != `{"segs":[{"kind":"text","text":"一"}]}` {
		t.Fatalf("段原样交出来：%+v", first)
	}
}

// 被撤回的（有一条 notice = recall 的通知指向它）不在里面；别的通知（戳一戳、表情回应）指向它不算
func TestRecentMessagesSkipsRecalled(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	conv := "qq:group:1"
	_, id1 := addMsg(t, s, conv, "1", "message", "human", "qq:10001", "peer", "说错了", 1000, nil)
	_, id2 := addMsg(t, s, conv, "2", "message", "human", "qq:10002", "peer", "被回应的", 2000, nil)
	_, id3 := addMsg(t, s, conv, "3", "message", "human", "qq:10002", "peer", "没人动的", 3000, nil)
	for _, n := range []struct {
		kind, key, target string
	}{{"recall", "n1", id1}, {"reaction", "n2", id2}, {"poke", "n3", id3}} {
		_, err := s.AppendInbound(NewInbound{
			Event:      NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:10009", Origin: "peer", Kind: "notice", Text: n.kind, Segments: map[string]string{"notice": n.kind}, CreatedAt: 4000, ReplyTo: &n.target},
			PlatformID: n.key,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := texts(mustRecent(t, s, conv, 100, 10), s); !reflect.DeepEqual(got, []string{"被回应的", "没人动的"}) {
		t.Fatalf("只有撤回算：%v", got)
	}
	// 撤回的通知在别的会话里，不影响这个会话（reply_to 是事件 id，不会撞，这里防的是查询忘了限定会话）
	if got := mustRecent(t, s, "qq:group:2", 100, 10); got != nil {
		t.Fatalf("别的会话：%+v", got)
	}
}

// 上万条事件的库里，取最近 30 条要快：撤回的查询走会话索引，不全表按 reply_to 扫
func TestRecentMessagesIsFastOnBigTable(t *testing.T) {
	t.Parallel()
	s, _ := fresh(t)
	// 3 万条一条语句灌进去（和 insertEvent 写的是同一张表、同样的列；一条条 Exec 要 7 秒左右，一条语句不到半秒）：
	// 这条测的是取最近 30 条用不用索引，不是写入
	if _, err := s.Events.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < 29999)
		INSERT INTO events (id, channel, conversation, actor_kind, actor_id, origin, kind, text, segments_json, reply_to, created_at)
		SELECT printf('big%06d', i), 'qq', 'qq:group:' || (i % 10), 'human', 'qq:1', 'peer', 'message', 'x', NULL, NULL, 1000 + i FROM n`); err != nil {
		t.Fatal(err)
	}
	// 全表扫的写法在这个规模上每次几秒；有索引是不到 1 毫秒。上限放宽到 100 毫秒，机器忙也不该碰到；
	// 一次超了就当场红（改坏的实现不用跑完 20 轮）
	const rounds = 20
	var total time.Duration
	for i := 0; i < rounds; i++ {
		start := time.Now()
		rows, err := s.RecentMessages("qq:group:3", 1<<40, 30)
		if err != nil || len(rows) != 30 {
			t.Fatalf("%d %v", len(rows), err)
		}
		d := time.Since(start)
		if d > 100*time.Millisecond {
			t.Fatalf("太慢了：第 %d 次 %v", i+1, d)
		}
		total += d
	}
	t.Logf("3 万条事件的库，取最近 30 条：每次 %v", total/rounds)
}

func mustRecent(t *testing.T, s *Store, conv string, before int64, n int) []RecentRow {
	t.Helper()
	rows, err := s.RecentMessages(conv, before, n)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
