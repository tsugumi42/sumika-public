package app

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/history"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

func directImagePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1200, 600))
	for y := range 600 {
		for x := range 1200 {
			img.SetNRGBA(x, y, color.NRGBA{R: 240, G: 240, B: 240, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func imageModel(t *testing.T, modalities string) llm.Model {
	t.Helper()
	var m llm.Model
	if err := json.Unmarshal([]byte(`{"id":"`+testModel+`","modalities":`+modalities+`}`), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPrivateDirectImageRouting(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.Close()
	data := directImagePNG(t)
	model := imageModel(t, `{"input":["text","image"]}`)
	for _, tc := range []struct {
		name, channel, scene, actor, model, hash string
		count                                    int
		forward, disabled, hidden, quote         bool
		want                                     int
	}{
		{name: "local two", channel: "console", scene: "private", actor: "owner", count: 2, want: 2},
		{name: "local quote block", channel: "console", scene: "private", actor: "owner", count: 1, quote: true, want: 1},
		{name: "qq owner", channel: "qq", scene: "private", actor: "owner", count: 1, want: 1},
		{name: "five fallback", channel: "qq", scene: "private", actor: "owner", count: 5},
		{name: "unknown model", channel: "qq", scene: "private", actor: "owner", count: 1, model: "null"},
		{name: "text model", channel: "qq", scene: "private", actor: "owner", count: 1, model: `{"input":["text"]}`},
		{name: "missing original", channel: "qq", scene: "private", actor: "owner", count: 1, hash: strings.Repeat("a", 64)},
		{name: "bad image", channel: "qq", scene: "private", actor: "owner", count: 1, hash: "invalid"},
		{name: "disabled", channel: "qq", scene: "private", actor: "owner", count: 1, disabled: true},
		{name: "forward", channel: "qq", scene: "private", actor: "owner", count: 1, forward: true},
		{name: "local group", channel: "console", scene: "group", actor: "owner", count: 1},
		{name: "qq group", channel: "qq", scene: "group", actor: "owner", count: 1},
		{name: "peer", channel: "qq", scene: "private", actor: "human", count: 1},
		{name: "unobserved", channel: "qq", scene: "private", actor: "owner", count: 1, hidden: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platform, conv := "qq", "qq:private:"+testAccount+":"+testOwner
			if tc.channel == "console" {
				platform, conv = "local", "console:"+testChar
			}
			if tc.scene == "group" {
				conv = tc.channel + ":group:745"
			}
			st, err := a.set.ForConversation(conv)
			if err != nil {
				t.Fatal(err)
			}
			hash := tc.hash
			if hash == "" {
				hash, _, err = mediastore.Put(a.paths.Root, platform, bytes.NewReader(data), 20<<20)
				if err != nil {
					t.Fatal(err)
				}
			}
			segs := make([]types.Seg, tc.count)
			for i := range segs {
				segs[i] = types.Seg{Kind: types.SegImage, Hash: hash}
			}
			if tc.forward {
				segs = append(segs, types.Seg{Kind: types.SegForward, Ref: "fixture"})
			}
			observers := []string{testChar}
			if tc.hidden {
				observers = nil
			}
			body := map[string]any{"segs": segs, "line": "看图", "seen": "编的已缓存描述"}
			if tc.channel == "console" { // 本地私聊按消息块的附件存图
				atts := make([]history.Attachment, tc.count)
				for i := range atts {
					atts[i] = history.Attachment{Kind: "image", Hash: hash}
				}
				part := history.Part{Text: "看图", Attachments: atts}
				if tc.quote { // 引用回复块里的图也走同一条直给路径
					part.Quote = &history.Quote{ID: "q1", Text: "被引用的话"}
				}
				body = map[string]any{"local_parts": []history.Part{part}, "seen": "编的已缓存描述"}
			}
			seq, err := st.AppendEvent(store.NewEvent{Channel: tc.channel, Conversation: conv, ActorKind: tc.actor, ActorID: "owner", Origin: tc.actor, Kind: "message", Text: "看图", Segments: body, CreatedAt: e.now.UnixMilli()}, observers)
			if err != nil {
				t.Fatal(err)
			}
			in := turn.Input{CharacterID: testChar, Conversation: conv, Seqs: []int64{seq}, Text: "看图", Audience: audience.Audience{Channel: tc.channel, Scene: tc.scene}, Identity: kernel.Invocation{Scope: kernel.Scope{CharacterID: testChar, Conversation: conv, Platform: platform, Channel: tc.channel, Scene: tc.scene, ActorKind: tc.actor, ActorID: "owner"}}}
			m := model
			if tc.model != "" {
				m = imageModel(t, tc.model)
			}
			a.cfg.Vision.Direct = !tc.disabled
			text, images := a.prepareImages(t.Context(), in, m)
			// 实例重建时按同样条件把图取回：能直给的就带回，不能的一张不带
			if restored := a.restoreImages(t.Context(), in, seq, m); len(restored) != tc.want {
				t.Fatalf("restored=%d want=%d", len(restored), tc.want)
			}
			if len(images) != tc.want {
				t.Fatalf("images=%d want=%d text=%s", len(images), tc.want, text)
			}
			if tc.want == 0 && tc.scene == "private" && tc.actor == "owner" && !tc.hidden && text != "编的已缓存描述" {
				t.Fatalf("fallback lost description: %s", text)
			}
			if tc.want > 0 && text != "看图" {
				t.Fatalf("direct changed text: %s", text)
			}
		})
	}
}

func TestLocalImageRequestKeepsImagesAcrossTurns(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768,"modalities":{"input":["text","image"]}}`)}
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Models: models, Reply: llmtest.Text("看到了")}, &e.bodies, &e.mu))
	defer srv.Close()
	e.llm = srv
	a := e.open()
	d := a.Desktop()
	defer func() { d.Close(); a.Close() }()
	key := localKey("console:" + testChar)
	imageBytes := directImagePNG(t)
	if err := d.Reader.SendLocalImages(t.Context(), key, "图片里是什么", [][]byte{imageBytes, imageBytes}, nil); err != nil {
		t.Fatal(err)
	}
	st, err := a.set.ForConversation(key.Conversation)
	if err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := st.Events.QueryRow(`SELECT seq FROM events WHERE actor_kind='owner' AND conversation=?`, key.Conversation).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err := d.Reader.SendLocal(t.Context(), key, "继续", nil); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	bodies := append([]string(nil), e.bodies...)
	e.mu.Unlock()
	var foreground []string
	for _, body := range bodies {
		var r struct {
			Model string `json:"model"`
		}
		json.Unmarshal([]byte(body), &r)
		if r.Model == testModel {
			foreground = append(foreground, body)
		}
	}
	if len(foreground) != 2 {
		t.Fatalf("foreground requests=%d", len(foreground))
	}
	// 下一轮实例按工作上下文重建：图按事件从媒体库取回，仍在原来那条消息上
	for i, body := range foreground {
		if strings.Count(body, `"type":"image_url"`) != 2 {
			t.Fatalf("request %d lost images: %s", i, body)
		}
	}
	if strings.Count(foreground[1], "图片里是什么") != 1 {
		t.Fatalf("rebuilt request duplicated or lost the text: %s", foreground[1])
	}
	_, msgs, _, err := history.For(st.Events, testChar, key.Conversation, 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range msgs {
		if m.Seq == seq {
			found = strings.Contains(m.Text, "图片里是什么")
		}
	}
	if !found {
		t.Fatal("rebuilt history lost the text")
	}
	var leaks int
	if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE segments_json LIKE '%data:image/%'`).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatal("payload persisted", leaks, err)
	}
	if err := a.set.TraceStore().Trace.QueryRow(`SELECT count(*) FROM model_runs WHERE coalesce(request_json,'') LIKE '%data:image/%' OR coalesce(context_json,'') LIKE '%data:image/%'`).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatal("payload traced", leaks, err)
	}
	var ctxJSON string
	if err := a.set.TraceStore().Trace.QueryRow(`SELECT context_json FROM model_runs WHERE stage='reply' ORDER BY started_at LIMIT 1`).Scan(&ctxJSON); err != nil || !strings.Contains(ctxJSON, `"images":{"tool":0,"user":2}`) {
		t.Fatalf("run record should say 2 images: %s %v", ctxJSON, err)
	}
	if err := d.Reader.SendLocalImages(context.Background(), api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: "console:group:745"}, "", [][]byte{imageBytes}, nil); err == nil {
		t.Fatal("group accepted local image interface")
	}
}
