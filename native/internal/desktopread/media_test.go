package desktopread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/datalock"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/testutil/junction"
)

// mediaFixture 在带空格、中文和 # % & 的数据根里真开 local/qq 两个库（T5 的特殊字符数据根）。
func mediaFixture(t *testing.T) (*Service, *store.Set, string) {
	t.Helper()
	p, err := paths.Init(filepath.Join(t.TempDir(), "数据 #%&"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := datalock.LockData(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	s := New(set, []api.CharacterView{{ID: "a", Name: "编造甲"}, {ID: "b", Name: "编造乙"}}, nil).WithData(p.Root)
	t.Cleanup(s.Close)
	return s, set, p.Root
}

func putMedia(t *testing.T, root, platform string, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	dir := filepath.Join(root, "go", "media", platform, h[:2])
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, h), data, 0644); err != nil {
		t.Fatal(err)
	}
	return h
}

func addSegs(t *testing.T, st *store.Store, channel, conv string, segs any, reply string, obs []string) string {
	t.Helper()
	e := store.NewEvent{Channel: channel, Conversation: conv, Kind: "message", ActorKind: "human", ActorID: "90001", Text: "[图片]", Segments: segs, CreatedAt: time.Now().UnixMilli()}
	if reply != "" {
		e.ReplyTo = &reply
	}
	seq, err := st.AppendEvent(e, obs)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err = st.Events.QueryRow(`SELECT id FROM events WHERE seq=?`, seq).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func image(h string) map[string]any {
	return map[string]any{"segs": []any{map[string]any{"kind": "image", "hash": h}}}
}

func mediaRef(conv, id, h string) api.MediaRef {
	k := keyOf(conv)
	return api.MediaRef{Source: api.EventRef{Conversation: k, Event: api.EventKey{Platform: k.Platform, EventID: id}}, Hash: h}
}

var pngBytes = []byte("\x89PNG\r\n\x1a\n编造的图片内容")

func TestMediaPlatformCredentials(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	local, _ := set.Platform("local")
	qq, _ := set.Platform("qq")
	h := putMedia(t, root, "local", pngBytes)
	putMedia(t, root, "qq", pngBytes)
	ctx := context.Background()
	localID := addSegs(t, local, "console", "console:a", image(h), "", nil)
	group := "qq:group:90002"
	qqID := addSegs(t, qq, "qq", group, image(h), "", []string{"a"})
	hidden := addSegs(t, qq, "qq", group, image(h), "", nil)
	plain := addSegs(t, qq, "qq", group, map[string]any{"segs": []any{map[string]any{"kind": "text", "text": "编造"}}}, "", []string{"a"})
	quoting := addSegs(t, qq, "qq", group, map[string]any{"segs": []any{map[string]any{"kind": "text", "text": "引用"}}}, qqID, []string{"b"})
	quotingHidden := addSegs(t, qq, "qq", group, map[string]any{"segs": []any{}}, hidden, []string{"a"})

	for _, ref := range []api.MediaRef{mediaRef("console:a", localID, h), mediaRef(group, qqID, h), mediaRef(group, quoting, h)} {
		r, err := s.ReadMedia(ctx, ref, 256)
		if err != nil || r.State != api.Ready || !bytes.Equal(r.Value.Bytes, pngBytes) || r.Value.MIME != "image/png" {
			t.Fatalf("合法凭据 %v：state=%s err=%v mime=%s", ref.Source.Event, r.State, err, r.Value.MIME)
		}
	}
	crossPlatform := mediaRef("console:a", localID, h)
	crossPlatform.Source.Conversation = keyOf(group)
	crossPlatform.Source.Event.Platform = "qq"
	wrongPlatform := mediaRef(group, qqID, h)
	wrongPlatform.Source.Event.Platform = "local"
	other := sha256.Sum256([]byte("别的图"))
	reject := map[string]api.MediaRef{
		"别平台的事件当 qq 凭据": crossPlatform,
		"事件平台和会话不一致":    wrongPlatform,
		"没被观察的事件":       mediaRef(group, hidden, h),
		"事件里没有这个 hash":  mediaRef(group, plain, h),
		"引用一条不可见的事件":    mediaRef(group, quotingHidden, h),
		"事件不带的 hash":    mediaRef(group, qqID, hex.EncodeToString(other[:])),
	}
	for name, ref := range reject {
		if _, err := s.ReadMedia(ctx, ref, 0); !errors.Is(err, ErrMediaRef) {
			t.Errorf("%s：err=%v，应拒绝", name, err)
		}
	}
	for name, hash := range map[string]string{"大写": "AB" + h[2:], "带路径": "../" + h[3:], "短": h[:10], "空": ""} {
		if _, err := s.ReadMedia(ctx, mediaRef(group, qqID, hash), 0); !errors.Is(err, store.ErrDisplayParameter) {
			t.Errorf("非法 hash %s：err=%v", name, err)
		}
	}
	for _, tier := range []api.MediaTier{-1, 1, 200, 2048} {
		if _, err := s.ReadMedia(ctx, mediaRef(group, qqID, h), tier); !errors.Is(err, store.ErrDisplayParameter) {
			t.Errorf("tier %d：err=%v", tier, err)
		}
	}

	// 删一个平台的文件：另一个平台照常读，缺的那边报缺文件、不建空文件、描述行不动。
	if _, err := qq.Events.Exec(`INSERT INTO media_descriptions(hash,model,prompt_version,prep_version,ok,brief,at) VALUES (?, 'm', 'v', 1, 1, '编造的描述', 1)`, h); err != nil {
		t.Fatal(err)
	}
	qqFile := filepath.Join(root, "go", "media", "qq", h[:2], h)
	if err := os.Remove(qqFile); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadMedia(ctx, mediaRef(group, qqID, h), 0); !errors.Is(err, mediastore.ErrMediaMissing) {
		t.Fatalf("qq 文件删了：err=%v", err)
	}
	if _, err := os.Lstat(qqFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺文件后不能建出空文件：%v", err)
	}
	if r, err := s.ReadMedia(ctx, mediaRef("console:a", localID, h), 0); err != nil || !bytes.Equal(r.Value.Bytes, pngBytes) {
		t.Fatalf("local 不受 qq 缺文件影响：%v", err)
	}
	var text string
	if err := qq.Events.QueryRow(`SELECT brief FROM media_descriptions WHERE hash=?`, h).Scan(&text); err != nil || text != "编造的描述" {
		t.Fatalf("描述行被改：%q %v", text, err)
	}
}

func TestMediaSizeLimit(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	group := "qq:group:90002"
	exact := bytes.Repeat([]byte{7}, mediaMax)
	over := append(bytes.Repeat([]byte{8}, mediaMax), 9)
	he, ho := putMedia(t, root, "qq", exact), putMedia(t, root, "qq", over)
	ide := addSegs(t, qq, "qq", group, image(he), "", []string{"a"})
	ido := addSegs(t, qq, "qq", group, image(ho), "", []string{"a"})
	if r, err := s.ReadMedia(context.Background(), mediaRef(group, ide, he), 0); err != nil || r.Value.Size != mediaMax {
		t.Fatalf("正好 20 MiB：size=%d err=%v", r.Value.Size, err)
	}
	if _, err := s.ReadMedia(context.Background(), mediaRef(group, ido, ho), 0); !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("多 1 字节：err=%v", err)
	}
}

func TestMediaJunctionRejected(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("联接只在 Windows 上构造")
	}
	s, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	group := "qq:group:90002"
	h := putMedia(t, root, "qq", pngBytes)
	id := addSegs(t, qq, "qq", group, image(h), "", []string{"a"})
	shard := filepath.Join(root, "go", "media", "qq", h[:2])
	outside := filepath.Join(t.TempDir(), "外面")
	if err := os.Rename(shard, outside); err != nil {
		t.Fatal(err)
	}
	if err := junction.Create(shard, outside); err != nil {
		t.Skipf("建不了联接：%v", err)
	}
	if _, err := s.ReadMedia(context.Background(), mediaRef(group, id, h), 0); !errors.Is(err, mediastore.ErrMediaLink) {
		t.Fatalf("分片目录是联接：err=%v", err)
	}
}

