package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

const modelID = "soak/plain"
const summaryText = "主人和两位角色轮流聊天，正在整理周末的读书安排，先读短篇小说，再一起讨论人物和结局。"

type counters struct{ turns, tools, steers, stops, compacts, fresh, toolErrors atomic.Uint64 }

func (c *counters) snapshot() totals {
	return totals{c.turns.Load(), c.tools.Load(), c.steers.Load(), c.stops.Load(), c.compacts.Load(), c.fresh.Load()}
}

type barrier struct{ arrived, release chan struct{} }
type gateway struct {
	mu   sync.Mutex
	next *barrier
}

func (g *gateway) arm() *barrier {
	b := &barrier{make(chan struct{}), make(chan struct{})}
	g.mu.Lock()
	g.next = b
	g.mu.Unlock()
	return b
}
func (g *gateway) serve(w http.ResponseWriter, r *http.Request) {
	reply := llmtest.Text("在呢，我们接着讨论这本书和周末的安排。")
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(data)))
		var req struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if err = json.Unmarshal(data, &req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var last, role string
		if n := len(req.Messages); n > 0 {
			role = req.Messages[n-1].Role
			json.Unmarshal(req.Messages[n-1].Content, &last)
		}
		if strings.Contains(last, "整理上下文的请求") {
			reply = llmtest.Text(summaryText)
		} else if role == "user" && strings.Contains(last, "soak-tools") {
			reply = llmtest.Call("soak-ls", "ls", `{"path":"."}`)
			reply.ToolCalls = append(reply.ToolCalls, llmtest.ToolCall{ID: "soak-read", Name: "read", Args: []string{`{"path":"book.txt"}`}})
		}
		g.mu.Lock()
		b := g.next
		g.next = nil
		g.mu.Unlock()
		if b != nil {
			close(b.arrived)
			select {
			case <-b.release:
			case <-r.Context().Done():
				return
			}
		}
	}
	// 复用夹具的 SSE 协议；每次请求各自的记录随调用释放，不在宿主里积攒历史请求。
	var bodies []string
	var mu sync.Mutex
	llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + modelID + `","context_length":100000,"max_output_tokens":4000}`)}, Reply: reply}, &bodies, &mu)(w, r)
}

type kernel struct {
	hub     *turn.Hub
	set     *store.Set
	server  *httptest.Server
	gateway gateway
	dbdir   string
	counts  *counters
}

func newKernel(ctx context.Context, dir string) (_ *kernel, err error) {
	k := &kernel{}
	defer func() {
		if err != nil {
			k.close()
		}
	}()
	p, err := paths.Init(filepath.Join(dir, "data"))
	if err != nil {
		return nil, err
	}
	for _, id := range []string{"suzu", "kiri"} {
		card := filepath.Join(p.CardDir(id), "character.yml")
		if err = os.MkdirAll(filepath.Dir(card), 0o755); err != nil {
			return nil, err
		}
		if err = os.WriteFile(card, []byte("name: "+id+"\n"), 0o644); err != nil {
			return nil, err
		}
		if err = os.WriteFile(filepath.Join(p.CardDir(id), "character.md"), []byte("## 身份\n编的挂机角色，喜欢读书。\n\n## 行为\n回答简短，认真做事。\n\n## 表达\n平常的短句。\n\n## 私下\n喜欢讨论故事。\n"), 0o644); err != nil {
			return nil, err
		}
	}
	root, err := config.FindRoot(".")
	if err != nil {
		return nil, err
	}
	// 出厂提示词只读，照 turntest 拷到隔离目录；不读取真实角色或配置。
	factory := filepath.Join(dir, "factory")
	if err = os.MkdirAll(filepath.Join(factory, "prompts"), 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(root, "prompts"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		text, eerr := os.ReadFile(filepath.Join(root, "prompts", e.Name()))
		if eerr != nil {
			return nil, eerr
		}
		if err = os.WriteFile(filepath.Join(factory, "prompts", e.Name()), text, 0o644); err != nil {
			return nil, err
		}
	}
	workdir := filepath.Join(dir, "work")
	if err = os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}
	if err = os.WriteFile(filepath.Join(workdir, "book.txt"), []byte("编的读书清单：短篇小说、人物、结局。\n"), 0o644); err != nil {
		return nil, err
	}
	k.set, err = store.OpenGo(p, "local")
	if err != nil {
		return nil, err
	}
	k.dbdir = filepath.Join(p.Root, "go")
	k.server = httptest.NewServer(http.HandlerFunc(k.gateway.serve))
	client := llm.New(llm.Magpie(k.server.URL + "/v1"))
	models, err := client.Models(ctx)
	if err != nil {
		return nil, err
	}
	cfg := turn.Config{Root: factory, Data: p, Models: map[string]string{"suzu": modelID, "kiri": modelID}, Workdir: workdir,
		Thinking: "off", RecentMessages: 30, Now: time.Now, Gate: approval.NewGate(p.Root),
		Context: turn.ContextLimits{KeepTokens: 50}, IdleAfter: 8 * time.Second, MaxLive: 4,
		CompactTimeout: 10 * time.Second, Retry: turn.RetryPolicy{Base: time.Millisecond, Cap: time.Millisecond}}
	cfg.ExecTool = func(ctx context.Context, cwd, name string, args jsjson.Value) (tools.Result, error) {
		if name != "ls" && name != "read" {
			return tools.Result{}, fmt.Errorf("挂机只调 ls/read，收到 %s", name)
		}
		result, err := tools.Execute(ctx, cwd, name, args)
		if k.counts != nil {
			if err != nil || result.IsError {
				k.counts.toolErrors.Add(1)
			} else {
				k.counts.tools.Add(1)
			}
		}
		return result, err
	}
	k.hub = turn.NewHub(ctx, cfg, k.set, client, models)
	return k, nil
}
func (k *kernel) close() {
	if k.hub != nil {
		k.hub.Idle()
	}
	if k.server != nil {
		k.server.Close()
	}
	if k.set != nil {
		k.set.Close()
	}
}

