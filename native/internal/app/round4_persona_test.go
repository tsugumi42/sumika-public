package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 非选中角色有记忆时，卡路径的 Stat 错误不能把它从预开名单里抹掉。
func TestUnusedMemoryCardErrorsStopOpen(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"文件循环链接", "card循环链接", "卡文件是目录"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			base := filepath.Join(e.data, "characters", "other")
			card := filepath.Join(base, "card")
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(e.data, "go", "characters", "other", "memory.db")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", file)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`CREATE TABLE memories (id TEXT PRIMARY KEY)`)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if kind == "card循环链接" {
				if err := os.WriteFile(filepath.Join(base, "character.yml"), []byte("name: 编的角色\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(card, card); err != nil {
					t.Skipf("本机没有创建符号链接的权限：%v", err)
				}
			} else {
				if err := os.MkdirAll(card, 0700); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(card, "character.yml")
				if kind == "文件循环链接" {
					if err := os.Symlink(file, file); err != nil {
						t.Skipf("本机没有创建符号链接的权限：%v", err)
					}
				} else if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
			}
			var statCause error
			if kind != "卡文件是目录" {
				file := filepath.Join(card, "character.yml")
				if kind == "card循环链接" {
					file = card
				}
				_, err := os.Stat(file)
				if err == nil || errors.Is(err, os.ErrNotExist) {
					t.Fatalf("循环链接没有造成非不存在错误：%v", err)
				}
				statCause = err.(*os.PathError).Err
			}
			a, err := Open(context.Background(), e.options())
			if a != nil {
				a.cleanup()
			}
			if err == nil {
				t.Fatal("有记忆的无关角色卡路径错误被吞掉")
			}
			if !strings.Contains(err.Error(), "other") {
				t.Errorf("卡错误没有指出角色：%v", err)
			}
			if statCause != nil && !errors.Is(err, statCause) {
				t.Errorf("没有返回卡的 Stat 错误：%v", err)
			}
		})
	}
}
