package mediastore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 平台目录、分片目录、.tmp 被换成联接：写之前就拒，联接指向的地方不留东西
func TestPutRefusesJunctions(t *testing.T) {
	content := "编的图"
	for _, which := range []string{"platform", "tmp", "shard"} {
		t.Run(which, func(t *testing.T) {
			data := t.TempDir()
			elsewhere := t.TempDir()
			media := filepath.Join(data, "go", "media")
			if err := os.MkdirAll(media, 0o755); err != nil {
				t.Fatal(err)
			}
			switch which {
			case "platform":
				mediaReadJunction(t, filepath.Join(media, "qq"), elsewhere)
			case "tmp":
				if err := os.MkdirAll(filepath.Join(media, "qq"), 0o755); err != nil {
					t.Fatal(err)
				}
				mediaReadJunction(t, filepath.Join(media, "qq", TmpDirName), elsewhere)
			case "shard":
				if err := os.MkdirAll(filepath.Join(media, "qq"), 0o755); err != nil {
					t.Fatal(err)
				}
				mediaReadJunction(t, filepath.Join(media, "qq", mediaShard(sum([]byte(content)))), elsewhere)
			}
			_, _, err := Put(data, "qq", strings.NewReader(content), 64)
			if !errors.Is(err, ErrMediaLink) {
				t.Fatalf("联接要拒：%v", err)
			}
			if left, _ := os.ReadDir(elsewhere); len(left) != 0 {
				t.Fatalf("联接指向的地方被写了东西：%v", left)
			}
		})
	}
}

// 建好目录之后、改名之前，分片目录被换成联接：最终文件不能落到联接指向的地方
func TestPutRejectsSwapBeforeRename(t *testing.T) {
	data := t.TempDir()
	elsewhere := t.TempDir()
	content := "换路径的图"
	shard := filepath.Join(data, "go", "media", "qq", mediaShard(sum([]byte(content))))
	_, _, err := put(data, "qq", strings.NewReader(content), 64, func() {
		if err := os.Remove(shard); err != nil {
			t.Fatal(err)
		}
		mediaReadJunction(t, shard, elsewhere)
	})
	if !errors.Is(err, ErrMediaLink) {
		t.Fatalf("改名前换成联接要拒：%v", err)
	}
	if left, _ := os.ReadDir(elsewhere); len(left) != 0 {
		t.Fatalf("联接指向的地方被写了东西：%v", left)
	}
	if left := tmpLeft(t, data, "qq"); len(left) != 0 {
		t.Fatalf("临时文件没清：%v", left)
	}
}
