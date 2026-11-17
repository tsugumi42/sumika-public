// Package turntest 是别的包对着真内核写测试用的夹具（渠道中间件的端到端、记忆插件）：真的 Hub、store、角色卡、出厂提示词，
// 假网关（llmtest），数据都在临时目录里，不碰 data/。内核自己的包内测试用不了它（会循环引用），用的是同一个假网关。
// 只给搭好的内核和看请求的几样；渠道回合怎么存库、怎么交回（Commit），由调用方自己做，夹具不替它做
package turntest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/memory"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// CharacterID 是夹具里唯一的角色（名字是编的）
const CharacterID = "suzu"

// Model 是这个角色用的模型（假网关的清单里只有它）
const Model = "test/plain"

// card 是编的角色卡和本地主窗口的场合提示
var card = map[string]string{
	"characters/suzu/card/character.yml": "name: 铃\nownerName: 主人\n",
	"characters/suzu/card/character.md":  "<!-- 测试用的卡，名字是编的 -->\n## 身份\n铃，住在主人电脑里的女仆。\n\n## 行为\n干活利落，话说到点上。\n\n## 表达\n短句，像发消息。\n\n## 私下\n喜欢被夸。\n",
	"config/prompts/scene-home.md":       "现在的场合：本地主窗口里的私聊（测试场景）。",
}

// Options 是夹具的设置，零值就能用：每次请求回一句"在呢～"
type Options struct {
	Reply   llmtest.Reply   // 没写在 Replies 里的那几次回它；零值是一句"在呢～"
	Replies []llmtest.Reply // 第 n 次请求回 Replies[n-1]
	// Hold 说第 nth 次请求停在第几段正文上：吐完前几段就停住，等内核中止连接（停下、插话打断时 Said 里有半句）；负数是不停
	Hold func(nth int) int
	// Before 在假网关回第 n 次请求之前调，可以在里面卡住；ctx 是这次请求的，内核中止时取消。n 是这时已经回过的请求数 + 1：
	// 同时只有一个请求在路上时和 Replies 的编号一样；几个会话同时请求时会重号，按 body 认是哪一个。
	// 卡住的一定要看 ctx：收尾时内核先取消，再等假网关上的请求都结束
	Before func(ctx context.Context, n int, body string)
	Models []json.RawMessage // 假网关的模型清单；空的是一个大上下文的 Model
	// Mutate 改内核的设置（钟、审批的关、保留多少原文……）；默认没有审批的关，调内置工具的要自己给 Gate
	Mutate func(*turn.Config)
}

// Kernel 是搭好的内核
type Kernel struct {
	Hub *turn.Hub
	// Set 是一组按平台分开的库（本地、QQ 各一个，共用一个 trace；规格-聊天记录按平台分库）：本地主窗口的存进本地库，qq:… 的会话存进 QQ 库。
	// 要查某个会话的库用 StoreFor
	Set  *store.Set
	Data paths.Paths

	t      testing.TB
	mu     sync.Mutex
	bodies []string
	logs   []string
}

// New 搭一个内核；先封入口并等真实退出，再关假网关和临时库。
func New(t testing.TB, o Options) *Kernel {
	t.Helper()
	k := &Kernel{t: t}
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k.Data = p
	for rel, text := range card {
		f := filepath.Join(p.Root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(f), 0o755)
		if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if k.Set, err = store.OpenGo(p, "local", "qq"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Set.Close() })

	if reflect.DeepEqual(o.Reply, llmtest.Reply{}) {
		o.Reply = llmtest.Text("在呢～")
	}
	if o.Models == nil {
		o.Models = []json.RawMessage{json.RawMessage(`{"id":"` + Model + `","context_length":100000,"max_output_tokens":4000}`)}
	}
	handler := llmtest.Handler(llmtest.Script{Models: o.Models, Reply: o.Reply, Replies: o.Replies, Hold: o.Hold}, &k.bodies, &k.mu)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o.Before != nil && r.Method == http.MethodPost {
			b, err := readBody(r)
			if err != nil {
				return
			}
			k.mu.Lock()
			n := len(k.bodies) + 1
			k.mu.Unlock()
			o.Before(r.Context(), n, b)
			if r.Context().Err() != nil {
				return // 内核那边已经中止了，不再回、不记
			}
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	client := llm.New(llm.Magpie(srv.URL + "/v1"))
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root, err := factoryRoot(t)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	cfg := turn.Config{Root: root, Data: p, Thinking: "low", RecentMessages: 30, Workdir: t.TempDir(),
		Models: map[string]string{CharacterID: Model}, Now: func() time.Time { return now }, RefScope: memory.ScopeKey,
		Hooks: turn.HookTimeouts{Context: 5 * time.Second, Notify: 5 * time.Second},
		// 假网关出错时重来的等待调到毫秒，次数照默认
		Retry: turn.RetryPolicy{Base: time.Millisecond, Cap: 2 * time.Millisecond},
		Logf: func(f string, a ...any) {
			k.mu.Lock()
			k.logs = append(k.logs, fmt.Sprintf(f, a...))
			k.mu.Unlock()
		}}
	if o.Mutate != nil {
		o.Mutate(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	k.Hub = turn.NewHub(ctx, cfg, k.Set, client, models)
	t.Cleanup(func() {
		cancel()
		k.Hub.Seal()
		drainCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := k.Hub.Drain(drainCtx); err != nil {
			t.Errorf("内核未排空：%v", err)
		}
		k.Hub.Idle()
	})
	return k
}

// factoryRoot 把项目里的出厂提示词（prompts/*.md）拷到临时的"项目根"
func factoryRoot(t testing.TB) (string, error) {
	repo, err := config.FindRoot(".")
	if err != nil {
		return "", err
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(filepath.Join(repo, "prompts"))
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repo, "prompts", e.Name()))
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "prompts", e.Name()), b, 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// Bodies 是假网关收到过的请求体，原样、按先后（拷一份）
func (k *Kernel) Bodies() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.bodies...)
}

