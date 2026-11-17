package desktopread

import (
	"bytes"
	"context"
	stdimage "image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/history"
	"sumika-native/internal/store"
)

func writeTestPNG(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// 图片附件发送时量好宽高存下，读回来时带上；文件被挪走以后仍按原比例占位。老记录（没存宽高）读回时补量一次，读不到就是 0。
func TestImageAttachmentDimsStoredAndProjected(t *testing.T) {
	t.Parallel()
	s, _ := fixture(t)
	dir := t.TempDir()
	path := writeTestPNG(t, dir, "图.png", 300, 200)
	key := keyOf("console:a")
	parts, _, err := s.ResolveParts(context.Background(), key, []api.LocalPart{{Text: "图", Attachments: []api.LocalAttachment{
		{Kind: "image", Path: path, Name: "图.png"},
		{Kind: "file", Path: path, Name: "文件.png"},
	}}})
	if err != nil || len(parts) != 1 {
		t.Fatalf("%+v %v", parts, err)
	}
	if a := parts[0].Attachments[0]; a.Width != 300 || a.Height != 200 {
		t.Fatalf("发送时该量好宽高存下：%+v", a)
	}
	if a := parts[0].Attachments[1]; a.Width != 0 {
		t.Fatalf("文件不量：%+v", a)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	got := s.projectParts(key, store.DisplayRow{}, parts)
	if a := got[0].Attachments[0]; a.Width != 300 || a.Height != 200 {
		t.Fatalf("文件没了仍按存下的宽高：%+v", a)
	}

	legacy := writeTestPNG(t, dir, "老.png", 90, 120)
	old := []history.Part{{Text: "老", Attachments: []history.Attachment{{Kind: "image", Path: legacy}, {Kind: "image", Path: filepath.Join(dir, "没有.png")}}}}
	got = s.projectParts(key, store.DisplayRow{}, old)
	if a := got[0].Attachments[0]; a.Width != 90 || a.Height != 120 {
		t.Fatalf("老记录读回时补量：%+v", a)
	}
	if a := got[0].Attachments[1]; a.Width != 0 || a.Height != 0 {
		t.Fatalf("读不到文件就是 0，不报错：%+v", a)
	}
}
