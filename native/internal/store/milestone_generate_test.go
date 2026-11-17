package store

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/testutil/sqlitedump"
)

// 里程碑 SQL 是冻结的历史建库结果，只由这个测试在 -update 时重写。
// 版本号怎么挑：2026-11-02 切 Go 那天用 git log --until="2026-11-02 23:59" --format=%H -1 -- \
// native/internal/store/migrations/<轨> 找到当天最后一个提交，再 ls-tree 看那个提交里的最大号；
// chat=0007、qq=0004、local=0001、trace=0006。中间版本取当天号与最新号的中点向上取整：
// chat=0012、qq=0006、local=0002、trace=0013；最新减一：chat=0016、qq=0007、local=0002、trace=0020。
var updateMilestones = flag.Bool("update", false, "重写 internal/store/testdata/milestones 里的冻结 SQL")

type storeMilestone struct {
	Name     string
	Desc     string
	Platform string // 空 = trace
	ChatTo   int64
	OwnTo    int64
	TraceTo  int64
	Seed     func(*sql.DB) error
}

func storeMilestones() []storeMilestone {
	qqCut, qqMid, qqLast := int64(4), int64(6), int64(7)
	localCut, localLast := int64(1), int64(2)
	return []storeMilestone{
		{Name: "platform-qq-chat0007-qq0004", Desc: "2026-11-02 切 Go 那天的 QQ 平台库：chat 0007、qq 0004", Platform: "qq", ChatTo: 7, OwnTo: qqCut, Seed: seedPlatform("qq")},
		{Name: "platform-qq-chat0012-qq0006", Desc: "QQ 平台库中间版本：chat 0012、qq 0006", Platform: "qq", ChatTo: 12, OwnTo: qqMid, Seed: seedPlatform("qq")},
		{Name: "platform-qq-chat0016-qq0007", Desc: "QQ 平台库最新减一：chat 0016、qq 0007", Platform: "qq", ChatTo: 16, OwnTo: qqLast, Seed: seedPlatform("qq")},
		{Name: "platform-local-chat0007-local0001", Desc: "2026-11-02 切 Go 那天的本地平台库：chat 0007、local 0001", Platform: "local", ChatTo: 7, OwnTo: localCut, Seed: seedPlatform("local")},
		{Name: "platform-local-chat0012-local0002", Desc: "本地平台库中间版本：chat 0012、local 0002", Platform: "local", ChatTo: 12, OwnTo: localLast, Seed: seedPlatform("local")},
		{Name: "platform-local-chat0016-local0002", Desc: "本地平台库最新减一：chat 0016、local 0002（local 轨中间和最新减一同号，保留两份不同 chat 版本）", Platform: "local", ChatTo: 16, OwnTo: localLast, Seed: seedPlatform("local")},
		{Name: "trace-0006", Desc: "2026-11-02 切 Go 那天的 trace 库：trace 0006", TraceTo: 6, Seed: seedTrace},
		{Name: "trace-0013", Desc: "trace 库中间版本：trace 0013", TraceTo: 13, Seed: seedTrace},
		{Name: "trace-0020", Desc: "trace 库最新减一：trace 0020", TraceTo: 20, Seed: seedTrace},
	}
}

