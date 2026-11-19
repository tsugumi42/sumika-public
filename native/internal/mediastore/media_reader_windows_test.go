package mediastore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/datalock"
)

// 路径和句柄不能变成第一次读成功后永远信任。
func TestReaderChecksEachOpen(t *testing.T) {
	root, hash := mediaReadWindowsFixture(t)
	lock, err := datalock.LockData(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	r, err := NewReader(root)
	if err != nil {
		t.Fatal(err)
	}
	f, err := r.Open("local", hash)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	file := filepath.Join(root, "go", "media", "local", hash[:2], hash)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Open("local", hash); !errors.Is(err, ErrMediaMissing) {
		t.Fatalf("后续读取没有检查文件缺失：%v", err)
	}
	shard := filepath.Dir(file)
	if err := os.Remove(shard); err != nil {
		t.Fatal(err)
	}
	mediaReadJunction(t, shard, filepath.Join(root, "go", "media", "qq", hash[:2]))
	if _, err := r.Open("local", hash); !errors.Is(err, ErrMediaLink) {
		t.Fatalf("后续读取接受了另一平台的同内容联接：%v", err)
	}
}

func TestNewReaderIgnoresRetiredImportHistory(t *testing.T) {
	root, hash := mediaReadWindowsFixture(t)
	lock, err := datalock.LockData(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	dir := filepath.Join(root, "go", "import", "fixture")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "activation.json"), []byte("编的坏日志"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(root)
	if err != nil {
		t.Fatalf("旧启用日志阻止媒体读取器建立：%v", err)
	}
	f, err := r.Open("local", hash)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil || string(got) != "编的相同平台媒体" {
		t.Fatalf("旧启用日志影响媒体内容：got=%q err=%v", got, err)
	}
}
