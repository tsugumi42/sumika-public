package mediastore

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVideoContainersAndPlaybackCopy(t *testing.T) {
	mp4 := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")
	mov := []byte("\x00\x00\x00\x14ftypqt  \x00\x00\x00\x00qt  ")
	for _, tc := range []struct {
		body []byte
		ext  string
	}{
		{mp4, ".mp4"}, {mov, ".mov"},
		{[]byte("\x1a\x45\xdf\xa3\x87\x42\x82\x84webm"), ".webm"},
		{[]byte("\x1a\x45\xdf\xa3\x8b\x42\x82\x88matroska"), ".mkv"},
		{[]byte("\x00\x00\x00\x18ftypavif\x00\x00\x00\x00avifisom"), ""},
		{[]byte("\x1a\x45\xdf\xa3garbage webm"), ""},
		{[]byte("\x00\x00\x00\x08ftyp"), ""},
		{[]byte("<html>video</html>"), ""},
	} {
		if got := VideoExtension(tc.body); got != tc.ext {
			t.Fatalf("容器 %x => %q want %q", tc.body, got, tc.ext)
		}
	}
	data := t.TempDir()
	hash, _, err := Put(data, "qq", bytes.NewReader(mov), 1024)
	if err != nil {
		t.Fatal(err)
	}
	source, err := OpenMedia(data, "qq", hash)
	if err != nil {
		t.Fatal(err)
	}
	oldPath, err := PrepareVideoCopy(context.Background(), data, "qq", source)
	source.Close()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(oldPath) != ".mov" || !strings.Contains(oldPath, filepath.Join(TmpDirName, videoTmpDirName)) {
		t.Fatalf("播放路径 %q", oldPath)
	}
	got, err := os.ReadFile(oldPath)
	if err != nil || !bytes.Equal(got, mov) {
		t.Fatalf("副本内容：%v %x", err, got)
	}
	cutoff := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldPath, cutoff.Add(-time.Hour), cutoff.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(filepath.Dir(oldPath), "fresh.mp4")
	if err := os.WriteFile(fresh, mp4, 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := CleanVideoTmp(data, "qq", cutoff); err != nil || n != 1 {
		t.Fatalf("清旧副本：%d %v", n, err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("旧副本未删：%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("新副本被删", err)
	}
	source, err = OpenMedia(data, "qq", hash)
	if err != nil {
		t.Fatal("原件被删", err)
	}
	b, err := io.ReadAll(source)
	source.Close()
	if err != nil || !bytes.Equal(b, mov) {
		t.Fatal("原件被改")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source, err = OpenMedia(data, "qq", hash)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := PrepareVideoCopy(ctx, data, "qq", source); err == nil {
		t.Fatal("取消后不能生成播放副本")
	}
	t.Logf("MOV 副本后缀正确、内容 %d 字节；清旧 1 个，保留新副本和原件", len(got))
}
