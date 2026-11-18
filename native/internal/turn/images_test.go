package turn

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/history"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/llm/openaichat"
	"sumika-native/internal/tools"
)

func TestImagesSurviveToolNextTurnAndRebuild(t *testing.T) {
	t.Parallel()
	const payload = "data:image/png;base64,uniquefakepayload"
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + callReply("missing_tool", `{}`) + `,` + textReply + `,` + textReply + `,` + textReply + `]`,
		mutate: func(c *Config) {
			c.PrepareImages = func(_ context.Context, in Input, _ llm.Model) (string, []llm.Image) {
				if len(in.Parts) > 0 {
					return in.Text, []llm.Image{{DataURL: payload}}
				}
				return in.Text, nil
			}
			c.RestoreImages = func(_ context.Context, _ Input, seq int64, _ llm.Model) []llm.Image {
				if seq == 1 { // 第一条主人消息带过图
					return []llm.Image{{DataURL: payload}}
				}
				return nil
			}
		},
	})
	conv := consoleConv(h.c.CharacterID)
	hash := strings.Repeat("a", 64)
	wait(t, h.hub.SendLocalParts(context.Background(), h.c.CharacterID, conv, []history.Part{{Text: "first image", Attachments: []history.Attachment{{Kind: "image", Hash: hash}}}}, "first image", nil))
	first := h.messages(0)
	if !strings.Contains(h.body(0), payload) || !strings.Contains(h.body(1), payload) {
		t.Fatal("image missing from initial or tool request")
	}
	h.send("second turn")
	second := h.messages(2)
	if len(second) < len(first) {
		t.Fatal("history shrank")
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("history changed at %d: %s / %s", i, first[i], second[i])
		}
	}
	if !strings.Contains(h.body(2), payload) {
		t.Fatal("image missing from second turn")
	}
	h.hub.Restart(h.c.CharacterID)
	h.send("after restart")
	// 重建后图从事件取回，放在原来那条消息上；文字不变，只有一份
	if !strings.Contains(h.body(3), payload) || strings.Count(h.body(3), "first image") != 1 || strings.Count(h.body(3), payload) != 1 {
		t.Fatal("rebuild must bring the image back onto its original message")
	}
	for _, table := range []struct {
		db    string
		query string
	}{
		{"trace", `SELECT * FROM model_runs`},
		{"trace", `SELECT * FROM kernel_inbox`},
		{"events", `SELECT * FROM events`},
	} {
		db := h.st.Trace
		if table.db == "events" {
			db = h.st.Events
		}
		data, err := json.Marshal(rows(t, db, table.query))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), payload) {
			t.Fatalf("image leaked into %s", table.query)
		}
	}
	var segments string
	if err := h.st.Events.QueryRow(`SELECT segments_json FROM events WHERE conversation=? AND text=?`, conv, "first image").Scan(&segments); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(segments, `"hash":"`+hash+`"`) || !strings.Contains(segments, `"text":"first image"`) {
		t.Fatalf("missing text or hash: %s", segments)
	}
}

func TestImageAccountingAndSerialization(t *testing.T) {
	t.Parallel()
	m := Msg{Role: "user", Text: "hello", Images: []llm.Image{{DataURL: "data:image/png;base64,secret"}}}
	b, err := json.Marshal(m)
	if err != nil || strings.Contains(string(b), "secret") {
		t.Fatalf("message persistence: %s %v", b, err)
	}
	if got := conversationTokens([]Msg{m}) - conversationTokens([]Msg{{Role: "user", Text: "hello"}}); got != ceilDiv35(3000) {
		t.Fatalf("image cost %d", got)
	}
}

func TestImageRetainedThroughChannelCommit(t *testing.T) {
	t.Parallel()
	const url = "data:image/png;base64,channelpayload"
	h := newHarnessOpts(t, harnessOpts{
		replies: `[` + textReply + `,` + textReply + `]`,
		mutate: func(c *Config) {
			c.PrepareImages = func(_ context.Context, in Input, _ llm.Model) (string, []llm.Image) {
				if in.Text == "first" {
					return "prepared first", []llm.Image{{DataURL: url}}
				}
				return in.Text, nil
			}
		},
	})
	r := wait(t, h.inGroup("first", nil))
	h.commitSaid(groupConv, r, nil)
	wait(t, h.inGroup("second", nil))
	if !strings.Contains(h.body(1), url) || !strings.Contains(h.body(1), "prepared first") {
		t.Fatal("commit lost the image or changed its text")
	}
}

