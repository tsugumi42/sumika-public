package store

// readNotices 的行为测试：撤回和回应分开、没观察到的通知不漏出来；
// 量测（SUMIKA_SCALE=1 才跑）对比原来的两条查询。数据全是编的，临时库。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// 同一页里被撤回的、被贴了表情的、两样都有的各自分对；别的会话、没观察到的、没有表情编号的通知都不算
func TestReadNoticesSplitsRecallAndReaction(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	msg := func(c, text string, at int64, obs []string) string {
		t.Helper()
		seq, err := qq.AppendEvent(NewEvent{Channel: "qq", Conversation: c, ActorKind: "human", ActorID: "90001", Kind: "message", Text: text, CreatedAt: at}, obs)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err := qq.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	notice := func(c, target, kind, ref string, at int64, actor string, obs []string) string {
		t.Helper()
		seg := map[string]any{"notice": kind}
		if ref != "" {
			seg["ref"] = ref
		}
		seq, err := qq.AppendEvent(NewEvent{Channel: "qq", Conversation: c, ActorKind: "human", ActorID: actor, Kind: "notice", CreatedAt: at, ReplyTo: &target, Segments: seg}, obs)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err := qq.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	only := msg(conv, "只被回应", 100, []string{"a"})
	gone := msg(conv, "被撤回", 110, []string{"a"})
	both := msg(conv, "又回应又撤回", 120, []string{"a"})
	plain := msg(conv, "什么都没有", 130, []string{"a"})
	r1 := notice(conv, only, "reaction", "76", 200, "90011", []string{"a"})
	r2 := notice(conv, only, "reaction", "12", 210, "90012", []string{"a"})
	notice(conv, only, "reaction", "", 215, "90013", []string{"a"})           // 没有表情编号：认不出是哪个，不算
	notice(conv, only, "reaction", "99", 220, "90014", nil)                   // 没人观察到的回应：看不见
	notice(conv, only, "poke", "", 225, "90015", []string{"a"})               // 别的通知不算
	notice("qq:group:90009", only, "reaction", "55", 230, "90016", nil)       // 别的会话里指着同一个 id 的，不算
	notice("qq:group:90009", gone, "recall", "", 231, "90016", []string{"a"}) // 同上，撤回也不算
	notice(conv, gone, "recall", "", 240, "90001", []string{"a"})
	r3 := notice(conv, both, "reaction", "9731", 250, "90011", []string{"a"})
	notice(conv, both, "recall", "", 260, "90001", []string{"a"})
	scope := DisplayScope{Platform: "qq", Conversation: conv, Characters: []string{"a"}}
	rows, err := qq.ReadDisplayLoaded(context.Background(), scope, []string{only})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]DisplayRow{}
	for _, r := range rows {
		by[r.ID] = r
	}
	ids := func(rs []DisplayReaction) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ID+":"+r.Ref)
		}
		return out
	}
	if got, want := ids(by[only].Reactions), []string{r1 + ":76", r2 + ":12"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("只被回应：got %v want %v", got, want)
	}
	if by[only].Recalled || !by[gone].Recalled || !by[both].Recalled || by[plain].Recalled {
		t.Fatalf("撤回分错：only=%v gone=%v both=%v plain=%v", by[only].Recalled, by[gone].Recalled, by[both].Recalled, by[plain].Recalled)
	}
	if got, want := ids(by[both].Reactions), []string{r3 + ":9731"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("又回应又撤回：got %v want %v", got, want)
	}
	if len(by[gone].Reactions) != 0 || len(by[plain].Reactions) != 0 {
		t.Fatalf("没被回应的不该有回应：%v %v", by[gone].Reactions, by[plain].Reactions)
	}
	// 观察范围是角色 b：什么都看不到（通知也要有人观察到才算）
	other := scope
	other.Characters = []string{"b"}
	other.Character = ""
	if rows, err := qq.ReadDisplayLoaded(context.Background(), other, []string{only}); err != nil || len(rows) != 0 {
		t.Fatalf("b 没观察到任何事件：%v %v", rows, err)
	}
}