// 旧导入段只补展示元数据和缺失的 hash；版本对不上保留中立段、不给媒体凭据。
func TestLegacySegmentsDisplay(t *testing.T) {
	t.Parallel()
	s, set, root := mediaFixture(t)
	qq, _ := set.Platform("qq")
	group := "qq:group:90002"
	h := putMedia(t, root, "qq", pngBytes)
	legacy := func(version int) map[string]any {
		return map[string]any{
			"segs": []any{map[string]any{"kind": "emoji"}, map[string]any{"kind": "text", "text": "编造"}, map[string]any{"kind": "face", "ref": "14"}},
			"import_meta": map[string]any{"version": version, "legacy_segments": []any{
				map[string]any{"type": "image", "hash": h, "width": 120, "height": 90, "animated": true, "market": true, "summary": "[动画表情]"},
				map[string]any{"type": "text", "text": "编造"},
				map[string]any{"type": "face", "id": "14"},
			}},
		}
	}
	good := addSegs(t, qq, "qq", group, legacy(1), "", []string{"a"})
	unknown := addSegs(t, qq, "qq", group, legacy(2), "", []string{"a"})
	mismatch := addSegs(t, qq, "qq", group, map[string]any{"segs": []any{map[string]any{"kind": "image"}}, "import_meta": map[string]any{"version": 1, "legacy_segments": []any{map[string]any{"type": "face", "id": "1"}}}}, "", []string{"a"})
	page, err := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(group), Limit: 10}, "")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]api.MessageView{}
	for _, m := range page.Value.Items {
		byID[m.Ref.Event.EventID] = m
	}
	g := byID[good].Segments
	if len(g) != 3 || g[0].Media == nil || g[0].Media.Hash != h || g[0].Width != 120 || g[0].Height != 90 || !g[0].Animated || !g[0].Sticker || g[0].Summary != "[动画表情]" || g[0].Issue != "" {
		t.Fatalf("对上号的旧段：%+v", g)
	}
	if g[1].Text != "编造" || g[2].Kind != "face" || g[2].Ref != "14" {
		t.Fatalf("中立段原样保留：%+v", g)
	}
	if r, err := s.ReadMedia(context.Background(), *g[0].Media, 0); err != nil || !bytes.Equal(r.Value.Bytes, pngBytes) {
		t.Fatalf("旧段补的 hash 走生产读取：%v", err)
	}
	for name, id := range map[string]string{"版本未知": unknown, "类型对不上": mismatch} {
		u := byID[id].Segments
		if len(u) == 0 || u[0].Media != nil || u[0].Issue == "" || u[0].Width != 0 {
			t.Fatalf("%s：%+v", name, u)
		}
		if _, err := s.ReadMedia(context.Background(), mediaRef(group, id, h), 0); !errors.Is(err, ErrMediaRef) {
			t.Fatalf("%s 不能凭旧段拿图：%v", name, err)
		}
	}
}

