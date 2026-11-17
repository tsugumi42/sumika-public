package memplugin

// 经过真的内核：假网关，真的 Hub、store、角色卡。看的是发给模型的请求里到底有什么，
// 也就是 State 是不是真的随实例走、档是不是真的分实例、插话那一次是不是真的只追加

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/memory"
	"sumika-native/internal/paths"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// 角色 id 和卡都是编的；卡的内容照内核测试的 live-second 场景
const character = "suzu"

var cardFiles = map[string]string{
	"characters/suzu/card/character.yml": "name: 铃\n",
	"characters/suzu/card/character.md":  "<!-- 测试用的卡，名字是编的 -->\n## 身份\n铃，住在主人电脑里的女仆。\n\n## 行为\n干活利落，话说到点上。\n\n## 表达\n短句，像发消息。\n\n## 私下\n喜欢被夸。\n",
	"config/prompts/scene-home.md":       "现在的场合：本地主窗口里的私聊（测试场景）。",
}

type kernel struct {
	t      *testing.T
	hub    *turn.Hub
	cancel context.CancelFunc
	st     *store.Store
	logs   *logSink
	mu     sync.Mutex
	bodies []string
	calls  []testToolCall
}

// message 是发给模型的一条消息：role、正文（字符串或分段的都拼成一个字符串）、原样的 JSON
type message struct{ Role, Text, Raw string }

func newKernel(t *testing.T) *kernel { return newKernelWith(t, nil) }

// analyzer 是这个包的测试共用的命令解析器；没有测试跑命令，它不会真的起 pwsh
var analyzer = policy.NewAnalyzer()

// newKernelWith：mutate 改内核的配置（压缩测试要把保留的原文调小）
func newKernelWith(t *testing.T, mutate func(*turn.Config)) *kernel {
	t.Helper()
	k := &kernel{t: t, logs: &logSink{}}
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for rel, text := range cardFiles {
		f := filepath.Join(p.Root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(f), 0o755)
		if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if k.st, err = store.OpenFresh(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.st.Close() })
	srv := httptest.NewServer(http.HandlerFunc(k.gateway))
	t.Cleanup(srv.Close)
	client := llm.New(llm.Magpie(srv.URL + "/v1"))
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := config.FindRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir() // 出厂的提示词：从项目里拷 prompts/*.md
	os.MkdirAll(filepath.Join(root, "prompts"), 0o755)
	entries, _ := os.ReadDir(filepath.Join(repo, "prompts"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			b, _ := os.ReadFile(filepath.Join(repo, "prompts", e.Name()))
			os.WriteFile(filepath.Join(root, "prompts", e.Name()), b, 0o644)
		}
	}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	cfg := turn.Config{Root: root, Data: p, Thinking: "low", RecentMessages: 30, Workdir: t.TempDir(), RefScope: memory.ScopeKey,
		Models: map[string]string{character: "test/plain"}, Now: func() time.Time { return now },
		Hooks: turn.HookTimeouts{Context: 5 * time.Second, Notify: 5 * time.Second}, Logf: k.logs.logf}
	cfg.Gate = approval.NewGateWith(p.Root, analyzer) // 这些测试不跑命令：共用一个没预热的解析器，不每建一次就起一个 pwsh
	if mutate != nil {
		mutate(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	k.cancel = cancel
	t.Cleanup(cancel)
	k.hub = turn.NewHub(ctx, cfg, store.Single(k.st), client, models)
	return k
}

// gateway 是假网关：记下每次请求的原样，回一句固定的话
func (k *kernel) gateway(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "test/plain", "context_length": 100000, "max_output_tokens": 4000}}})
		return
	}
	b, _ := io.ReadAll(r.Body)
	k.mu.Lock()
	k.bodies = append(k.bodies, string(b))
	var call testToolCall
	if len(k.calls) > 0 {
		call, k.calls = k.calls[0], k.calls[1:]
	}
	k.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(o map[string]any) {
		o["id"], o["object"], o["created"], o["model"] = "g", "chat.completion.chunk", 0, "x"
		j, _ := json.Marshal(o)
		fmt.Fprintf(w, "data: %s\n\n", j)
	}
	if call.name != "" {
		chunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call1", "type": "function", "function": map[string]any{"name": call.name, "arguments": call.args}}}}, "finish_reason": nil}}})
		chunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	chunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "在呢～"}, "finish_reason": nil}}})
	chunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
	chunk(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 5, "total_tokens": 105}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// request 是第 i 次发给模型的请求里的消息
