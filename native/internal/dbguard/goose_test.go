package dbguard

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

// 规格要求先实证：goose Provider 碰到 goose 表里已有更大号的库，是静默通过还是报错。
// 真实场景（程序内嵌到 2 号，库已经被更新的程序迁到 99 号）里 1、2 号都已应用。
func TestGooseBehaviorWhenDBVersionAhead(t *testing.T) {
	files := fstest.MapFS{
		"0001_one.sql": {Data: []byte("-- +goose Up\nCREATE TABLE one (id INTEGER);\n")},
		"0002_two.sql": {Data: []byte("-- +goose Up\nCREATE TABLE two (id INTEGER);\n")},
	}
	t.Run("旧号都在时静默通过", func(t *testing.T) {
		db := openTestDB(t, filepath.Join(t.TempDir(), "ahead.db"))
		pv, err := goose.NewProvider(goose.DialectSQLite3, db, files)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pv.Up(context.Background()); err != nil {
			t.Fatalf("先迁到 2：%v", err)
		}
		execAll(t, db, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (99, 1)`)
		pv2, err := goose.NewProvider(goose.DialectSQLite3, db, files)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pv2.Up(context.Background()); err != nil {
			t.Fatalf("goose 对更高的库报错了（与推断的静默通过相反）：%v", err)
		}
		got, err := pv2.GetDBVersion(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got != 99 {
			t.Fatalf("库版本 = %d，要 99", got)
		}
	})
	t.Run("缺程序的旧号时报缺迁移", func(t *testing.T) {
		db := openTestDB(t, filepath.Join(t.TempDir(), "missing.db"))
		execAll(t, db, gooseDDL, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, 1), (99, 1)`)
		pv, err := goose.NewProvider(goose.DialectSQLite3, db, files)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pv.Up(context.Background())
		if err == nil {
			t.Fatal("goose 静默通过了一个缺 1、2 号的库")
		}
		if !strings.Contains(err.Error(), "missing") {
			t.Fatalf("要的是缺迁移错误，实际：%v", err)
		}
		t.Logf("实际错误：%v", err)
	})
}
