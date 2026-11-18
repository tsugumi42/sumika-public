package desktopread

import (
	"bytes"
	"context"
	"errors"
	stdimage "image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/mediastore"
)

func TestMediaThumbnailLifecycle(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	src := stdimage.NewNRGBA(stdimage.Rect(0, 0, 800, 400))
	for y := range 400 {
		for x := range 800 {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 91, A: uint8(x%180 + 75)})
		}
	}
	var original bytes.Buffer
	if err := png.Encode(&original, src); err != nil {
		t.Fatal(err)
	}
	hash := putMedia(t, root, "qq", original.Bytes())
	event := addSegs(t, qq, "qq", "qq:group:90002", image(hash), "", []string{"a"})
	ref := mediaRef("qq:group:90002", event, hash)
	read := func(service *Service, tier api.MediaTier) []byte {
		t.Helper()
		result, err := service.ReadMedia(context.Background(), ref, tier)
		if err != nil {
			t.Fatal(err)
		}
		return result.Value.Bytes
	}
	first := read(s, 384)
	img, err := png.Decode(bytes.NewReader(first))
	if err != nil || img.Bounds() != stdimage.Rect(0, 0, 384, 192) {
		t.Fatalf("缩略图尺寸：%v %v", img, err)
	}
	if _, _, _, alpha := img.At(5, 5).RGBA(); alpha == 65535 {
		t.Fatal("透明度丢失")
	}
	if !bytes.Equal(read(s, 0), original.Bytes()) {
		t.Fatal("灯箱未读原图")
	}
	cache := filepath.Join(root, "go", "media-thumbs", "qq", hash[:2], hash+".v1.384.png")
	source := filepath.Join(root, "go", "media", "qq", hash[:2], hash)
	// 重启读取服务后仍命中磁盘；把源压缩内容改坏，证明命中没有偷偷解码原件。
	restarted := New(set, []api.CharacterView{{ID: "a"}}, nil).WithData(root)
	defer restarted.Close()
	if err := os.WriteFile(source, []byte("编造的损坏原件"), 0600); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, read(restarted, 384)) {
		t.Fatal("新服务未命中磁盘缩略图")
	}
	if err := os.WriteFile(source, original.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	// 截断缓存加崩溃残留：并发重做必须得到同一份完整像素，不能读到半文件。
	if err := os.WriteFile(cache, first[:len(first)/2], 0600); err != nil {
		t.Fatal(err)
	}
	partial := cache + ".crashed.partial"
	if err := os.WriteFile(partial, []byte("半文件"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(partial, old, old); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			result, err := s.ReadMedia(context.Background(), ref, 384)
			if err != nil {
				failures <- err
				return
			}
			if !bytes.Equal(first, result.Value.Bytes) {
				failures <- errors.New("并发重建像素变化或拿到半文件")
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	entries, err := os.ReadDir(filepath.Dir(cache))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(cache) {
		t.Fatalf("崩溃或在途临时文件残留：%v", entries)
	}
	// 现有缓存不能把不可见事件或另一平台的文件变成可读。
	hidden := addSegs(t, qq, "qq", "qq:group:90002", image(hash), "", nil)
	if _, err := s.ReadMedia(context.Background(), mediaRef("qq:group:90002", hidden, hash), 384); !errors.Is(err, ErrMediaRef) {
		t.Fatalf("缓存绕过可见性：%v", err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadMedia(context.Background(), ref, 384); !errors.Is(err, mediastore.ErrMediaMissing) {
		t.Fatalf("源缺失仍返回孤儿缩略图：%v", err)
	}
}

func TestMediaAnimatedBypassesThumbnail(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	palette := color.Palette{color.Transparent, color.White}
	frame := stdimage.NewPaletted(stdimage.Rect(0, 0, 800, 400), palette)
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: []*stdimage.Paletted{frame, frame}, Delay: []int{10, 10}}); err != nil {
		t.Fatal(err)
	}
	hash := putMedia(t, root, "qq", b.Bytes())
	event := addSegs(t, qq, "qq", "qq:group:90002", image(hash), "", []string{"a"})
	got, err := s.ReadMedia(context.Background(), mediaRef("qq:group:90002", event, hash), 128)
	if err != nil || !bytes.Equal(got.Value.Bytes, b.Bytes()) {
		t.Fatalf("动图被缩成静态：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go", "media-thumbs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("动图产生缩略文件：%v", err)
	}
}
