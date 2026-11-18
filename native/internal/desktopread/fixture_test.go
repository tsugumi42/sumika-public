package desktopread

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 交错测试重复真实读写，不重复验证迁移。模板来自本次真实迁移且在复制前关库，
// 每个用例仍有独立的 t.TempDir、local/qq/trace 文件和连接，序号互不共享。
func fixtureFactory(t *testing.T) func(*testing.T) (*Service, *store.Set) {
	t.Helper()
	template, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(template, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	if err = set.Close(); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, source := range []string{template.ChatDB("local"), template.ChatDB("qq"), template.GoTraceDB()} {
		relative, err := filepath.Rel(template.Root, source)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		files[relative] = data
	}
	return func(t *testing.T) (*Service, *store.Set) {
		t.Helper()
		p, err := paths.Init(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		for relative, data := range files {
			destination := filepath.Join(p.Root, relative)
			if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destination, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		set, err := store.OpenGo(p, "local", "qq")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { set.Close() })
		return New(set, []api.CharacterView{{ID: "a", Name: "同名"}, {ID: "b", Name: "同名"}}, nil), set
	}
}

// 在查询前准备批量输入，临时触发器把整批放进真实 EndPart 事务；通知仍走
// commitPlanChanged。它只验证批量交错，逐笔 AppendEvent 另由 BurstReplay 覆盖。
func prepareBurst(t *testing.T, st *store.Store, conversation string, size int) []string {
	t.Helper()
	if _, err := st.Events.Exec(`PRAGMA temp_store=MEMORY; CREATE TEMP TABLE display_burst (id TEXT PRIMARY KEY, conversation TEXT, created_at INTEGER, ordinal INTEGER)`); err != nil {
		t.Fatal(err)
	}
	tx, err := st.Events.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.Prepare(`INSERT INTO display_burst(id,conversation,created_at,ordinal) VALUES (?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	ids := make([]string, size)
	for i := range size {
		at := int64(size + 4 - i)
		ids[i] = store.NewID(time.UnixMilli(at))
		if _, err = statement.Exec(ids[i], conversation, at, i); err != nil {
			t.Fatal(err)
		}
	}
	if err = statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Events.Exec(`CREATE TEMP TRIGGER commit_display_burst AFTER UPDATE OF state ON outbound_parts
	WHEN NEW.plan_id='plan' AND NEW.idx=0 AND NEW.state='aborted'
	BEGIN
	 INSERT INTO events(id,channel,conversation,actor_kind,actor_id,kind,text,created_at)
	 SELECT id,'qq',conversation,'human','90001','message','burst',created_at FROM display_burst ORDER BY ordinal;
	 INSERT INTO observations(character_id,event_seq)
	 SELECT 'a',e.seq FROM events e JOIN display_burst b ON b.id=e.id;
	END`); err != nil {
		t.Fatal(err)
	}
	return ids
}
