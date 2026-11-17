package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"sumika-native/internal/datalock"
)

// 只用临时库：建最小的 events 和 channel_msgs，验证只补该补的、重跑不变、备份在改之前、只数不改
func TestBackfillReplyTo(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "qq.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE events (seq INTEGER PRIMARY KEY, id TEXT, channel TEXT, conversation TEXT, actor_kind TEXT, kind TEXT, reply_to TEXT, segments_json TEXT)`,
		`CREATE TABLE channel_msgs (channel TEXT, conversation TEXT, platform_id TEXT, event_seq INTEGER)`,
		`INSERT INTO events VALUES (1,'in1','qq','g1','human','message',NULL,'{}')`,
		`INSERT INTO channel_msgs VALUES ('qq','g1','-100',1)`,
		// 要补：发出的、带引用、reply_to 空、平台消息号在库里
		`INSERT INTO events VALUES (2,'out1','qq','g1','character','message',NULL,'{"parts":[{"kind":"message","quote":"-100"}]}')`,
		// 已经有值：不动
		`INSERT INTO events VALUES (3,'out2','qq','g1','character','message','keep','{"parts":[{"kind":"message","quote":"-100"}]}')`,
		// 连不上：平台消息号不在库里
		`INSERT INTO events VALUES (4,'out3','qq','g1','character','message',NULL,'{"parts":[{"kind":"message","quote":"-999"}]}')`,
		// 别的会话同号不能串
		`INSERT INTO events VALUES (5,'out4','qq','g2','character','message',NULL,'{"parts":[{"kind":"message","quote":"-100"}]}')`,
		// 没有引用的发出消息、人发的带引用消息：不碰
		`INSERT INTO events VALUES (6,'out5','qq','g1','character','message',NULL,'{"parts":[{"kind":"message"}]}')`,
		`INSERT INTO events VALUES (7,'in2','qq','g1','human','message',NULL,'{"parts":[{"kind":"message","quote":"-100"}]}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	now := time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC)
	ctx := context.Background()
	rep, err := Run(ctx, file, filepath.Join(dir, "backups"), false, now)
	if err != nil || rep.WithQuote != 4 || rep.Empty != 3 || rep.Resolvable != 1 || rep.Updated != 0 || rep.Backup != "" {
		t.Fatalf("只数：%v %+v", err, rep)
	}
	read := func(path, id string) string {
		d, _ := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
		defer d.Close()
		var v sql.NullString
		d.QueryRow(`SELECT reply_to FROM events WHERE id=?`, id).Scan(&v)
		return v.String
	}
	if read(file, "out1") != "" {
		t.Fatal("只数不该改库")
	}
	rep, err = Run(ctx, file, filepath.Join(dir, "backups"), true, now)
	if err != nil || rep.Updated != 1 || rep.Backup == "" {
		t.Fatalf("补：%v %+v", err, rep)
	}
	if read(file, "out1") != "in1" || read(file, "out2") != "keep" || read(file, "out3") != "" || read(file, "out4") != "" || read(file, "out5") != "" {
		t.Fatal("补错了行")
	}
	backup := filepath.Join(rep.Backup, "qq.db")
	if _, err := os.Stat(backup); err != nil {
		t.Fatal("没有备份", err)
	}
	if read(backup, "out1") != "" {
		t.Fatal("备份里不该已经有补过的值")
	}
	rep, err = Run(ctx, file, filepath.Join(dir, "backups"), true, now.Add(time.Second))
	if err != nil || rep.Updated != 0 || rep.Empty != 2 {
		t.Fatalf("重跑：%v %+v", err, rep)
	}
}

// Bun 管本地私聊时（Go 不在、Bun 拿着 go/local.lock），写 local.db 的 -platform local 和别的平台都不能开工（复核 R3）
func TestLockDataRefusesWhileBunHoldsLocalLock(t *testing.T) {
	data := t.TempDir()
	first, err := datalock.LockData(data) // 让 backend.lock 的文件存在，Go 已退出
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	bun, err := datalock.LockLocal(data)
	if err != nil {
		t.Fatal(err)
	}
	defer bun.Close()
	for _, platform := range []string{"local", "qq"} {
		l, err := lockData(data, platform)
		if l != nil {
			l.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "Bun") {
			t.Fatalf("-platform %s：Bun 在写本地，该拒绝并说清：%v", platform, err)
		}
	}
	_ = bun.Close()

	// 从没起过 Bun（local.lock 还不存在）：local 能开工，并且建出 local.lock 拿住（写的时候没人能起 Bun 去写）
	fresh := t.TempDir()
	g0, _ := datalock.LockData(fresh)
	_ = g0.Close()
	l, err := lockData(fresh, "local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := datalock.LockLocal(fresh); !errors.Is(err, datalock.ErrLocalInUse) {
		t.Fatalf("-platform local 写的时候该占着 local.lock：%v", err)
	}
	l.Close()
	// qq 平台不写 local.db，不为它建 local.lock
	data2 := t.TempDir()
	g, _ := datalock.LockData(data2)
	_ = g.Close()
	l2, err := lockData(data2, "qq")
	if err != nil {
		t.Fatal(err)
	}
	l2.Close()
	if _, err := os.Stat(filepath.Join(data2, "go", datalock.LocalLockName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("qq 平台不该建 local.lock：%v", err)
	}
	// Sumika 还开着：照旧的提示
	g2, _ := datalock.LockData(data2)
	defer g2.Close()
	if _, err := lockData(data2, "local"); err == nil || !strings.Contains(err.Error(), "Sumika 还开着") {
		t.Fatalf("%v", err)
	}
}
