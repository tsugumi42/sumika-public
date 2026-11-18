package mediastore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func tmpLeft(t *testing.T, data, platform string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(data, "go", "media", platform, TmpDirName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// M2 的文件一半：落在 <根>/qq/<aa>/<hash>，hash 等于内容的 sha256，OpenMedia 读得出，临时目录是空的
func TestPutStoresContentAddressed(t *testing.T) {
	data := filepath.Join(t.TempDir(), "带 空格 的目录")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("编的一张图\x89PNG")
	hash, size, err := Put(data, "qq", bytes.NewReader(content), 1<<20)
	if err != nil || hash != sum(content) || size != int64(len(content)) {
		t.Fatalf("Put：%q %d %v", hash, size, err)
	}
	f, err := OpenMedia(data, "qq", hash)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if !bytes.Equal(got, content) {
		t.Fatal("读出来的内容不一样")
	}
	if left := tmpLeft(t, data, "qq"); len(left) != 0 {
		t.Fatalf("临时文件没清：%v", left)
	}
	if _, err := os.Stat(filepath.Join(data, "go", "media", "qq", hash[:2], hash)); err != nil {
		t.Fatal(err)
	}
}

// M5 的文件一半：超限中止、删临时文件、不落最终文件；没有长度的无限流也停得住；刚好等于上限收下
func TestPutCapsSize(t *testing.T) {
	data := t.TempDir()
	endless := io.MultiReader(bytes.NewReader(bytes.Repeat([]byte("x"), 100)), neverEnding{})
	if _, _, err := Put(data, "qq", endless, 64); !errors.Is(err, ErrMediaTooBig) {
		t.Fatalf("无限流没在上限处中止：%v", err)
	}
	if left := tmpLeft(t, data, "qq"); len(left) != 0 {
		t.Fatalf("超限以后临时文件还在：%v", left)
	}
	if entries, _ := os.ReadDir(filepath.Join(data, "go", "media", "qq")); len(entries) != 1 || entries[0].Name() != TmpDirName {
		t.Fatalf("超限的不该留下任何最终文件：%v", entries)
	}
	exact := bytes.Repeat([]byte("y"), 64)
	if _, n, err := Put(data, "qq", bytes.NewReader(exact), 64); err != nil || n != 64 {
		t.Fatalf("刚好等于上限要收下：%d %v", n, err)
	}
	if _, _, err := Put(data, "qq", bytes.NewReader(append(exact, 'z')), 64); !errors.Is(err, ErrMediaTooBig) {
		t.Fatalf("多一个字节要拒：%v", err)
	}
	if _, _, err := Put(data, "qq", strings.NewReader(""), 64); !errors.Is(err, errMediaEmpty) {
		t.Fatalf("空内容要拒：%v", err)
	}
	if _, _, err := Put(data, "qq", strings.NewReader("a"), 0); err == nil {
		t.Fatal("上限 0 要报错，不能当不设限")
	}
}

type neverEnding struct{}

func (neverEnding) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'n'
	}
	return len(p), nil
}

// 同内容再写一次：成功、一个文件；已有的坏文件（大小不对）被新内容盖掉
func TestPutIdempotentAndHealsBrokenFile(t *testing.T) {
	data := t.TempDir()
	content := []byte("同一张图")
	h1, _, err := Put(data, "qq", bytes.NewReader(content), 1024)
	if err != nil {
		t.Fatal(err)
	}
	h2, _, err := Put(data, "qq", bytes.NewReader(content), 1024)
	if err != nil || h2 != h1 {
		t.Fatalf("第二次：%q %v", h2, err)
	}
	path := filepath.Join(data, "go", "media", "qq", h1[:2], h1)
	if err := os.WriteFile(path, []byte("半截"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Put(data, "qq", bytes.NewReader(content), 1024); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, content) {
		t.Fatalf("坏文件没被盖掉：%q", b)
	}
	if left := tmpLeft(t, data, "qq"); len(left) != 0 {
		t.Fatalf("临时文件没清：%v", left)
	}
}

// 并发写同一个 hash（两个段同一张图，工人 ×2）：都成功，只有一个文件，读出来内容对
func TestPutConcurrentSameContent(t *testing.T) {
	data := t.TempDir()
	content := bytes.Repeat([]byte("并发"), 5000)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	hashes := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, _, err := Put(data, "qq", bytes.NewReader(content), 1<<20)
			errs <- err
			hashes <- h
		}()
	}
	wg.Wait()
	close(errs)
	close(hashes)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发写同一个 hash 失败：%v", err)
		}
	}
	for h := range hashes {
		if h != sum(content) {
			t.Fatalf("hash 不对：%q", h)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(data, "go", "media", "qq", sum(content)[:2]))
	if len(entries) != 1 {
		t.Fatalf("同一个 hash 该只有一个文件：%v", entries)
	}
	if left := tmpLeft(t, data, "qq"); len(left) != 0 {
		t.Fatalf("临时文件没清：%v", left)
	}
}

// 旧启用日志不参与媒体写入和临时文件清理。
func TestPutIgnoresRetiredImportHistory(t *testing.T) {
	data := t.TempDir()
	run := filepath.Join(data, "go", "import", "unfinished")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "activation.json"), []byte("{坏的"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, _, err := Put(data, "qq", strings.NewReader("图"), 64)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(data, "go", "media", "qq", hash[:2], hash))
	if err != nil || string(got) != "图" {
		t.Fatalf("媒体没有写到平台目录：%q %v", got, err)
	}
	if _, err := CleanTmp(data, "qq", time.Now()); err != nil {
		t.Fatal(err)
	}
}

// 平台名不合法（路径穿越）：拒绝，不建目录
func TestPutRejectsBadPlatform(t *testing.T) {
	data := t.TempDir()
	for _, p := range []string{"", "..", "../x", `a\b`, "QQ"} {
		if _, _, err := Put(data, p, strings.NewReader("图"), 64); err == nil {
			t.Fatalf("平台名 %q 不该通过", p)
		}
	}
	if entries, _ := os.ReadDir(data); len(entries) != 0 {
		t.Fatalf("不合法的平台名不该留下东西：%v", entries)
	}
}

func TestCleanTmpOnlyOldFiles(t *testing.T) {
	data := t.TempDir()
	if _, _, err := Put(data, "qq", strings.NewReader("先建出目录"), 64); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(data, "go", "media", "qq", TmpDirName)
	oldFile, newFile := filepath.Join(tmp, "put-old"), filepath.Join(tmp, "put-new")
	for _, f := range []string{oldFile, newFile} {
		if err := os.WriteFile(f, []byte("半截"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldFile, past, past); err != nil {
		t.Fatal(err)
	}
	n, err := CleanTmp(data, "qq", time.Now().Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("清了 %d 个：%v", n, err)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatal("老的没删")
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Fatal("新的不该删")
	}
	// 目录不在：什么也不做
	if n, err := CleanTmp(t.TempDir(), "qq", time.Now()); err != nil || n != 0 {
		t.Fatalf("没有目录：%d %v", n, err)
	}
}