func TestGenerateMilestones(t *testing.T) {
	if !*updateMilestones {
		t.Skip("要重写冻结的里程碑 SQL 时加 -update")
	}
	dir := filepath.Join("testdata", "milestones")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range storeMilestones() {
		t.Run(m.Name, func(t *testing.T) {
			text, err := buildStoreMilestone(t, m)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, m.Name+".sql")
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func buildStoreMilestone(t *testing.T, m storeMilestone) (string, error) {
	t.Helper()
	file := filepath.Join(t.TempDir(), m.Name+".db")
	db, err := open(file, false)
	if err != nil {
		return "", err
	}
	defer db.Close()
	if m.TraceTo > 0 {
		if err := migrateWith(db, "migrations/trace", m.TraceTo, ""); err != nil {
			return "", err
		}
	} else {
		if err := migrateWith(db, "migrations/chat", m.ChatTo, ""); err != nil {
			return "", err
		}
		if hasMigrations(m.Platform) {
			if err := migrateWith(db, "migrations/"+m.Platform, m.OwnTo, "goose_db_version_"+m.Platform); err != nil {
				return "", err
			}
		}
	}
	if m.Seed != nil {
		if err := m.Seed(db); err != nil {
			return "", err
		}
	}
	if err := freezeGooseTimestamps(db); err != nil {
		return "", err
	}
	dump, err := sqlitedump.Dump(db)
	if err != nil {
		return "", err
	}
	header := fmt.Sprintf("-- 冻结的里程碑：%s\n-- 生成：cd native && go test ./internal/store -run TestGenerateMilestones -update\n-- 数据全是编的；名字、号码不是真实用户或消息。\n", m.Desc)
	return header + dump, nil
}

// freezeGooseTimestamps 把 goose 自己的插入时间固定下来，重写 SQL 时不会因为运行日期产生无意义的差异。
func freezeGooseTimestamps(db *sql.DB) error {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'goose_db_version%'`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range names {
		if _, err := db.Exec(`UPDATE "` + name + `" SET tstamp = '2026-11-02 00:00:00'`); err != nil {
			return err
		}
	}
	return nil
}

func seedPlatform(platform string) func(*sql.DB) error {
	conv := "local:topic:编造"
	other := "local:topic:编造二"
	if platform == "qq" {
		conv = "qq:group:编造群"
		other = "qq:group:编造群二"
	}
	return func(db *sql.DB) error {
		events := fmt.Sprintf(`INSERT INTO events (seq, id, channel, conversation, actor_kind, actor_id, kind, text, created_at) VALUES
(1, 'evt-编造-1', %[1]s, %[2]s, 'human', 'u-编造-甲', 'message', '编造的第一句', 1000),
(2, 'evt-编造-2', %[1]s, %[2]s, 'character', '编造角色', 'message', '编造的第二句', 2000),
(3, 'evt-编造-3', %[1]s, %[2]s, 'human', 'u-编造-乙', 'message', '编造的第三句', 3000),
(4, 'evt-编造-4', %[1]s, %[3]s, 'human', 'u-编造-甲', 'message', '别的会话编造的话', 4000)`,
			sqlText(platform), sqlText(conv), sqlText(other))
		if _, err := db.Exec(events); err != nil {
			return err
		}
		if err := execIfTable(db, "chain_state", fmt.Sprintf(`INSERT INTO chain_state (conversation, last_seq) VALUES (%[1]s, 3), (%[2]s, 1)`, sqlText(conv), sqlText(other))); err != nil {
			return err
		}
		if err := execIfTable(db, "channel_msgs", fmt.Sprintf(`INSERT INTO channel_msgs (channel, conversation, platform_id, event_seq) VALUES
(%[1]s, %[2]s, 'platform-编造-1', 1), (%[1]s, %[3]s, 'platform-编造-2', 2)`, sqlText(platform), sqlText(conv), sqlText(other))); err != nil {
			return err
		}
		if err := execIfTable(db, "qq_people", `INSERT INTO qq_people (uid, nickname, last_seen) VALUES ('u-编造-甲', '编造甲', 1000), ('u-编造-乙', '编造乙', 2000)`); err != nil {
			return err
		}
		if err := execIfTable(db, "qq_alerts", `INSERT INTO qq_alerts (at, kind, account, text) VALUES (1000, 'down', 'u-编造-账号', '编造的掉线'), (2000, 'up', 'u-编造-账号', '编造的恢复')`); err != nil {
			return err
		}
		if err := execIfTable(db, "qq_sync", `INSERT INTO qq_sync (account, through) VALUES ('u-编造-账号', 100), ('u-编造-账号二', 200)`); err != nil {
			return err
		}
		if err := execIfTable(db, "local_topics", `INSERT INTO local_topics (id, character_id, title, created_at) VALUES ('topic-编造-1', '编造角色', '编造的话题', 1000), ('topic-编造-2', '编造角色', '编造的另一个话题', 2000)`); err != nil {
			return err
		}
		if err := execIfTable(db, "local_groups", `INSERT INTO local_groups (id, name, created_at) VALUES ('group-编造-1', '编造的群', 1000), ('group-编造-2', '编造的另一个群', 2000)`); err != nil {
			return err
		}
		if err := execIfTable(db, "local_group_members", `INSERT INTO local_group_members (group_id, character_id, position) VALUES ('group-编造-1', '编造角色', 0), ('group-编造-2', '编造角色', 0)`); err != nil {
			return err
		}
		if err := execIfTable(db, "media_fetch", `INSERT INTO media_fetch (event_seq, idx, kind, url_sig, state, updated_at) VALUES
(1, 0, 'image', 'sig-编造-1', 'done', 1000), (2, 0, 'image', 'sig-编造-2', 'pending', 2000)`); err != nil {
			return err
		}
		return nil
	}
}

func seedTrace(db *sql.DB) error {
	if err := execIfTable(db, "turns", `INSERT INTO turns (id, character_id, conversation, channel, started_at, ended_at, outcome) VALUES
('turn-编造-1', '编造角色', 'local:topic:编造', 'local', 1000, 1100, 'done'),
('turn-编造-2', '编造角色', 'local:topic:编造', 'local', 2000, 2200, 'stopped'),
('turn-编造-3', '编造角色', 'qq:group:编造群', 'qq', 3000, NULL, NULL)`); err != nil {
		return err
	}
	if err := execIfTable(db, "model_runs", `INSERT INTO model_runs (id, character_id, conversation, stage, model, started_at, input_tokens, output_tokens, sections_json, injected_json) VALUES
('run-编造-1', '编造角色', 'local:topic:编造', 'chat', '编造模型', 1000, 10, 20, '[]', '[]'),
('run-编造-2', '编造角色', 'qq:group:编造群', 'chat', '编造模型', 3000, 30, 40, '[]', '[]')`); err != nil {
		return err
	}
	if err := execIfTable(db, "steps", `INSERT INTO steps (id, character_id, conversation, kind, name, status, started_at, batch) VALUES
('step-编造-1', '编造角色', 'local:topic:编造', 'tool', '编造工具', 'done', 1050, 1),
('step-编造-2', '编造角色', 'qq:group:编造群', 'tool', '编造工具', 'running', 3050, 1)`); err != nil {
		return err
	}
	if err := execIfTable(db, "gate_state", `INSERT INTO gate_state (key, state, since_seq, updated_at) VALUES
('gate-编造-1', 'open', 1, 1000), ('gate-编造-2', 'closed', 2, 2000)`); err != nil {
		return err
	}
	if err := execIfTable(db, "gate_decisions", `INSERT INTO gate_decisions (at, character_id, conversation, seq, batch, frequency, verdict, reason) VALUES
(1000, '编造角色', 'local:topic:编造', 1, 1, 0.5, 'speak', '编造的理由一'),
(2000, '编造角色', 'qq:group:编造群', 2, 1, 0.25, 'skip', '编造的理由二')`); err != nil {
		return err
	}
	return nil
}

func execIfTable(db *sql.DB, table, stmt string) error {
	ok, err := tableExists(db, table)
	if err != nil || !ok {
		return err
	}
	_, err = db.Exec(stmt)
	return err
}

func sqlText(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
