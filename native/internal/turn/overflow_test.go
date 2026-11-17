package turn

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sumika-native/internal/llm"
	"sync"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

const overflowText = "prompt is too long: 213462 tokens > 200000 maximum"

// 假网关用服务商的原文拒绝第一步，摘要以后同一步正常；只多一次回复请求（另有一次摘要请求）。
func TestOverflowRecovers(t *testing.T) {
	t.Parallel()
	for _, sample := range []struct{ scene, text string }{
		{"private", overflowText},
		{"private", "Your input exceeds the context window of this model"},
		{"private", "The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)"},
		{"private", "This model's maximum prompt length is 131072 but the request contains 537812 tokens"},
		{"group", overflowText},
	} {
		t.Run(sample.scene+"/"+sample.text, func(t *testing.T) {
			t.Parallel()
			h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `,` + textReply + `]`, mutate: smallKeep,
				intercept: func(n int, _ string, w http.ResponseWriter, _ *http.Request) bool {
					if n == 1 {
						http.Error(w, sample.text, 400)
						return true
					}
					return false
				}})
			h.seedRounds(8)
			seq := h.storeOwner("现在这句")
			in := consoleInput(h.c.CharacterID, seq, "现在这句")
			in.Audience.Scene = sample.scene
			if sample.scene == "group" {
				in.Scenes = []string{"scene-home-group"}
			}
			o := recv(t, h.hub.Respond(in), "压缩后回复")
			if o.Err != nil || o.Result == nil || o.Result.Reply != "好的" || h.postCount() != 3 {
				t.Fatalf("拒绝→摘要→同一步正常：%+v，请求 %d 次", o, h.postCount())
			}
			if eps := h.epochs(); len(eps) != 1 || eps[0].status != "ok" || eps[0].trigger != "overflow" {
				t.Fatalf("成功的溢出快照：%+v", eps)
			}
			body := h.body(1)
			if !strings.Contains(body, "前面的聊天已经整理成下面的摘要") || strings.Contains(body, strings.Repeat("甲", 10)) || strings.Count(body, "现在这句") != 1 {
				t.Fatalf("新快照、本轮一句只出现一次：%s", body)
			}
			rows := h.runRows()
			if len(rows) != 3 || rows[0].stage != "reply-retry" || !strings.Contains(rows[0].err, "上下文超了，压缩后重来") || rows[0].rebuild != "" ||
				rows[1].stage != "compact" || rows[2].stage != "reply" || rows[2].rebuild != "compact" || !h.logged("上下文超了，压缩后重来") {
				t.Fatalf("失败一行不进回复前缀链、成功回复标 compact、日志说明：%+v", rows)
			}
			if tr := h.rebuilds()[0].Trace; tr == nil || (tr.Reason != RebuildProviderExceed && !slices.Contains(tr.Also, RebuildProviderExceed)) || !tr.Snapshot {
				t.Fatalf("服务商报超长后压缩重建的那一轮，留痕里有 provider_overflow 且用上了新快照：%+v", tr)
			}
			if cls := h.classRows(); len(cls) != 3 || cls[0][1] != llm.ClassContextOverflow || cls[1][1] != "" || cls[2][1] != "" {
				t.Fatalf("上下文超长那一行带 context_overflow，成功的压缩和回复没有类别：%v", cls)
			}
		})
	}
}

