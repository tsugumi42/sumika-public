package desktopread

import (
	"bytes"
	"context"
	"errors"
	stdimage "image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/testutil/junction"
)

// 派生数据的 miss 也必须拒绝联接，不能从外部目录补图或往里面写缩略图。
func TestMediaThumbnailJunctionRejected(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	hash := putMedia(t, root, "qq", pngBytes)
	event := addSegs(t, qq, "qq", "qq:group:90002", image(hash), "", []string{"a"})
	outside := t.TempDir()
	target := filepath.Join(root, "go", "media-thumbs")
	if err := junction.Create(target, outside); err != nil {
		t.Fatal(err)
	}
	_, err := s.ReadMedia(context.Background(), mediaRef("qq:group:90002", event, hash), 384)
	if !errors.Is(err, mediastore.ErrMediaLink) {
		t.Fatalf("缓存联接未拒绝：%v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("联接目标被写入：%v %v", entries, err)
	}
}

// 缩略图缓存是派生数据：杀毒、预览这类程序暂时占着缓存文件时，读缩略图不该跟着失败，照源图现生成、原样返回。
// 整包并行跑时偶发过"文件被另一个进程占用"；这里用句柄把占用固定下来，两种占法都不靠计时放开。
func TestMediaThumbnailCacheLockedStillServes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		share uint32 // 占着缓存的句柄允许别人怎么开
		stale bool   // 缓存先截断成坏的，逼出重写改名
	}{
		{"独占：连打开都不行", 0, false},
		{"允许读写不允许改名：重写改名等不到", windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, set, root := mediaFixture(t)
			qq, _ := set.Platform("qq")
			src := stdimage.NewNRGBA(stdimage.Rect(0, 0, 800, 400))
			for y := range 400 {
				for x := range 800 {
					src.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 91, A: 255})
				}
			}
			var original bytes.Buffer
			if err := png.Encode(&original, src); err != nil {
				t.Fatal(err)
			}
			hash := putMedia(t, root, "qq", original.Bytes())
			event := addSegs(t, qq, "qq", "qq:group:90002", image(hash), "", []string{"a"})
			ref := mediaRef("qq:group:90002", event, hash)
			read := func() ([]byte, error) {
				result, err := s.ReadMedia(context.Background(), ref, 384)
				return result.Value.Bytes, err
			}
			first, err := read()
			if err != nil {
				t.Fatal(err)
			}
			cache := filepath.Join(root, "go", "media-thumbs", "qq", hash[:2], hash+".v1.384.png")
			if tc.stale {
				if err := os.WriteFile(cache, first[:len(first)/2], 0600); err != nil {
					t.Fatal(err)
				}
			}
			name, err := windows.UTF16PtrFromString(cache)
			if err != nil {
				t.Fatal(err)
			}
			h, err := windows.CreateFile(name, windows.GENERIC_READ, tc.share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(h)
			got, err := read()
			if err != nil || !bytes.Equal(got, first) {
				t.Fatalf("缓存被占着时读缩略图：%d 字节 err=%v（应该和第一次一样）", len(got), err)
			}
		})
	}
}