// 四组实例按连续十二轮轮换：组内复用，离开后闲置超过八秒；高 rate 下仍由 MaxLive 和短 IdleAfter 约束。
var targets = []struct{ character, conversation string }{{"suzu", "console:suzu"}, {"kiri", "console:kiri"}, {"suzu", "console:soak-room"}, {"kiri", "console:soak-room"}}

func (k *kernel) work(ctx context.Context, rate float64, c *counters) error {
	k.counts = c
	ticker := time.NewTicker(time.Duration(float64(time.Second) / rate))
	defer ticker.Stop()
	for n := 0; ; n++ {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if err := k.step(ctx, n, c); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}
func (k *kernel) step(ctx context.Context, n int, c *counters) error {
	target := targets[(n/12)%len(targets)]
	action := n % 12
	text := fmt.Sprintf("第 %d 句：我们来讨论短篇小说的人物和结局，周末先读书再交流。", n+1)
	if action == 2 || action == 6 {
		text += " soak-tools"
	}
	st, err := k.set.ForConversation(target.conversation)
	if err != nil {
		return err
	}
	seq, err := st.AppendEvent(store.NewEvent{Channel: "console", Conversation: target.conversation, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{target.character})
	if err != nil {
		return err
	}
	var b *barrier
	if action == 4 || action == 8 {
		b = k.gateway.arm()
	}
	out := k.hub.Respond(turn.Input{CharacterID: target.character, Conversation: target.conversation, Seqs: []int64{seq}, Text: text, Audience: audience.Console(target.conversation), Addressed: true, Scope: turn.ConsoleScope})
	// 总时限用于故障诊断，不靠调度恰好赶上一次立即返回的假回复。
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	if b != nil {
		select {
		case <-b.arrived:
			if action == 4 {
				if _, ok := k.hub.SteerIn(target.character, target.conversation, "插一句：先读短篇，再讨论结局。", nil); !ok {
					close(b.release)
					return errors.New("正在回复却没收下插话")
				}
				c.steers.Add(1)
				close(b.release)
			} else {
				if !k.hub.StopIn(target.character, target.conversation) {
					return errors.New("正在回复却没停下")
				}
				c.stops.Add(1)
			}
		case <-ctx.Done():
			// 仍要收这一轮的结果，关库前不留在路上的回合。
		case early := <-out:
			return fmt.Errorf("回合没到交错屏障就结束：%v", early.Err)
		case <-timer.C:
			dumpGoroutines()
			return fmt.Errorf("第 %d 句等假网关屏障超过 10 秒", n+1)
		}
	}
	select {
	case result := <-out:
		if ctx.Err() != nil {
			return nil
		}
		if result.Err != nil {
			return result.Err
		}
		if result.Result == nil || result.Result.Stopped != (action == 8) {
			return fmt.Errorf("第 %d 句的停止状态不对：%+v", n+1, result.Result)
		}
		c.turns.Add(1)
		var fresh int
		if err = k.set.Trace().QueryRow(`SELECT count(*) FROM model_runs WHERE turn_id = ? AND rebuild = 'fresh'`, result.TurnID).Scan(&fresh); err != nil {
			return err
		}
		c.fresh.Add(uint64(fresh))
	case <-timer.C:
		dumpGoroutines()
		return fmt.Errorf("第 %d 句超过 10 秒还没结束", n+1)
	}
	if c.toolErrors.Load() > 0 {
		return fmt.Errorf("真实只读工具出错 %d 次", c.toolErrors.Load())
	}
	if action == 11 {
		result, err := k.hub.Compact(target.character, target.conversation, "manual", 0)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		if !result.OK {
			return fmt.Errorf("手动压缩没成：%s", result.Err)
		}
		c.compacts.Add(1)
	}
	return nil
}

// dumpGoroutines 超时的那一刻把所有协程的栈打到标准错误：分得清是卡在锁上（真卡住）还是机器忙、慢了
func dumpGoroutines() {
	fmt.Fprintln(os.Stderr, "===== 超时时的协程栈 =====")
	_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
}