// 量测：一个会话里有 n 万条事件、一些撤回和回应，对比原来的两条查询和现在的一条（ReadDisplayLoaded 每 200 行一块各调一次）
func TestNoticesScaleMeasure(t *testing.T) {
	t.Parallel()
	if os.Getenv("SUMIKA_SCALE") == "" {
		t.Skip("量测：SUMIKA_SCALE=1 才跑")
	}
	set, _ := goSet(t)
	qq, _ := set.Platform("qq")
	ctx := context.Background()
	for _, n := range []int{20000, 100000, 300000} {
		conv := fmt.Sprintf("qq:group:%d", 80000+n/1000)
		tx, err := qq.Events.Begin()
		if err != nil {
			t.Fatal(err)
		}
		// n 条消息；每 20 条里 1 条被贴一个回应、每 50 条里 1 条被撤回
		if _, err = tx.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<?)
			INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,created_at)
			SELECT ?||'#m'||i,'qq',?,'human','90001','message','编造正文',1000+i FROM n`, n-1, conv, conv); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+20 FROM n WHERE i+20<=?)
			INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,segments_json,reply_to,created_at)
			SELECT ?||'#r'||i,'qq',?,'human','90002','notice','reaction','{"notice":"reaction","ref":"76"}',?||'#m'||i,?+i FROM n`, n-1, conv, conv, conv, 5000000); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+50 FROM n WHERE i+50<=?)
			INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,segments_json,reply_to,created_at)
			SELECT ?||'#c'||i,'qq',?,'human','90001','notice','recall','{"notice":"recall"}',?||'#m'||i,?+i FROM n`, n-1, conv, conv, conv, 6000000); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO observations(character_id,event_seq) SELECT 'a',seq FROM events WHERE conversation=?`, conv); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		scope := DisplayScope{Platform: "qq", Conversation: conv, Characters: []string{"a"}}
		refs := map[string]bool{}
		for i := n - 200; i < n; i++ {
			refs[fmt.Sprintf("%s#m%d", conv, i)] = true
		}
		median := func(f func()) time.Duration {
			var ds []time.Duration
			for range 7 {
				t0 := time.Now()
				f()
				ds = append(ds, time.Since(t0))
			}
			sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
			return ds[3]
		}
		filter, fa := displayFilter(scope, "e")
		args := []any{scope.Conversation}
		for id := range refs {
			args = append(args, id)
		}
		args = append(args, fa...)
		marks := strings.TrimSuffix(strings.Repeat("?,", len(refs)), ",")
		drain := func(tx *sql.Tx, q string) {
			rows, err := tx.QueryContext(ctx, q, args...)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
			}
			rows.Close()
		}
		oldRecall := `SELECT e.reply_to FROM events e WHERE e.conversation=? AND e.reply_to IN (` + marks + `) AND e.kind='notice' AND json_valid(e.segments_json) AND json_extract(e.segments_json,'$.notice')='recall' AND ` + filter
		oldReaction := `SELECT e.reply_to,e.id,e.actor_kind,e.actor_id,e.created_at,COALESCE(json_extract(e.segments_json,'$.ref'),'') FROM events e
			WHERE e.conversation=? AND e.reply_to IN (` + marks + `) AND e.kind='notice' AND json_valid(e.segments_json)
			AND json_extract(e.segments_json,'$.notice')='reaction' AND ` + filter + ` ORDER BY e.created_at,e.seq`
		inTx := func(f func(*sql.Tx)) {
			tx, err := qq.Events.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			f(tx)
		}
		before := median(func() { inTx(func(tx *sql.Tx) { drain(tx, oldRecall); drain(tx, oldReaction) }) })
		after := median(func() {
			inTx(func(tx *sql.Tx) {
				if _, _, err := readNotices(ctx, tx, scope, refs); err != nil {
					t.Fatal(err)
				}
			})
		})
		t.Logf("会话 %d 条事件：原来两条查询 %.1f ms，现在一条 %.1f ms", n, float64(before.Microseconds())/1000, float64(after.Microseconds())/1000)
	}
}
