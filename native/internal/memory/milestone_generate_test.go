package memory

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/testutil/sqlitedump"
)

// 记忆库里程碑的 SQL 和 store 的放在一起（internal/store/testdata/milestones），
// 生成用：cd native && go test ./internal/memory -run TestGenerateMilestones -update
var updateMemoryMilestones = flag.Bool("update", false, "重写 internal/store/testdata/milestones 里的冻结记忆库 SQL")

type memoryMilestone struct {
	Name    string
	Desc    string
	Migrate bool // true = 跑完写入侧迁移（goose 0003）
}

func memoryMilestones() []memoryMilestone {
	return []memoryMilestone{
		{Name: "memory-bun", Desc: "Bun 时代的角色记忆库：只有基础五张表，没有 goose 表"},
		{Name: "memory-v3", Desc: "Bun 基础表加上写入侧迁移 0001～0003（goose 0003，没有 sumika_schema）", Migrate: true},
	}
}

func TestGenerateMilestones(t *testing.T) {
	if !*updateMemoryMilestones {
		t.Skip("要重写冻结的里程碑 SQL 时加 -update")
	}
	dir := filepath.Join("..", "store", "testdata", "milestones")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range memoryMilestones() {
		t.Run(m.Name, func(t *testing.T) {
			text, err := buildMemoryMilestone(t, m)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, m.Name+".sql"), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func buildMemoryMilestone(t *testing.T, m memoryMilestone) (string, error) {
	t.Helper()
	file := filepath.Join(t.TempDir(), m.Name+".db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		return "", err
	}
	if _, err := db.Exec(BaseSchema); err != nil {
		db.Close()
		return "", err
	}
	if _, err := db.Exec(memoryMilestoneSeed); err != nil {
		db.Close()
		return "", err
	}
	if err := db.Close(); err != nil {
		return "", err
	}
	if m.Migrate {
		if err := Migrate(context.Background(), file); err != nil {
			return "", err
		}
	}
	db, err = sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		return "", err
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE goose_db_version SET tstamp = '2026-11-02 00:00:00'`); err != nil && !isMissingTable(err) {
		return "", err
	}
	dump, err := sqlitedump.Dump(db)
	if err != nil {
		return "", err
	}
	header := fmt.Sprintf("-- 冻结的里程碑：%s\n-- 生成：cd native && go test ./internal/memory -run TestGenerateMilestones -update\n-- 数据全是编的；名字、号码不是真实用户或消息。\n", m.Desc)
	return header + dump, nil
}

func isMissingTable(err error) bool {
	return strings.Contains(err.Error(), "no such table")
}

const memoryMilestoneSeed = `
INSERT INTO facts (id, kind, text, subject, visibility, status, created_at, scope) VALUES
('fact-编造-1', 'fact', '编造的事实一', 'owner', 'owner', 'active', 1000, 'owner'),
('fact-编造-2', 'preference', '编造的事实二', 'owner', 'owner', 'active', 2000, 'owner'),
('fact-编造-3', 'fact', '编造的事实三', '编造甲', 'owner', 'active', 3000, 'owner');
INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at, status) VALUES
('episode-编造-1', 'local:topic:编造', '编造的片段一', 'owner', 1, 2, 1000, 'active'),
('episode-编造-2', 'local:topic:编造', '编造的片段二', 'owner', 3, 4, 2000, 'active');
INSERT INTO fact_revisions (fact_id, op, at, by, reason) VALUES
('fact-编造-1', 'create', 1000, '编造角色', '编造的创建'),
('fact-编造-2', 'confirm', 2000, 'owner', '编造的确认');
INSERT INTO summary_state (conversation, last_seq, updated_at) VALUES
('local:topic:编造', 10, 1000),
('qq:group:编造群', 20, 2000);
INSERT INTO memory_jobs (conversation, from_seq, to_seq, version, done_at) VALUES
('local:topic:编造', 1, 10, 1, 1000),
('qq:group:编造群', 1, 20, 1, 2000);
`
