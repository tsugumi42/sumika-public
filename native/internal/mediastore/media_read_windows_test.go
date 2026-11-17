package mediastore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"sumika-native/internal/testutil/junction"
)

func mediaReadWindowsFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	content := []byte("编的相同平台媒体")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	for _, platform := range []string{"local", "qq"} {
		path := filepath.Join(dir, "go", "media", platform, hash[:2], hash)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, hash
}

func mediaReadJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := junction.Create(link, target); err != nil {
		t.Fatalf("建编造联接：%v", err)
	}
}

func TestMediaReadRejectsPathSwap(t *testing.T) {
	dir, hash := mediaReadWindowsFixture(t)
	local := filepath.Join(dir, "go", "media", "local")
	qq := filepath.Join(dir, "go", "media", "qq")
	f, err := openMedia(dir, "local", hash, func() {
		if err := os.Rename(local, local+"-old"); err != nil {
			t.Fatal(err)
		}
		mediaReadJunction(t, local, qq)
	})
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, ErrMediaLink) || errors.Is(err, ErrMediaMissing) {
		t.Fatalf("检查后换成另一平台的同内容联接必须拒绝：%v", err)
	}
	// 被冒用的平台仍能读，不把相同 hash 或文件内容误当成拒绝理由。
	f, err = OpenMedia(dir, "qq", hash)
	if err != nil {
		t.Fatal("另一平台被误伤：", err)
	}
	f.Close()
}

func TestMediaReadRejectsReparseHandle(t *testing.T) {
	dir, _ := mediaReadWindowsFixture(t)
	link := filepath.Join(dir, "编的句柄联接")
	mediaReadJunction(t, link, filepath.Join(dir, "go", "media", "qq"))
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(h), link)
	defer f.Close()
	// expected 取句柄的真实路径，确保拒绝来自重解析属性，而不是路径不匹配。
	expected, err := mediaFinalPath(h)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkMediaHandle(f, expected); !errors.Is(err, ErrMediaLink) {
		t.Fatalf("重解析点句柄没有拒绝：%v", err)
	}
}

func TestMediaReadRejectsNonDirectoryParent(t *testing.T) {
	dir, hash := mediaReadWindowsFixture(t)
	shard := filepath.Join(dir, "go", "media", "local", hash[:2])
	if err := os.RemoveAll(shard); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shard, []byte("编的非目录父路径"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenMedia(dir, "local", hash)
	if f != nil {
		f.Close()
	}
	if err == nil || errors.Is(err, ErrMediaMissing) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("非目录父路径必须算读取错误，不能算缺文件：%v", err)
	}
}
