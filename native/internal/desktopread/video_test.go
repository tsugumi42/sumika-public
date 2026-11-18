package desktopread

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

func TestVideoProjectionAndCredentialsWithoutFileIO(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	hash := putMedia(t, root, "qq", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"))
	cover := putMedia(t, root, "qq", pngBytes)
	id := addSegs(t, qq, "qq", conv, json.RawMessage(fmt.Sprintf(`{"segs":[{"kind":"video","hash":%q},{"kind":"video","size":%d}]}`, hash, 60<<20)), "", []string{"a"})
	var seq int64
	if err := qq.Events.QueryRow(`SELECT seq FROM events WHERE id=?`, id).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	for _, row := range []store.MediaFetch{
		{EventSeq: seq, Idx: 0, Kind: "video", Hash: hash, State: store.MediaDone, CoverHash: cover, DurationMS: 12345, Width: 160, Height: 90, Size: 24},
		{EventSeq: seq, Idx: 1, Kind: "video", State: store.MediaTooBig, Size: 60 << 20},
	} {
		if err := qq.PutMediaFetch(row); err != nil {
			t.Fatal(err)
		}
	}
	opens := 0
	s.openMedia = func(data, platform, hash string) (*os.File, error) {
		opens++
		return mediastore.OpenMedia(data, platform, hash)
	}
	ctx := context.Background()
	if _, err := s.ReadMedia(ctx, mediaRef(conv, id, cover), 0); err != nil || opens != 1 {
		t.Fatalf("封面授权或计数器失效：%d %v", opens, err)
	}
	opens = 0
	page, err := s.ReadMessages(ctx, api.ConversationRequest{Key: keyOf(conv), Limit: 200}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Value.Items) != 1 {
		t.Fatalf("消息数：%d", len(page.Value.Items))
	}
	d := page.Value.Items[0].Segments[0]
	if d.Media == nil || d.Media.Hash != hash || d.Cover == nil || d.Cover.Hash != cover || d.DurationMS != 12345 || d.Width != 160 || d.Height != 90 || d.Size != 24 {
		t.Fatalf("视频投影：%+v", d)
	}
	large := page.Value.Items[0].Segments[1]
	if large.FetchState != store.MediaTooBig || large.Size != 60<<20 {
		t.Fatalf("超限投影：%+v", large)
	}
	if opens != 0 {
		t.Fatalf("读消息打开媒体 %d 次，要 0", opens)
	}
	if _, err := s.PrepareVideo(ctx, *d.Media); err != nil {
		t.Fatal(err)
	}
	plain := addSegs(t, qq, "qq", conv, map[string]any{"segs": []any{map[string]any{"kind": "text", "text": "编造"}}}, "", []string{"a"})
	for _, ref := range []api.MediaRef{mediaRef(conv, plain, hash), mediaRef("qq:group:90003", id, hash), mediaRef(conv, id, cover)} {
		if _, err := s.PrepareVideo(ctx, ref); err == nil {
			t.Fatalf("伪造视频凭据通过：%+v", ref)
		}
	}
	if _, err := s.ReadMedia(ctx, mediaRef(conv, plain, cover), 0); err == nil {
		t.Fatal("跨事件封面通过")
	}
	row := store.MediaFetch{EventSeq: seq, Idx: 0, Kind: "video", Hash: hash, State: store.MediaDone, CoverHash: cover, DurationMS: 12345, Width: 160, Height: 90}
	row.Hash = cover
	if err := qq.PutMediaFetch(row); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadMedia(ctx, mediaRef(conv, id, cover), 0); err == nil {
		t.Fatal("父 hash 不匹配的封面通过")
	}
	t.Log("消息投影打开文件 0 次；封面与视频自身凭据通过，跨事件/跨会话/父 hash 不匹配被拒")
}

func TestForwardVideoCredentials(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	st, _ := set.Platform("qq")
	ctx := context.Background()
	video := putMedia(t, root, "qq", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"))
	poster := putMedia(t, root, "qq", pngBytes)
	outerVideo := putMedia(t, root, "qq", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom\x01"))
	opens := 0
	s.openMedia = func(data, platform, hash string) (*os.File, error) {
		opens++
		return mediastore.OpenMedia(data, platform, hash)
	}
	for _, conv := range []string{"qq:group:90002", "qq:private:90001:90003"} {
		t.Run(conv, func(t *testing.T) {
			inner := types.ForwardTree{ID: "inner", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: types.SegVideo, Hash: video}, {Kind: types.SegImage, Hash: poster}}}}}
			outer := types.ForwardTree{ID: "outer", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: types.SegForward, Ref: "inner"}, {Kind: types.SegImage, Hash: poster}, {Kind: types.SegVideo, Hash: outerVideo}}}}}
			for _, tree := range []types.ForwardTree{inner, outer} {
				if err := st.PutForward(ctx, tree, 1000, conv); err != nil {
					t.Fatal(err)
				}
			}
			id := addSegs(t, st, "qq", conv, map[string]any{"segs": []types.Seg{{Kind: types.SegForward, Ref: "outer"}, {Kind: types.SegVideo, Hash: video}}}, "", []string{"a"})
			read, err := s.ReadForward(ctx, api.ForwardRequest{Source: mediaRef(conv, id, video).Source, ID: "inner", Via: []string{"outer"}})
			if err != nil {
				t.Fatal(err)
			}
			ref := *read.Value.Nodes[0].Segments[0].Media
			path, err := s.PrepareVideo(ctx, ref)
			if err != nil {
				t.Fatalf("里层视频不能播放：%v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom" {
				t.Fatalf("播放副本内容：%q %v", got, err)
			}
			outerRef := ref
			outerRef.Forward, outerRef.Hash = []string{"outer"}, outerVideo
			if _, err := s.PrepareVideo(ctx, outerRef); err != nil {
				t.Fatalf("外层视频不能播放：%v", err)
			}
			for _, bad := range []struct {
				name                         string
				path                         []string
				hash, conversation, platform string
			}{
				{"省略根", []string{"inner"}, video, conv, "qq"},
				{"伪造根", []string{"other", "inner"}, video, conv, "qq"},
				{"循环", []string{"outer", "outer"}, video, conv, "qq"},
				{"超深", make([]string, types.ForwardDepth+1), video, conv, "qq"},
				{"树里没有视频", []string{"outer"}, video, conv, "qq"},
				{"封面冒充视频", []string{"outer", "inner"}, poster, conv, "qq"},
				{"不存在hash", []string{"outer", "inner"}, strings.Repeat("a", 64), conv, "qq"},
				{"跨会话", []string{"outer", "inner"}, video, "qq:group:90004", "qq"},
				{"跨平台", []string{"outer", "inner"}, video, conv, "local"},
				{"本地私聊", []string{"outer", "inner"}, video, "console:a", "local"},
				{"本地群聊", []string{"outer", "inner"}, video, "home:room", "local"},
			} {
				t.Run(bad.name, func(t *testing.T) {
					forged := ref
					opens = 0
					forged.Forward, forged.Hash = bad.path, bad.hash
					forged.Source.Conversation = keyOf(bad.conversation)
					forged.Source.Event.Platform = bad.platform
					if _, err := s.PrepareVideo(ctx, forged); err == nil {
						t.Fatal("伪造凭据通过")
					}
					if opens != 0 {
						t.Fatalf("未获授权就打开媒体 %d 次", opens)
					}
				})
			}
			image := *read.Value.Nodes[0].Segments[1].Media
			if _, err := s.ReadMedia(ctx, image, 0); err != nil {
				t.Fatalf("邻居图片授权改变：%v", err)
			}
			image.Forward = []string{"inner"}
			if _, err := s.ReadMedia(ctx, image, 0); err == nil {
				t.Fatal("图片跳过根通过")
			}
		})
	}
}