func (k *kernel) request(i int) []message {
	k.t.Helper()
	k.mu.Lock()
	defer k.mu.Unlock()
	if i >= len(k.bodies) {
		k.t.Fatalf("只发了 %d 次请求，要看第 %d 次", len(k.bodies), i)
	}
	var r struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(k.bodies[i]), &r); err != nil {
		k.t.Fatal(err)
	}
	var out []message
	for _, raw := range r.Messages {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			k.t.Fatal(err)
		}
		text := ""
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			text = s
		} else {
			var parts []struct {
				Text string `json:"text"`
			}
			json.Unmarshal(m.Content, &parts)
			for _, p := range parts {
				text += p.Text
			}
		}
		out = append(out, message{m.Role, text, string(raw)})
	}
	return out
}

func (k *kernel) requests() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.bodies)
}

// enable 启用记忆插件，所有角色用同一份记忆
func (k *kernel) enable(m *memory.Memory) *Plugin {
	k.t.Helper()
	p := New(Options{Memories: only(m), Logf: k.logs.logf, Now: func() time.Time { return fixedNow }})
	if err := k.hub.Use(p); err != nil {
		k.t.Fatal(err)
	}
	return p
}

// send 是主人在主窗口说了一句（档用主窗口的）
func (k *kernel) send(text string) {
	k.t.Helper()
	if o := <-k.hub.Send(character, text); o.Err != nil {
		k.t.Fatal(o.Err)
	}
}

// say 是平台群里有人说了一句：存进库，再请她回；档由 scope 给（装配处该用 memory.ScopeKey 算）
func (k *kernel) say(conv string, a audience.Audience, scope, actorKind, actorID, text string) {
	k.t.Helper()
	seq, err := k.st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: actorKind, ActorID: actorID, Origin: actorKind, Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{character})
	if err != nil {
		k.t.Fatal(err)
	}
	if o := <-k.hub.Respond(turn.Input{CharacterID: character, Conversation: conv, Seqs: []int64{seq}, Text: text, Audience: a, Addressed: true, Scope: scope}); o.Err != nil {
		k.t.Fatal(o.Err)
	}
}

// inMessages 数 sub 在几条消息里出现；asserted 之外还返回出现在哪几条的位置
func inMessages(ms []message, sub string) (n int, at []int) {
	for i, m := range ms {
		if strings.Contains(m.Text, sub) {
			n++
			at = append(at, i)
		}
	}
	return n, at
}

// isPrefix：a 的每条原样都是 b 的开头（只追加，缓存才接得上）
func isPrefix(a, b []message) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		if a[i].Raw != b[i].Raw {
			return false
		}
	}
	return true
}

