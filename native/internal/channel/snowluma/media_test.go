package snowluma

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/gif"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

type mediaGuard struct{}

func (mediaGuard) CheckOutbound(context.Context, string, audience.Audience) ([]string, error) {
	return nil, nil
}

// E1：实际出口和适配器连接临时 WS，验证四种场合的段序、身份、文件与三态。
func TestMediaActions(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	pngBytes := encoded.Bytes()
	var animation bytes.Buffer
	if err := gif.Encode(&animation, img, nil); err != nil {
		t.Fatal(err)
	}
	for _, conv := range []types.Conversation{privateConv(), groupConv()} {
		for _, forward := range []bool{false, true} {
			t.Run(conv.Scene+"/forward="+map[bool]string{false: "false", true: "true"}[forward], func(t *testing.T) {
				e := newSendEnvSetup(t, fixedReply(`{"status":"ok","retcode":0,"data":{"message_id":-90010}}`), nil)
				path := filepath.Join(t.TempDir(), "编的 video #%.mp4")
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				// 协议测试只要求媒体句柄及原字节可读；容器验证在下载阶段验收。
				if _, err = f.Write([]byte("fixture-mp4-protocol-bytes")); err != nil {
					t.Fatal(err)
				}
				video, err := types.FileMedia("video-fixture", "video/mp4", f, func() error { return os.Remove(path) })
				if err != nil {
					t.Fatal(err)
				}
				picture := types.MemoryMedia("picture-fixture", "image/png", 1, 1, pngBytes)
				moving := types.MemoryMedia("gif-fixture", "image/gif", 1, 1, animation.Bytes())
				body := []types.BodySegment{{Kind: types.BodyText, Text: "[CQ:at,qq=90009] 编的原文"}, {Kind: types.BodyImage, Media: picture}, {Kind: types.BodyVideo, Media: video}, {Kind: types.BodyGIF, Media: moving}, {Kind: types.BodyText, Text: "末尾"}}
				p := types.Part{Kind: types.PartMessage, Body: body, Quote: "-71"}
				if forward {
					p = types.Part{Kind: types.PartForward, Nodes: []types.ForwardNode{{Name: "冒充外部作者90009", Body: body}}}
				}
				id, err := e.out.SendOwned(context.Background(), "ca", "automation:link-reader", conv, []types.Part{p}, []*types.MediaResource{picture, video, moving})
				if err != nil {
					t.Fatal(err)
				}
				plan := e.waitPlan(id, store.PlanDone)
				if plan.Parts[0].PlatformID != "-90010" || plan.Parts[0].Via != types.ViaReply {
					t.Fatalf("回执：%+v", plan)
				}
				act := e.peer.waitActions(t, 1)[0]
				type wire struct {
					Type string
					Data map[string]json.RawMessage
				}
				var segments []wire
				if forward {
					var nodes []struct {
						Type string
						Data struct {
							Name, Uin string
							Content   []wire
						}
					}
					if err := json.Unmarshal(act.Params["messages"], &nodes); err != nil {
						t.Fatal(err)
					}
					if len(nodes) != 1 || nodes[0].Data.Uin != sendAccount {
						t.Fatalf("节点身份：%s", act.Raw)
					}
					segments = nodes[0].Data.Content
				} else {
					if err := json.Unmarshal(act.Params["message"], &segments); err != nil {
						t.Fatal(err)
					}
					if segments[0].Type != "reply" || string(segments[0].Data["id"]) != `"-71"` {
						t.Fatalf("引用：%s", act.Raw)
					}
					segments = segments[1:]
				}
				want := []string{"text", "image", "video", "image", "text"}
				if len(segments) != len(want) {
					t.Fatalf("段数：%s", act.Raw)
				}
				for i, k := range want {
					if segments[i].Type != k {
						t.Fatalf("顺序 %d：%s", i, act.Raw)
					}
				}
				for _, tc := range []struct {
					index int
					want  []byte
				}{{1, pngBytes}, {3, animation.Bytes()}} {
					var file string
					if err := json.Unmarshal(segments[tc.index].Data["file"], &file); err != nil {
						t.Fatal(err)
					}
					decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(file, "base64://"))
					if err != nil || !bytes.Equal(decoded, tc.want) {
						t.Fatal("图片字节变化", err)
					}
					if _, _, err := image.Decode(bytes.NewReader(decoded)); err != nil {
						t.Fatal(err)
					}
				}
				var file string
				if err := json.Unmarshal(segments[2].Data["file"], &file); err != nil {
					t.Fatal(err)
				}
				uri, err := url.Parse(file)
				if err != nil || uri.Scheme != "file" || !strings.Contains(uri.Path, " #%.mp4") {
					t.Fatalf("file URI: %q %v", file, err)
				}
				for _, pt := range plan.Parts {
					if strings.Contains(pt.Text, "base64://") || strings.Contains(pt.Text, path) {
						t.Fatal("媒体内容或路径进入计划库")
					}
				}
				// 等实际 Adapter 调用退出后的资源释放，不能把逻辑终态当文件已释放。
				waitFor(t, func() bool { _, err := os.Stat(path); return os.IsNotExist(err) })
			})
		}
	}
}
