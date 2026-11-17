package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"sumika-native/internal/kernel"
)

// 0021：旧库（迁到 0020）里已有的行升级后指纹是 NULL；新写的行存下指纹，同一个 attempt 后写的一次没带指纹时不清掉先写的
// （modelcall 和回合各写一次，谁先谁后都不能丢）
func TestRunPromptHashesUpgradeAndUpsert(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "trace.db")
	db, err := open(file, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, "migrations/trace", 20); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var cols int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('model_runs') WHERE name = 'prompt_hashes_json'`).Scan(&cols); err != nil || cols != 0 {
		db.Close()
		t.Fatalf("起点应是没有指纹列的旧库：%d，%v", cols, err)
	}
	if _, err := db.Exec(`INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, total_ms, input_tokens, output_tokens, cache_read_tokens, sections_json, injected_json)
		VALUES ('old', 'c', 'k', 'reply', 'm', 1, 1, 1, 1, 0, '[]', '[]')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenTraceFile(file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{Trace: db}
	got := func(where string, args ...any) sql.NullString {
		var v sql.NullString
		if err := db.QueryRow(`SELECT prompt_hashes_json FROM model_runs WHERE `+where, args...).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := got(`id = 'old'`); v.Valid {
		t.Fatalf("旧行升级后应是 NULL：%+v", v)
	}
	const fp = `{"v":1,"msgs":[["system",10,"0123456789abcdef"]]}`
	for _, order := range [][2]string{{fp, ""}, {"", fp}} {
		attempt := "a-" + order[0] + "-" + order[1]
		id := kernel.Invocation{RootID: "r", RunID: "u", StepID: "s", AttemptID: attempt, Kind: "foreground"}
		for i, p := range order {
			run := Run{ID: attempt + string(rune('0'+i)), CharacterID: "c", Conversation: "k", Stage: "reply", Model: "m", StartedAt: 1, Identity: id, Now: 1, Prompt: p}
			if err := s.AddRun(run); err != nil {
				t.Fatal(err)
			}
		}
		if v := got(`attempt_id = ?`, attempt); v.String != fp {
			t.Fatalf("先后 %q：指纹丢了：%+v", order, v)
		}
	}
	// 没给指纹的行是 NULL，不是空串
	if err := s.AddRun(Run{ID: "plain", CharacterID: "c", Conversation: "k", Stage: "reply", Model: "m", StartedAt: 1, Now: 1}); err != nil {
		t.Fatal(err)
	}
	if v := got(`id = 'plain'`); v.Valid {
		t.Fatalf("没指纹应是 NULL：%+v", v)
	}
}