// 主窗口里连着问：第一轮把固定要点和命中追加在这句话前面；原样再问什么都不加；问到新的只追加新的。
// 每一次请求都是上一次的前缀加新内容（缓存规则 ADR-022）
func TestKernelAppendsMemoryOnce(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	k.enable(openMemory(t, fixedNow, standardFacts(), &fakeEmbed{}))

	k.send("吉他练得怎么样了")
	r0 := k.request(0)
	n, at := inMessages(r0, textGuitar)
	if n != 1 || r0[at[0]].Role != "user" || !strings.Contains(r0[at[0]].Text, turn.ContextHeader) || !strings.Contains(r0[at[0]].Text, "要一直遵守的约定和边界：") {
		t.Fatalf("第一轮：命中和固定要点该在同一条参考信息里（%d 条含命中）：\n%s", n, r0[at[0]].Text)
	}
	if !strings.Contains(r0[at[0]].Text, memory.DataNotice) {
		t.Fatalf("注入的命中那一段该带 DataNotice：\n%s", r0[at[0]].Text)
	}
	if _, said := inMessages(r0, "吉他练得怎么样了"); said[len(said)-1] != len(r0)-1 || at[0] >= len(r0)-1 {
		t.Fatalf("参考信息该在这句话前面，这句话是最后一条：参考 %v，说话 %v，共 %d 条", at, said, len(r0))
	}

	k.send("吉他练得怎么样了")
	r1 := k.request(1)
	if !isPrefix(r0, r1) {
		t.Fatal("第二次请求该是第一次的延续（只追加）")
	}
	if n, _ := inMessages(r1, textGuitar); n != 1 {
		t.Fatalf("原样再问：命中不该再追加，历史里一共该只有 1 条含它，得到 %d", n)
	}

	k.send("搬家的事怎么样了")
	r2 := k.request(2)
	if !isPrefix(r1, r2) {
		t.Fatal("第三次请求该是第二次的延续")
	}
	for sub, want := range map[string]int{textGuitar: 1, "听说：" + textMove: 1, pinnedPublic: 1, pinnedOwner: 1} {
		if n, _ := inMessages(r2, sub); n != want {
			t.Errorf("第三次请求里「%s」该在 %d 条消息里，得到 %d", sub, want, n)
		}
	}
}

// 外部审计 F02 的记忆侧，走真的内核：同一个群，主人问 → 群友问 → 主人再问。
// 档不同仍分实例；两次请求都能想起同角色私事，主人再问回到首个实例且不重复注入。
func TestKernelOwnerThenFriendThenOwner(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	k.enable(openMemory(t, fixedNow, standardFacts(), nil))
	ownerScope, friendScope := memory.ScopeKey(ownerInA), memory.ScopeKey(friendInA)
	if ownerScope == friendScope {
		t.Fatal("主人问和群友问的档该不同")
	}
	// 群友这句带一个独一无二的记号，后面数它在主人的实例里出现几次（正文里不带号码，历史读出来的话本来就没有）
	const friendAsk = "群友这一句：吉他练得怎么样了"

	k.say(groupA, ownerInA, ownerScope, "owner", "owner", "吉他练得怎么样了")
	if n, _ := inMessages(k.request(0), textGuitar); n != 1 {
		t.Fatalf("主人问：该想起自己的事：%d", n)
	}

	k.say(groupA, friendInA, friendScope, "human", "qq:60001", friendAsk)
	r1 := k.request(1)
	for _, remembered := range []string{textGuitar, pinnedOwner} {
		if n, _ := inMessages(r1, remembered); n != 1 {
			t.Fatalf("请求者变化后同角色记忆 %q 出现 %d 次，应为一次", remembered, n)
		}
	}
	if n, _ := inMessages(r1, pinnedPublic); n != 1 {
		t.Fatalf("群友问：公开的约定照给，得到 %d", n)
	}

	k.say(groupA, ownerInA, ownerScope, "owner", "owner", "吉他练得怎么样了")
	r2 := k.request(2)
	if !isPrefix(k.request(0), r2) {
		t.Fatal("主人再问：该回到第一个实例，请求是第一次的延续")
	}
	// 主人的实例里该有的：主人自己的事、两样约定，各一条，没有因为群友问了一轮就重给
	for sub, want := range map[string]int{textGuitar: 1, pinnedOwner: 1, pinnedPublic: 1} {
		if n, _ := inMessages(r2, sub); n != want {
			t.Errorf("主人再问：「%s」该在 %d 条消息里，得到 %d", sub, want, n)
		}
	}
	// 群友那句话本身内核会追上来（别的档说过的话按先后追加在末尾），而且只追一次。
	// 内核靠它让主人这个实例知道群里发生了什么，记忆这边不能把它挡掉
	if n, _ := inMessages(r2, friendAsk); n != 1 {
		t.Errorf("主人再问：群友那句话该被追上一次，得到 %d", n)
	}
	// 群友那一轮给她的参考信息（里面又有一份公开约定）是另一个实例的东西，追不过来：
	// 主人的实例里只该有主人自己那一轮的参考信息，不是两份
	if n, _ := inMessages(r2, turn.ContextHeader); n != 1 {
		t.Errorf("主人再问：参考信息该只有主人第一轮的那一份，得到 %d 条", n)
	}
}