func TestOverflowStopsRecovery(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"第二次还超", "压缩失败", "关了自动压缩", "QQ私聊", "QQ群", "别的4xx", "已经出字"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			summary := summaryReply
			if name == "压缩失败" {
				summary = shortSummary
			}
			h := newHarnessOpts(t, harnessOpts{replies: `[` + summary + `,` + textReply + `]`, mutate: func(c *Config) {
				smallKeep(c)
				c.Context.NoAutoCompact = name == "关了自动压缩"
			}, intercept: func(n int, body string, w http.ResponseWriter, _ *http.Request) bool {
				if strings.Contains(body, compactMarker) {
					return false
				}
				// 上限改坏时让它继续得到溢出，测试以有时限的结果和请求数抓住循环。
				if n == 1 || name == "第二次还超" {
					text := overflowText
					if name == "别的4xx" {
						text = "invalid parameter: temperature"
					}
					if name == "已经出字" {
						w.Header().Set("Content-Type", "text/event-stream")
						sseChunk(w, "前半句")
						fmt.Fprintf(w, "data: {\"error\":{\"message\":%q,\"type\":\"api_error\"}}\n\n", text)
						return true
					}
					http.Error(w, text, 400)
					return true
				}
				return false
			}})
			h.seedRounds(8)
			out := func() <-chan Outcome { return h.hub.Send(h.c.CharacterID, "这句不能丢") }
			if strings.HasPrefix(name, "QQ") {
				conv, aud := groupConv, groupAud
				if name == "QQ私聊" {
					conv, aud.Scene = "qq:private:test-peer", "private"
				}
				aud.Conversation = conv
				appendMessage := func(who, text string) int64 {
					seq, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: who, ActorID: "p1", Origin: who, Kind: "message", Text: text}, []string{h.c.CharacterID})
					if err != nil {
						t.Fatal(err)
					}
					return seq
				}
				for range 8 {
					appendMessage("human", strings.Repeat("群", 300))
					appendMessage("character", strings.Repeat("话", 300))
				}
				out = func() <-chan Outcome {
					seq := appendMessage("human", "这句不能丢")
					return h.hub.Respond(Input{CharacterID: h.c.CharacterID, Conversation: conv, Seqs: []int64{seq}, Text: "这句不能丢", Audience: aud, Addressed: true, Scope: "public"})
				}
			}
			o := recv(t, out(), name+"报错")
			wantPosts, wantEpochs := 1, 0
			if name == "第二次还超" {
				wantPosts, wantEpochs = 3, 1
			} else if name == "压缩失败" {
				wantPosts, wantEpochs = 2, 1
			}
			if o.Err == nil || h.postCount() != wantPosts || len(h.epochs()) != wantEpochs {
				t.Fatalf("应报错、不再恢复：%v，请求 %d/%d 次，快照 %+v（应 %d 份）", o.Err, h.postCount(), wantPosts, h.epochs(), wantEpochs)
			}
			if name != "别的4xx" && !strings.Contains(o.Err.Error(), overflowText) {
				t.Fatalf("保留服务商原错：%v", o.Err)
			}
			if name == "压缩失败" && (h.epochs()[0].status != "failed" || h.hub.liveOf(h.c.CharacterID).b.Reset) {
				t.Fatalf("没压成不能裁剪：%+v", h.epochs())
			}
			if name == "已经出字" && o.Result.Reply != "前半句" {
				t.Fatalf("已经出字的半句保留：%+v", o.Result)
			}
		})
	}
}

// 后续步骤溢出：工具不重跑、结果和插话不丢；重建不能把排着队的下一轮提前带进来。
func TestOverflowKeepsToolStep(t *testing.T) {
	t.Parallel()
	executed := 0
	var h *harness
	h = newHarnessOpts(t, harnessOpts{replies: `[{"toolCalls":[{"id":"r1","name":"ls","args":["{\"path\":\".\"}"]}],` + usage + `},` + summaryReply + `,` + textReply + `]`,
		mutate: func(c *Config) {
			smallKeep(c)
			c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				executed++
				h.storeQueued("排队的下一轮")
				if _, ok := h.hub.Steer(h.c.CharacterID, "工具期间插话"); !ok {
					t.Error("工具期间的插话应该收下")
				}
				return tools.Result{Text: "工具只执行一次的结果"}, nil
			}
		}, intercept: func(n int, _ string, w http.ResponseWriter, _ *http.Request) bool {
			if n == 2 {
				http.Error(w, overflowText, 400)
				return true
			}
			return false
		}})
	h.seedRounds(8)
	r := h.send("先查目录再回答")
	if executed != 1 || r.Reply != "好的" || h.postCount() != 4 || len(r.Steps) != 2 {
		t.Fatalf("只重发失败的步骤：工具 %d 次，请求 %d 次，回复 %+v", executed, h.postCount(), r)
	}
	b := h.body(2)
	if strings.Count(b, "先查目录再回答") != 1 || strings.Count(b, "工具只执行一次的结果") != 1 || !strings.Contains(b, `"tool_call_id":"r1"`) || !strings.Contains(b, "前面的聊天已经整理成下面的摘要") {
		t.Fatalf("快照之后保留完整工具往返和本轮一句：%s", b)
	}
	if strings.Contains(b, "排队的下一轮") || strings.Count(b, "工具期间插话") != 1 {
		t.Fatalf("排队的下一轮不能提前带入，插话只接回一次：%s", b)
	}
}

