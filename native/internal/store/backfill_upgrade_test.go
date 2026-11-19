package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// 用正式第六代库模拟旧程序误清；升级必须让这些区间重新可见并可立即调度。
func TestQQBackfillLegacyFilledUpgrade(t *testing.T) {
	t.Parallel()
	for _, scene := range []string{"private", "group", "empty", "rollback"} {
		t.Run(scene, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "qq.db")
			db, err := openGo(file, "migrations/chat", "")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := migrateWith(db, "migrations/qq", 6, "goose_db_version_qq"); err != nil {
				t.Fatal(err)
			}
			if v := qqPlatformVersion(t, db); v != 6 {
				t.Fatalf("旧库版本=%d，要6", v)
			}
			if _, err := db.Exec(`INSERT INTO qq_sync(account,through) VALUES ('100',1000),('200',900),('300',50);
INSERT INTO channel_gaps(channel,conversation,from_ms,to_ms,reason,state,tries,last_try_ms,last_manual_ms,created_ms,updated_ms) VALUES
('qq','qq:private:100:800',600,700,'outage','open',1,800,810,500,820),
('qq','qq:private:100:800',710,720,'page-cap','blocked',2,800,810,500,820),
('qq','qq:private:100:800',730,740,'stalled','abandoned',3,800,810,500,820),
('local','local:private:800',100,200,'outage','filled',3,800,810,50,820)`); err != nil {
				t.Fatal(err)
			}
			conv := "qq:private:100:800"
			if scene == "group" {
				conv = "qq:group:800"
			}
			if scene != "empty" {
				if _, err := db.Exec(`INSERT INTO channel_gaps(channel,conversation,from_ms,to_ms,reason,state,tries,last_try_ms,last_manual_ms,created_ms,updated_ms) VALUES
('qq',?,100,200,'outage','filled',3,950,960,80,970),
('qq',?,300,400,'page-cap','filled',2,950,960,80,970),
('qq','qq:private:400:800',500,550,'outage','filled',0,950,960,80,970)`, conv, conv); err != nil {
					t.Fatal(err)
				}
			}
			before, err := (&Store{Events: db}).ChannelGaps(context.Background(), "qq")
			if err != nil {
				t.Fatal(err)
			}
			if scene == "rollback" {
				if _, err := db.Exec(`CREATE TRIGGER fail_upgrade BEFORE UPDATE ON channel_gaps BEGIN SELECT RAISE(FAIL,'编造迁移落盘失败'); END`); err != nil {
					t.Fatal(err)
				}
				if err := migrateWith(db, "migrations/qq", 0, "goose_db_version_qq"); err == nil {
					t.Fatal("迁移落盘失败却成功")
				}
				assertLegacyChecked(t, db, map[string]int64{"100": 1000, "200": 900, "300": 50})
				if v := qqPlatformVersion(t, db); v != 6 {
					t.Fatalf("失败迁移推进了版本：%d", v)
				}
				if has, err := tableExists(db, "qq_backfill_continuity"); err != nil || has {
					t.Fatalf("建表未回滚：%v %v", has, err)
				}
			} else if err := migrateWith(db, "migrations/qq", 0, "goose_db_version_qq"); err != nil {
				t.Fatal(err)
			}
			s := &Store{Events: db}
			after, err := s.ChannelGaps(context.Background(), "qq")
			if err != nil || len(after) != len(before) {
				t.Fatalf("迁移改变缺口集合：%v %v", after, err)
			}
			for i, old := range before {
				got := after[i]
				if old.State != "filled" || scene == "rollback" {
					if got != old {
						t.Fatalf("不应改动的缺口变化：before=%+v after=%+v", old, got)
					}
					continue
				}
				if got.State != "blocked" || got.Reason != "unverified-history" || got.Tries != 0 || got.LastTryMS != 0 || got.UpdatedMS < old.UpdatedMS {
					t.Fatalf("旧filled仍被隐藏或未恢复重试：%+v", got)
				}
				got.State, got.Reason, got.Tries, got.LastTryMS, got.UpdatedMS = old.State, old.Reason, old.Tries, old.LastTryMS, old.UpdatedMS
				if got != old {
					t.Fatalf("降级丢失原区间或手动冷却：%+v %+v", old, got)
				}
			}
			if scene == "rollback" {
				return
			}
			want := map[string]int64{"100": 1000, "200": 900, "300": 50}
			if scene == "private" {
				want["100"] = 100
			} else if scene == "group" {
				want["100"], want["200"] = 100, 100
			}
			assertLegacyChecked(t, db, want)
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM qq_backfill_continuity`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("升级凭空制造连续证明：%d %v", n, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM channel_gaps WHERE channel='local' AND state='filled'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("QQ迁移改了其他平台：%d %v", n, err)
			}
			page, err := s.ReadAccountGaps(context.Background(), "100", "", 100)
			wantVisible := 3
			if scene != "empty" {
				wantVisible += 2
			}
			if err != nil || len(page.Items) != wantVisible {
				t.Fatalf("旧filled升级后仍不可见：%+v %v", page, err)
			}
			// 新代已证明的 filled 不能在重开时再次降级。
			if scene != "empty" {
				if _, err := db.Exec(`UPDATE channel_gaps SET state='filled' WHERE channel='qq' AND conversation=? AND from_ms=100`, conv); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenChatFile(file, "qq")
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			assertLegacyChecked(t, reopened, want)
			if scene != "empty" {
				if err := reopened.QueryRow(`SELECT count(*) FROM channel_gaps WHERE channel='qq' AND conversation=? AND from_ms=100 AND state='filled'`, conv).Scan(&n); err != nil || n != 1 {
					t.Fatalf("新版filled重开被降级：%d %v", n, err)
				}
			}
		})
	}
}

func assertLegacyChecked(t *testing.T, db *sql.DB, want map[string]int64) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM qq_sync`).Scan(&n); err != nil || n != len(want) {
		t.Fatalf("不应新增账号检查位置：%d %v", n, err)
	}
	for account, expected := range want {
		var got int64
		if err := db.QueryRow(`SELECT through FROM qq_sync WHERE account=?`, account).Scan(&got); err != nil || got != expected {
			t.Fatalf("检查位置未保守回退：account=%s got=%d want=%d err=%v", account, got, expected, err)
		}
	}
}