// Requests 是假网关回过几次请求（在 Before 里被中止的不算）
func (k *Kernel) Requests() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.bodies)
}

// Message 是发给模型的一条消息：role、正文（分段的拼成一个字符串）、原样的 JSON（比前缀用）
type Message struct{ Role, Text, Raw string }

// Messages 是第 i 次请求（从 0 数）里的消息
func (k *Kernel) Messages(i int) []Message {
	k.t.Helper()
	bodies := k.Bodies()
	if i >= len(bodies) {
		k.t.Fatalf("只发了 %d 次请求，要看第 %d 次", len(bodies), i+1)
	}
	var r struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(bodies[i]), &r); err != nil {
		k.t.Fatal(err)
	}
	out := make([]Message, 0, len(r.Messages))
	for _, raw := range r.Messages {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			k.t.Fatal(err)
		}
		var text string
		if json.Unmarshal(m.Content, &text) != nil {
			var parts []struct {
				Text string `json:"text"`
			}
			json.Unmarshal(m.Content, &parts)
			for _, p := range parts {
				text += p.Text
			}
		}
		out = append(out, Message{m.Role, text, string(raw)})
	}
	return out
}

// Logs 是内核记过的日志，一行一条
func (k *Kernel) Logs() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return strings.Join(k.logs, "\n")
}

// Wait 等一轮的结果，最多 30 秒：卡住时红在这里，不等 go test 的总超时；出错就红
func (k *Kernel) Wait(o <-chan turn.Outcome) *turn.Result {
	k.t.Helper()
	select {
	case r := <-o:
		if r.Err != nil {
			k.t.Fatal(r.Err)
		}
		return r.Result
	case <-time.After(30 * time.Second):
		k.t.Fatal("30 秒还没说完")
		return nil
	}
}

// Send 是主人在本地主窗口说了一句，等她说完
func (k *Kernel) Send(text string) *turn.Result {
	k.t.Helper()
	return k.Wait(k.Hub.Send(CharacterID, text))
}

// Say 是平台会话里有人说了一句：存进库（她看得到），再请她回，等她说完。她说的话不存库、不交回，要的话接着调 Commit
func (k *Kernel) Say(conv string, a audience.Audience, scope, actorKind, actorID, text string) *turn.Result {
	k.t.Helper()
	seq := k.Append(conv, actorKind, actorID, text)
	return k.Wait(k.Hub.Respond(turn.Input{CharacterID: CharacterID, Conversation: conv, Seqs: []int64{seq}, Text: text, Audience: a, Addressed: true, Scope: scope}))
}

// StoreFor 是这个会话所在平台的库（本地主窗口的是本地库，qq:… 的是 QQ 库）
func (k *Kernel) StoreFor(conv string) *store.Store {
	k.t.Helper()
	st, err := k.Set.ForConversation(conv)
	if err != nil {
		k.t.Fatal(err)
	}
	return st
}

// Append 往会话所在平台的库里存一条消息（她看得到），返回 seq。渠道从会话 id 的开头取（qq:group:… 是 qq）；origin 照说话的人：
// owner 是 owner，character 是 character，别的是 peer
func (k *Kernel) Append(conv, actorKind, actorID, text string) int64 {
	k.t.Helper()
	channel, _, _ := strings.Cut(conv, ":")
	origin := "peer"
	if actorKind == "owner" || actorKind == "character" {
		origin = actorKind
	}
	seq, err := k.StoreFor(conv).AppendEvent(store.NewEvent{Channel: channel, Conversation: conv, ActorKind: actorKind, ActorID: actorID, Origin: origin, Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{CharacterID})
	if err != nil {
		k.t.Fatal(err)
	}
	return seq
}

// Commit 扮演渠道中间件写库（ADR-184 第 3 条）：把这一回合她说的每一句原样存进库，再交回内核；返回交回的几行
func (k *Kernel) Commit(conv string, r *turn.Result) []turn.Committed {
	k.t.Helper()
	var lines []turn.Committed
	for _, s := range r.Said {
		lines = append(lines, turn.Committed{Step: s.Step, Seq: k.Append(conv, "character", CharacterID, s.Text), Text: s.Text})
	}
	if len(lines) > 0 {
		if err := k.Hub.Commit(CharacterID, conv, r.TurnID, lines); err != nil {
			k.t.Fatal(err)
		}
	}
	return lines
}

func readBody(r *http.Request) (string, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return string(b), nil
}
