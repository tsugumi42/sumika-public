package app

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// 粘贴的图存进媒体库，返回的路径指向库里同内容的文件，随分块消息发出去后读回来还在。
func TestSaveLocalImageThenSendAsAttachment(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("看到了")})
	key := localKey("console:" + testChar)
	data := tinyPNG(t)
	att, err := d.Reader.SaveLocalImage(context.Background(), key, data)
	if err != nil {
		t.Fatal(err)
	}
	if att.Kind != "image" || att.Media != nil || att.Name == "" {
		t.Fatalf("附件不对：%+v", att)
	}
	got, err := os.ReadFile(att.Path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("媒体库里的文件和粘贴的不一样：%v", err)
	}
	if !strings.Contains(filepath.ToSlash(att.Path), "/go/media/local/") {
		t.Fatalf("没有存进 local 媒体库：%s", att.Path)
	}
	again, err := d.Reader.SaveLocalImage(context.Background(), key, data)
	if err != nil || again.Path != att.Path {
		t.Fatalf("同一张图应当寻址到同一个文件：%+v %v", again, err)
	}
	if err := d.Reader.SendLocalParts(context.Background(), key, []api.LocalPart{{Text: "看这张", Attachments: []api.LocalAttachment{att}}}, nil); err != nil {
		t.Fatalf("存好的图发不出去：%v", err)
	}
	items := readAll(t, d, key)
	if len(items) == 0 || len(items[0].Parts) != 1 || len(items[0].Parts[0].Attachments) != 1 || items[0].Parts[0].Attachments[0].Path != att.Path {
		t.Fatalf("读回的块没有这张图：%+v", items)
	}
}

func TestSaveLocalImageRejects(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	key := localKey("console:" + testChar)
	if _, err := d.Reader.SaveLocalImage(context.Background(), key, []byte("不是图片")); err == nil {
		t.Fatal("不是 PNG 也收了")
	}
	if _, err := d.Reader.SaveLocalImage(context.Background(), key, nil); err == nil {
		t.Fatal("空内容也收了")
	}
	qq := api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:1"}
	if _, err := d.Reader.SaveLocalImage(context.Background(), qq, tinyPNG(t)); err == nil {
		t.Fatal("QQ 会话也能存")
	}
	other := localKey("console:不存在的角色")
	if _, err := d.Reader.SaveLocalImage(context.Background(), other, tinyPNG(t)); err == nil {
		t.Fatal("不存在的会话也能存")
	}
}