func TestCompactingCallback(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"回复前", "提前", "手动", "溢出", "失败", "没有可压的", "回调panic"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var events []string
			summary := summaryReply
			if name == "失败" {
				summary = shortSummary
			}
			replies := `[` + textReply + `,` + summary + `,` + textReply + `]`
			if name == "提前" {
				replies = `[` + textReply + `,` + textReply + `,` + summary + `]`
			} else if name == "手动" || name == "失败" || name == "回调panic" || name == "溢出" {
				replies = `[` + summary + `,` + textReply + `]`
			}
			h := newHarnessOpts(t, harnessOpts{replies: replies, mutate: func(c *Config) {
				smallKeep(c)
				c.OnCompacting = func(character, conv string, starting bool) {
					mu.Lock()
					events = append(events, fmt.Sprintf("%s|%s|%v", character, conv, starting))
					mu.Unlock()
					if name == "回调panic" {
						panic("界面回调坏了")
					}
				}
			}, intercept: func(n int, body string, w http.ResponseWriter, _ *http.Request) bool {
				if strings.Contains(body, compactMarker) {
					mu.Lock()
					if len(events) != 1 || !strings.HasSuffix(events[0], "|true") {
						t.Errorf("摘要请求发出时应只有开始通知：%v", events)
					}
					mu.Unlock()
				}
				if name == "溢出" && n == 1 {
					http.Error(w, overflowText, 400)
					return true
				}
				return false
			}})
			switch name {
			case "回复前":
				h.nearHard()
				h.send("压完再说")
			case "提前":
				h.nearEarly()
				h.send("这一轮说完提前压")
			case "溢出":
				h.seedRounds(8)
				h.send("服务商说超了")
			default:
				if name != "没有可压的" {
					h.seedRounds(8)
				}
				r, err := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
				wantOK := name != "失败" && name != "没有可压的"
				if err != nil || r.OK != wantOK {
					t.Fatalf("手动压缩：%+v %v", r, err)
				}
			}
			h.hub.Idle()
			mu.Lock()
			got := append([]string(nil), events...)
			mu.Unlock()
			prefix := h.c.CharacterID + "|" + consoleConv(h.c.CharacterID) + "|"
			if want := []string{prefix + "true", prefix + "false"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("开始、结束各一次（角色、会话匹配）：%v，应 %v", got, want)
			}
			if name == "回调panic" && !h.logged("压缩回调出错") {
				t.Fatal("回调 panic 应记日志")
			}
		})
	}
}

// 压缩期间能停下，插话压完才交给失败那一步；两条路结束通知都不能丢。
func TestOverflowCompactInterrupted(t *testing.T) {
	t.Parallel()
	for _, stop := range []bool{true, false} {
		t.Run(fmt.Sprintf("停止=%v", stop), func(t *testing.T) {
			t.Parallel()
			entered, release := make(chan struct{}, 1), make(chan struct{})
			var mu sync.Mutex
			var phases []bool
			h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `,` + textReply + `,` + textReply + `]`,
				mutate: func(c *Config) {
					smallKeep(c)
					c.OnCompacting = func(_, _ string, starting bool) {
						mu.Lock()
						phases = append(phases, starting)
						mu.Unlock()
					}
				}, intercept: func(n int, body string, w http.ResponseWriter, r *http.Request) bool {
					if n == 1 {
						http.Error(w, overflowText, 400)
						return true
					}
					return holdCompact(entered, release)(n, body, w, r)
				}})
			h.seedRounds(8)
			out := h.hub.Send(h.c.CharacterID, "发生溢出的那句")
			recv(t, entered, "溢出后的摘要请求")
			if stop {
				if !h.stopWithin() {
					t.Fatal("压缩期间应该能停止")
				}
			} else {
				if _, ok := h.hub.Steer(h.c.CharacterID, "压缩期间插话"); !ok {
					t.Fatal("压缩期间应该收下插话")
				}
			}
			close(release)
			o := recv(t, out, "压缩后停止或回复")
			if stop {
				if o.Result == nil || !o.Result.Stopped || h.postCount() != 2 {
					t.Fatalf("停止以后不能再发回复请求：%+v，请求 %d 次", o, h.postCount())
				}
			} else {
				if o.Err != nil || o.Result.Reply != "好的" || h.postCount() != 3 || strings.Count(h.body(1), "压缩期间插话") != 1 {
					t.Fatalf("插话随重发的步骤交一次：%+v，请求 %d 次", o, h.postCount())
				}
			}
			mu.Lock()
			got := append([]bool(nil), phases...)
			mu.Unlock()
			if !reflect.DeepEqual(got, []bool{true, false}) {
				t.Fatalf("被取消的压缩也应发结束通知：%v", got)
			}
		})
	}
}

