package app

// gate list 列"有消息的群"的查询（sqlGroupConversations）：
//   - 结果要和原来的 SELECT DISTINCT … GLOB 一模一样（边界形状都放进去比，短测试里跑）；
//   - 大库上要快：gate list 在宿主的 op 锁里跑，扫全表的话托盘开始 / 停止接收和别的控制命令都得排队（带 -short 跳过，造数据要几秒）。

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func oldGroupConversations(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.Events.Query(`SELECT DISTINCT conversation FROM events WHERE conversation GLOB 'qq:group:[0-9]*' ORDER BY conversation`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		rows.Scan(&c)
		out = append(out, c)
	}
	return out
}

func newGroupConversations(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.Events.Query(sqlGroupConversations)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		rows.Scan(&c)
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func insertConvs(t *testing.T, st *store.Store, convs ...string) {
	t.Helper()
	for i, c := range convs {
		if _, err := st.Events.Exec(`INSERT INTO events(id, channel, conversation, actor_kind, actor_id, kind, text, created_at, origin) VALUES (?, 'qq', ?, 'human', 'qq:1', 'message', '话', ?, 'peer')`,
			fmt.Sprintf("edge-%d", i), c, i); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGroupConversationQueryMatchesTheGlobItReplaced(t *testing.T) {
	t.Parallel()
	e := newGateEnv(t)
	qq, _ := e.set.Platform("qq")
	insertConvs(t, qq,
		"qq:group:", "qq:group:/", "qq:group:0", "qq:group:00", "qq:group:9", "qq:group:99999999999999999999", "qq:group:9zz", "qq:group:123abc",
		"qq:group::", "qq:group:;", "qq:group:a", "qq:group:A", "qq:group: 1", "qq:group:\x7f", "qq:groupp:1", "qq:grou:1", "qq:group", "qq:grouq:1",
		"qq:private:90001:80002", "qq:group:1:2", "QQ:group:1", "local:x", "", "z", "qq:group:5", "qq:group:5", "qq:group:50", "qq:group:6")
	got, want := newGroupConversations(t, qq), oldGroupConversations(t, qq)
	if len(want) < 8 || !reflect.DeepEqual(got, want) {
		t.Fatalf("和 GLOB 版结果不一样（或者造的数据太少）：\n新 %q\n旧 %q", got, want)
	}
	// 没有任何群消息的库：空
	p, err := paths.Init(filepath.Join(t.TempDir(), "空库"))
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	qq2, _ := set.Platform("qq")
	if g := newGroupConversations(t, qq2); len(g) != 0 {
		t.Fatalf("空库应是空：%q", g)
	}
}

// 大库：150 万条消息分在 80 个群里。原来的扫全表写法要 0.4 秒以上（220 万条实测 0.56～0.65 秒），现在是毫秒级；
// 上限放 0.15 秒——新写法离它很远，慢机器也不会红；旧写法放回去必红
func TestGateListStaysFastOnABigEventsTable(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("造 150 万行要几秒，-short 跳过；改 gate list 的查询时手动跑")
	}
	e := newGateEnv(t)
	qq, _ := e.set.Platform("qq")
	const n = 1_500_000
	if _, err := qq.Events.Exec(`INSERT INTO events(id, channel, conversation, actor_kind, actor_id, kind, text, created_at, origin)
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
SELECT 'big-'||i, 'qq', 'qq:group:'||(20000 + (i % 80)), 'human', 'qq:1', 'message', '话', i, 'peer' FROM n`, n); err != nil {
		t.Fatal(err)
	}
	var best time.Duration
	for k := 0; k < 3; k++ {
		began := time.Now()
		list, err := e.admin().list(context.Background())
		d := time.Since(began)
		if err != nil || len(list) != 80+3 { // 80 个造的群，加 newGateEnv 里的 gEvents、gNameOnly、gModeOnly
			t.Fatalf("%d 个群，%v", len(list), err)
		}
		if k == 0 || d < best {
			best = d
		}
	}
	t.Logf("%d 条消息、80 个群：gate list 最快一次 %v", n, best)
	if best > 150*time.Millisecond {
		t.Fatalf("gate list 在 %d 条消息上用了 %v（上限 150ms）：查询是不是又扫全表了？", n, best)
	}
}