// 装配处算错档：整个实例不给记忆，日志里留一行
func TestKernelScopeMismatch(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	k.enable(openMemory(t, fixedNow, standardFacts(), nil))
	k.say(groupA, ownerInA, "随手填的档", "owner", "owner", "吉他练得怎么样了")
	r := k.request(0)
	for _, sub := range []string{textGuitar, pinnedPublic, pinnedOwner} {
		if n, _ := inMessages(r, sub); n != 0 {
			t.Fatalf("档对不上不该给任何东西，「%s」出现了", sub)
		}
	}
	if !strings.Contains(k.logs.String(), "对不上") {
		t.Fatalf("该留一行日志：\n%s", k.logs.String())
	}
}

// 工具清单变化会重建实例：停掉后不追加，再启用后按新实例重新召回。
func TestKernelStopAndReEnable(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	p := k.enable(openMemory(t, fixedNow, standardFacts(), nil))
	k.send("吉他练得怎么样了")
	k.hub.StopPlugin(ID)

	k.send("搬家的事怎么样了")
	if n, _ := inMessages(k.request(1), textMove); n != 0 {
		t.Fatalf("停掉以后不该再追加：%d", n)
	}
	if err := k.hub.Use(p); err != nil {
		t.Fatal(err)
	}
	k.send("搬家的事怎么样了")
	r2 := k.request(2)
	for sub, want := range map[string]int{textMove: 1, textGuitar: 0, pinnedPublic: 1} {
		if n, _ := inMessages(r2, sub); n != want {
			t.Errorf("再启用以后「%s」该在 %d 条消息里，得到 %d", sub, want, n)
		}
	}
}

// steerer 在回合开头（记忆之后）替主人插一句，让内核走一遍"插话送到"
type steerer struct {
	hub  *turn.Hub
	text string
	once sync.Once
}

func (s *steerer) ID() string { return "steerer" }
func (s *steerer) Setup(r *turn.Registry) (func(), error) {
	r.OnContext(func(_ context.Context, in turn.ContextIn) (*turn.Contribution, error) {
		if !in.Steer {
			s.once.Do(func() { s.hub.Steer(character, s.text) })
		}
		return nil, nil
	})
	return nil, nil
}

// 插话送到时记忆再来一次：只追加没给过的，紧挨在插话前面；固定要点、给过的命中不重复
func TestKernelSteerAppendsOnlyNew(t *testing.T) {
	t.Parallel()
	k := newKernel(t)
	k.enable(openMemory(t, fixedNow, standardFacts(), nil))
	if err := k.hub.Use(&steerer{hub: k.hub, text: "对了还有搬家的事"}); err != nil {
		t.Fatal(err)
	}
	k.send("吉他练得怎么样了")
	if k.requests() != 1 {
		t.Fatalf("插话在第一步送到，只该请求一次：%d", k.requests())
	}
	r := k.request(0)
	_, steerAt := inMessages(r, "对了还有搬家的事")
	n, moveAt := inMessages(r, textMove)
	if n != 1 || len(steerAt) == 0 {
		t.Fatalf("插话和它召回的记忆都该在：%d %v", n, steerAt)
	}
	if moveAt[0]+1 != steerAt[len(steerAt)-1] {
		t.Fatalf("插话那一次召回的该紧挨在插话前面（记忆 %v，插话 %v）", moveAt, steerAt)
	}
	extra := r[moveAt[0]].Text
	if strings.Contains(extra, "要一直遵守") || strings.Contains(extra, textGuitar) {
		t.Fatalf("插话那一次只该追加新的：\n%s", extra)
	}
	if n, _ := inMessages(r, textGuitar); n != 1 {
		t.Fatalf("第一次的命中只该在历史里一次：%d", n)
	}
}
