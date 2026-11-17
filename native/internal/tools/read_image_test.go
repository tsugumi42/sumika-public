package tools

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadImageReturnsResizedAttachment(t *testing.T) {
	wd := t.TempDir()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2048, 512))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "wide.png"), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runTool(t, wd, "read", map[string]any{"path": "wide.png"})
	if r.IsError || r.Text != "Read image file [image/png]" || len(r.Images) != 1 || !strings.HasPrefix(r.Images[0].DataURL, "data:image/png;base64,") {
		t.Fatalf("read image: %+v", r)
	}
	if strings.Contains(r.Text, "base64") {
		t.Fatal("image bytes must not enter the result text")
	}
	if r := runTool(t, wd, "read", map[string]any{"path": "nope.png"}); !r.IsError {
		t.Fatal("missing file must stay an error")
	}
}