func TestImageBatchPreservedThroughCommitAndDrop(t *testing.T) {
	t.Parallel()
	for _, commit := range []bool{true, false} {
		t.Run(map[bool]string{true: "commit", false: "drop"}[commit], func(t *testing.T) {
			t.Parallel()
			const url = "data:image/png;base64,batchpayload"
			h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `]`, mutate: func(c *Config) {
				c.PrepareImages = func(_ context.Context, in Input, _ llm.Model) (string, []llm.Image) {
					if in.Text == "batch one\nbatch two" {
						return in.Text, []llm.Image{{DataURL: url}}
					}
					return in.Text, nil
				}
			}})
			a, b := h.storeGroup("batch one"), h.storeGroup("batch two")
			r := wait(t, h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: groupConv, Seqs: []int64{a, b}, Text: "batch one\nbatch two", Audience: audience.Audience{Channel: "qq", Scene: "group", PersonIDs: []string{"p1"}}, Addressed: true, Scope: "public"}))
			first := h.messages(0)
			if commit {
				h.commitSaid(groupConv, r, nil)
			}
			wait(t, h.inGroup("next", nil))
			second := h.messages(1)
			if !isPrefix(first, second) {
				t.Fatalf("batch prefix changed: %v / %v", first, second)
			}
			if strings.Count(h.body(1), "batch one") != 1 || !strings.Contains(h.body(1), url) {
				t.Fatal("batch duplicated or image lost")
			}
		})
	}
}

func TestToolImagesFollowResultsOnlyForVisionModels(t *testing.T) {
	t.Parallel()
	const url = "data:image/png;base64,toolpayload"
	msgs := []Msg{
		{Role: "user", Text: "看看这个文件"},
		{Role: "assistant", Calls: []Call{{ID: "c1", Name: "read"}, {ID: "c2", Name: "read"}}},
		{Role: "toolResult", CallID: "c1", Text: "Read image file [image/png]", Images: []llm.Image{{DataURL: url}}},
	}
	full := append(append([]Msg(nil), msgs...), Msg{Role: "toolResult", CallID: "c2", Text: "ok"})
	encode := func(msgs []Msg, seeImages bool) string {
		body, err := openaichat.Encode(chat.Request{Model: "m", Style: chat.StyleForeground, Messages: chatMessages(msgs), SeeImages: seeImages})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	seen := encode(msgs, true)
	toolAt, noResultAt, userAt := strings.Index(seen, `"tool_call_id":"c1"`), strings.Index(seen, "No result provided"), strings.Index(seen, "Attached image(s) from tool result:")
	if !(toolAt >= 0 && toolAt < noResultAt && noResultAt < userAt) || strings.Count(seen, url) != 1 {
		t.Fatalf("image message must follow every tool message: %s", seen)
	}
	if blind := encode(msgs, false); strings.Contains(blind, "image_url") || strings.Contains(blind, "Attached image") {
		t.Fatalf("text-only model got an image: %s", blind)
	}
	slimmed, n := (&Built{}).slim(append(full, Msg{Role: "user", Text: "再来"}))
	if n != 1 || len(slimmed[2].Images) != 0 || len(msgs[2].Images) != 1 {
		t.Fatalf("old tool image should drop from the request copy only: %d %v", n, slimmed[2].Images)
	}
}

func TestLateImagesJoinTheRunningTurnOnItsNextStep(t *testing.T) {
	t.Parallel()
	const late = "data:image/png;base64,latepayload"
	var h *harness
	var runID string
	h = newHarnessOpts(t, harnessOpts{
		replies: `[` + callReply("ls", `{}`) + `,` + textReply + `,` + textReply + `]`,
		mutate: func(c *Config) {
			c.PrepareImages = func(_ context.Context, in Input, _ llm.Model) (string, []llm.Image) {
				runID = in.Identity.RunID
				return in.Text, nil
			}
			// 第一步的工具在跑时，等图超时的那张图下完了：这一轮还在跑，补给它；别的运行 id 没有这一轮，补不进去
			c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				if !h.hub.LateImages(runID, []llm.Image{{DataURL: late}}, "（补给你看：）") {
					t.Error("正在跑的这一轮应当收下补图")
				}
				if h.hub.LateImages("不存在的运行", []llm.Image{{DataURL: late}}, "x") {
					t.Error("不存在的运行不该收")
				}
				return tools.Result{Text: "ok"}, nil
			}
		},
	})
	h.send("先回一句")
	if strings.Contains(h.body(0), late) || !strings.Contains(h.body(1), late) || !strings.Contains(h.body(1), "补给你看") {
		t.Fatal("补图应当出现在这一轮的下一步请求，而不是第一步")
	}
	if h.hub.LateImages(runID, []llm.Image{{DataURL: late}}, "x") {
		t.Fatal("结束的运行不该再收补图")
	}
}