func TestCompactingWaitCallback(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var mu sync.Mutex
	var phases []bool
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + summaryReply + `,` + textReply + `]`,
		mutate: func(c *Config) {
			c.OnCompacting = func(_, _ string, starting bool) {
				mu.Lock()
				phases = append(phases, starting)
				mu.Unlock()
			}
		}, intercept: holdCompact(entered, release)})
	size := h.nearEarly()
	waiting := h.onWaitCompact()
	h.send("先提前压")
	recv(t, entered, "后台摘要请求")
	h.setHard(size + 300)
	out := h.hub.Send(h.c.CharacterID, "等后台那次")
	recv(t, waiting, "回复前等待后台压缩")
	mu.Lock()
	before := append([]bool(nil), phases...)
	mu.Unlock()
	close(release)
	if !reflect.DeepEqual(before, []bool{true, true}) {
		t.Fatalf("后台压缩、回复前等待都通知开始：%v", before)
	}
	if o := recv(t, out, "等完以后回复"); o.Err != nil {
		t.Fatal(o.Err)
	}
	h.hub.Idle()
	mu.Lock()
	got := append([]bool(nil), phases...)
	mu.Unlock()
	if !reflect.DeepEqual(got, []bool{true, true, false, false}) || h.countBodies(compactMarker) != 1 {
		t.Fatalf("两个开始各有结束，摘要只请求一次：%v", got)
	}
}

// 成功 SSE 的用量也能说明服务商静默溢出；边界、缓存口径和恢复限制都走真实网关入口。
func TestOverflowSilentResults(t *testing.T) {
	t.Parallel()
	for _, stop := range []string{"stop", "length"} {
		for _, name := range []string{"恢复", "第二次还超", "已经出字", "关闭自动压缩", "QQ私聊", "QQ群", "边界内", "有输出", "只有缓存写"} {
			t.Run(stop+"/"+name, func(t *testing.T) {
				t.Parallel()
				const window = 20000
				prompt, cached, written, output := window+1, 15000, 0, 0
				if stop == "length" {
					prompt = window * 99 / 100
				}
				if name == "边界内" {
					if stop == "stop" {
						prompt = window
					} else {
						prompt--
					}
				}
				if name == "有输出" {
					output = 1
				}
				if name == "只有缓存写" {
					cached, written = 0, 15000
				}
				h := newHarnessOpts(t, harnessOpts{replies: `[` + summaryReply + `,` + textReply + `]`, mutate: func(c *Config) {
					smallKeep(c)
					c.Context.Window, c.Context.Reserve, c.Context.HardRatio = window, 1, 1
					// 正常边界用例只看是否误报；关掉回合后的提前压，避免把它计作 overflow 的恢复。
					c.Context.NoAutoCompact = name == "关闭自动压缩" || name == "边界内" || name == "只有缓存写" || stop == "length" && name == "有输出"
				}, intercept: func(n int, body string, w http.ResponseWriter, _ *http.Request) bool {
					if strings.Contains(body, compactMarker) || n > 1 && name != "第二次还超" {
						return false
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if name == "已经出字" {
						sseChunk(w, "前半句")
					}
					fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"prompt_tokens_details\":{\"cached_tokens\":%d,\"cache_write_tokens\":%d}}}\n\ndata: [DONE]\n\n", stop, prompt, output, cached, written)
					return true
				}})
				h.seedRounds(8)
				in := consoleInput(h.c.CharacterID, h.storeOwner("现在这句"), "现在这句")
				if strings.HasPrefix(name, "QQ") {
					in.Audience.Channel = "qq"
					if name == "QQ群" {
						in.Audience.Scene = "group"
					}
				}
				o := recv(t, h.hub.Respond(in), "静默溢出处理")
				wantPosts, wantEpochs := 1, 0
				wantErr := name != "恢复" && name != "边界内" && name != "有输出" && name != "只有缓存写"
				if name == "恢复" || name == "第二次还超" || stop == "stop" && name == "有输出" {
					wantPosts, wantEpochs = 3, 1
				}
				if (o.Err != nil) != wantErr || h.postCount() != wantPosts || len(h.epochs()) != wantEpochs {
					t.Fatalf("静默溢出只恢复一次、邻居不变：错误 %v（应 %v），请求 %d/%d，快照 %+v（应 %d）", o.Err, wantErr, h.postCount(), wantPosts, h.epochs(), wantEpochs)
				}
				if name == "恢复" || stop == "stop" && name == "有输出" {
					if o.Result == nil || o.Result.Reply != "好的" || h.epochs()[0].status != "ok" || h.epochs()[0].trigger != "overflow" || !strings.Contains(h.body(1), "前面的聊天已经整理成下面的摘要") {
						t.Fatalf("静默溢出换上成功快照再回复：%+v，快照 %+v", o, h.epochs())
					}
					if rows := h.runRows(); len(rows) != 3 || rows[0].stage != "reply-retry" || !strings.Contains(rows[0].err, "上下文超了，压缩后重来") {
						t.Fatalf("静默溢出按 A7 留错误记录：%+v", rows)
					}
				}
				if name == "已经出字" && (o.Result == nil || o.Result.Reply != "前半句") {
					t.Fatalf("静默溢出已出的字保留：%+v", o)
				}
			})
		}
	}
}
