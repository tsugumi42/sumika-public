package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
)

// 正式桌面写入口、真实本地库和 Hub；假网关主动调详情，验证结果确实进入下一次请求。
func TestLocalLinkSummaryReachesModel(t *testing.T) {
	t.Parallel()
	for _, scene := range []string{"private", "topic", "group"} {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/enabled=%t", scene, enabled), func(t *testing.T) {
				t.Parallel()
				e := newEnv(t)
				cardOnly(t, e, "birch")
				e.configure(fmt.Sprintf("linkReader:\n  context:\n    enabled: %t\n", enabled))
				file := filepath.Join(e.data, "config", "sumika.yml")
				cfg, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				cfg = []byte(strings.Replace(string(cfg), "characters:\n", "characters:\n  birch:\n    model: "+testModel+"\n", 1))
				if err := os.WriteFile(file, cfg, 0600); err != nil {
					t.Fatal(err)
				}
				script := llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}, Reply: llmtest.Text("本地回复")}
				if enabled {
					script.Replies = []llmtest.Reply{llmtest.Call("link-detail", "read_link_detail", `{"message":1}`), llmtest.Text("看过详情了")}
				}
				srv := httptest.NewServer(llmtest.Handler(script, &e.bodies, &e.mu))
				t.Cleanup(srv.Close)
				e.llm = srv
				f := &linkFake{}
				e.linkFetcher = f
				a := e.open()
				d := a.Desktop()
				t.Cleanup(func() { d.Close(); a.Close() })
				ctx := context.Background()
				key := localKey("console:" + testChar)
				st, _ := a.set.For("console")
				if scene == "topic" {
					if _, err := st.Events.Exec(`INSERT INTO local_topics(id,character_id,created_at) VALUES ('links',?,1)`, testChar); err != nil {
						t.Fatal(err)
					}
					key = localKey(key.Conversation + ":topic:links")
				}
				text := "看看这个 https://x.com/someone/status/123456"
				if scene == "group" {
					g, err := d.Reader.CreateLocalGroup(ctx, api.LocalGroupCreate{Name: "编的会客室", Characters: []string{testChar, "birch"}})
					if err != nil {
						t.Fatal(err)
					}
					key = g.Key
					err = d.Reader.SendLocalGroup(ctx, api.LocalGroupSend{Key: key, Text: text, Mentions: []string{testChar, "birch"}})
				} else {
					err = d.Reader.SendLocal(ctx, key, text, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				e.mu.Lock()
				bodies := append([]string(nil), e.bodies...)
				e.mu.Unlock()
				if len(bodies) == 0 {
					t.Fatal("没有走到模型请求")
				}
				for i, body := range bodies {
					got := strings.Contains(body, "[链接资料]") && strings.Contains(body, "X 编的作者：编的推文正文")
					if got != enabled || strings.Contains(body, `"read_link_detail"`) != enabled {
						t.Fatalf("第 %d 个请求的摘要或工具开关不符 enabled=%t：%s", i+1, enabled, body)
					}
				}
				if enabled {
					if len(bodies) < 2 || !strings.Contains(bodies[1], "作者：编的作者") || !strings.Contains(bodies[1], "正文：\\n编的推文正文") || !strings.Contains(bodies[1], `"role":"tool"`) {
						t.Fatalf("详情结果没有进入第二次请求：%v", bodies)
					}
					if f.calls.Load() != 1 {
						t.Fatalf("整条消息只能取一次，实际 %d", f.calls.Load())
					}
					for _, id := range []string{testChar, "birch", "outside"} {
						notes, err := st.LinkNotes(ctx, key.Conversation, id, 1, 0)
						want := id == testChar || scene == "group" && id == "birch"
						if err != nil || (len(notes) == 1) != want {
							t.Fatalf("%s 的可见性不符：%+v %v", id, notes, err)
						}
					}
				} else if f.calls.Load() != 0 {
					t.Fatalf("关着仍取了 %d 次", f.calls.Load())
				}
				var stored string
				if err := st.Events.QueryRow(`SELECT text FROM events WHERE seq=1 AND conversation=? AND actor_kind='owner'`, key.Conversation).Scan(&stored); err != nil || stored != text {
					t.Fatalf("主人原文落库不符：%q %v", stored, err)
				}
			})
		}
	}
}