func TestFacesReadOnly(t *testing.T) {
	t.Parallel()
	s, _, root := mediaFixture(t)
	ctx := context.Background()
	if _, err := s.ReadFaceIndex(ctx); !errors.Is(err, ErrFaceIndex) {
		t.Fatalf("没有索引：%v", err)
	}
	dir := filepath.Join(root, "cache", "qq", "faces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	index := `{"version":"9.9","faces":{"14":{"name":"微笑","kind":"classic"},"2":{"name":"撇嘴","kind":"classic"},"128512":{"name":"笑","kind":"emoji","char":"😀"},"../x":{"name":"坏"}}}`
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "14.png"), pngBytes, 0644); err != nil {
		t.Fatal(err)
	}
	r, err := s.ReadFaceIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, f := range r.Value.Items {
		ids = append(ids, f.ID)
	}
	if len(ids) != 3 || ids[0] != "2" || ids[1] != "14" || ids[2] != "128512" || r.Value.Items[2].Char != "😀" {
		t.Fatalf("索引：%+v", r.Value.Items)
	}
	if p, err := s.ReadFacePNG(ctx, "14"); err != nil || !bytes.Equal(p.Value.Bytes, pngBytes) {
		t.Fatalf("取图：%v", err)
	}
	if _, err := s.ReadFacePNG(ctx, "2"); !errors.Is(err, ErrFaceMissing) {
		t.Fatalf("缺图：%v", err)
	}
	for _, bad := range []string{"", "1234567", "../14", "14.png", "1a"} {
		if _, err := s.ReadFacePNG(ctx, bad); !errors.Is(err, store.ErrDisplayParameter) {
			t.Fatalf("编号 %q：%v", bad, err)
		}
	}
}

func TestReleasedOutboundAndIncomingDownloadProjection(t *testing.T) {
	t.Parallel()
	s, set, _ := mediaFixture(t)
	for _, conv := range []string{"console:a", "console:group:invented", "qq:private:90001:90003", "qq:group:90002"} {
		t.Run(conv, func(t *testing.T) {
			st, err := set.ForConversation(conv)
			if err != nil {
				t.Fatal(err)
			}
			if id, group := store.LocalGroupID(conv); group {
				if err := st.CreateLocalGroup(context.Background(), store.LocalGroup{ID: id, Name: "编造测试群", Members: []store.LocalGroupMember{{CharacterID: "a"}, {CharacterID: "b"}}}); err != nil {
					t.Fatal(err)
				}
			}
			body := []types.BodySegment{{Kind: types.BodyVideo, Media: &types.MediaResource{ID: "released-video", MIME: "video/mp4", Width: 320, Height: 180, Size: 1234}}, {Kind: types.BodyGIF, Media: &types.MediaResource{ID: "released-gif", MIME: "image/gif", Width: 64, Height: 48, Size: 567}}}
			outID := addSegs(t, st, keyOf(conv).Platform, conv, map[string]any{"parts": []types.Part{{Kind: types.PartMessage, Body: body}}}, "", []string{"a"})
			inID := ""
			if keyOf(conv).Platform == "qq" {
				inID = addSegs(t, st, "qq", conv, map[string]any{"segs": []types.Seg{{Kind: types.SegVideo, Size: 789}, {Kind: types.SegFile, File: "编的文件.pdf", Size: 456}}}, "", []string{"a"})
			}
			page, err := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf(conv), Limit: 20}, "")
			if err != nil {
				t.Fatal(err)
			}
			foundOut, foundIn := false, inID == ""
			for _, message := range page.Value.Items {
				switch message.Ref.Event.EventID {
				case outID:
					foundOut = true
					if len(message.Segments) != 2 {
						t.Fatal("出站媒体顺序丢失", message.Segments)
					}
					for i, seg := range message.Segments {
						m := body[i].Media
						if seg.MediaState != "outbound-released" || seg.Width != m.Width || seg.Height != m.Height || seg.Size != m.Size || seg.Animated != (i == 1) || seg.Download != nil || seg.Media != nil {
							t.Fatalf("释放资源被当成收件或丢元信息：%+v", seg)
						}
					}
				case inID:
					foundIn = true
					if len(message.Segments) != 2 {
						t.Fatal("收件媒体丢失", message.Segments)
					}
					for i, seg := range message.Segments {
						if seg.Download == nil || seg.Download.Source.Event.EventID != inID || seg.Download.Index != i || seg.MediaState == "outbound-released" {
							t.Fatalf("收件下载凭据丢失或串到出站：%+v", seg)
						}
					}
				}
			}
			if !foundOut || !foundIn {
				t.Fatal("缺少已存事件", foundOut, foundIn)
			}
		})
	}
}

// 出站的卡片图、视频带着“语义文字”存下来；资源释放后界面读到的段要带上它，
// 不然只剩一个图片占位，看不出这是什么内容（待办“链接卡片的语义文字到界面丢了”）。
func TestReleasedOutboundKeepsSemanticText(t *testing.T) {
	t.Parallel()
	s, set, _ := mediaFixture(t)
	st, err := set.ForConversation("console:a")
	if err != nil {
		t.Fatal(err)
	}
	body := []types.BodySegment{
		{Kind: types.BodyText, Text: "正文", Semantic: "文字段不改写"},
		{Kind: types.BodyImage, Media: &types.MediaResource{ID: "released-card", MIME: "image/png", Width: 64, Height: 48, Size: 10}, Semantic: "卡片：编的标题"},
		{Kind: types.BodyVideo, Media: &types.MediaResource{ID: "released-v", MIME: "video/mp4", Width: 32, Height: 18, Size: 20}, Semantic: "视频说明"},
		{Kind: types.BodyImage, Media: &types.MediaResource{ID: "released-plain", MIME: "image/png", Width: 8, Height: 8, Size: 5}},
	}
	outID := addSegs(t, st, keyOf("console:a").Platform, "console:a", map[string]any{"parts": []types.Part{{Kind: types.PartMessage, Body: body}}}, "", []string{"a"})
	page, err := s.ReadMessages(context.Background(), api.ConversationRequest{Key: keyOf("console:a"), Limit: 20}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Value.Items {
		if m.Ref.Event.EventID != outID {
			continue
		}
		if len(m.Segments) != 4 {
			t.Fatalf("段数：%+v", m.Segments)
		}
		want := []string{"", "卡片：编的标题", "视频说明", ""}
		for i, seg := range m.Segments {
			if seg.Summary != want[i] {
				t.Fatalf("第 %d 段 Summary=%q，想要 %q", i, seg.Summary, want[i])
			}
		}
		return
	}
	t.Fatal("没读到刚存的消息")
}
