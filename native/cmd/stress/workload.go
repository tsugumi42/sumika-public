package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	randv2 "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

const modelID = "stress/plain"

type session struct {
	id, character, secret, file string
	mu                          sync.Mutex
	hold                        *gate
}
type gate struct{ arrived, release chan struct{} }
type kernel struct {
	hub      *turn.Hub
	set      *store.Set
	server   *httptest.Server
	cancel   context.CancelFunc
	mu       sync.Mutex
	records  []requestRecord
	sessions []*session
	tools    int
	gate     *approval.Gate
}

func newKernel(dir string, n, characters, tools int) (_ *kernel, err error) {
	k := &kernel{tools: tools}
	defer func() {
		if err != nil {
			k.close()
		}
	}()
	p, err := paths.Init(filepath.Join(dir, "fixture"))
	if err != nil {
		return nil, err
	}
	root, err := config.FindRoot(".")
	if err != nil {
		return nil, err
	}
	factory := filepath.Join(dir, "factory")
	if err = os.MkdirAll(filepath.Join(factory, "prompts"), 0755); err != nil {
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
		var b []byte
		b, err = os.ReadFile(filepath.Join(root, "prompts", e.Name()))
		if err != nil {
			return nil, err
		}
		if err = os.WriteFile(filepath.Join(factory, "prompts", e.Name()), b, 0644); err != nil {
			return nil, err
		}
	}
	modelsByCharacter := map[string]string{}
	for i := range characters {
		id := fmt.Sprintf("stress-character-%d", i)
		modelsByCharacter[id] = modelID
		card := p.CardDir(id)
		if err = os.MkdirAll(card, 0755); err != nil {
			return nil, err
		}
		for name, text := range map[string]string{"character.yml": "name: " + id + "\n", "character.md": "## 身份\n编的压力测试角色。\n\n## 行为\n简短回答。\n\n## 表达\n短句。\n\n## 私下\n喜欢读书。\n"} {
			if err = os.WriteFile(filepath.Join(card, name), []byte(text), 0644); err != nil {
				return nil, err
			}
		}
	}
	work := filepath.Join(dir, "work")
	if err = os.MkdirAll(work, 0755); err != nil {
		return nil, err
	}
	for i := range n {
		var token [16]byte
		if _, err = rand.Read(token[:]); err != nil {
			return nil, err
		}
		s := &session{id: fmt.Sprintf("console:stress-%d", i), character: fmt.Sprintf("stress-character-%d", i%characters), secret: "stress-secret-" + hex.EncodeToString(token[:])}
		own := filepath.Join(work, fmt.Sprintf("session-%d", i))
		if err = os.MkdirAll(own, 0755); err != nil {
			return nil, err
		}
		s.file = filepath.Join(own, "book.txt")
		if err = os.WriteFile(s.file, []byte(s.secret+"\n编的读书清单。\n"), 0644); err != nil {
			return nil, err
		}
		k.sessions = append(k.sessions, s)
	}
	k.set, err = store.OpenGo(p, "local")
	if err != nil {
		return nil, err
	}
	k.server = httptest.NewServer(http.HandlerFunc(k.serve))
	client := llm.New(llm.Magpie(k.server.URL + "/v1"))
	ctx, cancel := context.WithCancel(context.Background())
	k.cancel = cancel
	models, err := client.Models(ctx)
	if err != nil {
		return nil, err
	}
	maxLive := n / 4
	if maxLive < 1 {
		maxLive = 1
	}
	k.gate = approval.NewGate(p.Root)
	cfg := turn.Config{Root: factory, Data: p, Models: modelsByCharacter, Workdir: work, Thinking: "off", RecentMessages: 1000, Now: time.Now, Gate: k.gate, MaxLive: maxLive, IdleAfter: time.Hour, Context: turn.ContextLimits{KeepTokens: 50}, CompactTimeout: 30 * time.Second}
	k.hub = turn.NewHub(ctx, cfg, k.set, client, models)
	return k, nil
}
func (k *kernel) close() {
	if k.cancel != nil {
		k.cancel()
	}
	if k.hub != nil {
		k.hub.Idle()
	}
	// 审批门的常驻解析器由本次装配创建，和 Hub 一起回收，不能等十分钟闲置超时。
	if k.gate != nil {
		k.gate.Analyzer.Close()
	}
	if k.server != nil {
		k.server.CloseClientConnections()
		k.server.Close()
	}
	if k.set != nil {
		k.set.Close()
	}
}
func (k *kernel) serve(w http.ResponseWriter, r *http.Request) {
	reply := llmtest.Text("读完了，接着讨论。")
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
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
		// 路由标记不是暗号：先按每次输入的公开会话标记归属，再独立扫描所有暗号。
		var s *session
		for _, m := range req.Messages {
			if m.Role != "user" {
				continue
			}
			for _, candidate := range k.sessions {
				if strings.Contains(string(m.Content), "[session="+candidate.id+"]") {
					s = candidate
				}
			}
		}
		if s == nil {
			http.Error(w, "请求没有会话标记", 400)
			return
		}
		k.mu.Lock()
		k.records = append(k.records, requestRecord{s.id, string(data)})
		k.mu.Unlock()
		last := req.Messages[len(req.Messages)-1]
		var text string
		_ = json.Unmarshal(last.Content, &text)
		if strings.Contains(text, "整理上下文的请求") {
			reply = llmtest.Text("[session=" + s.id + "] 主人在讨论读书安排，暗号是 " + s.secret + "。")
		} else if last.Role == "user" && strings.Contains(text, "[round=") && k.tools > 0 {
			reply = llmtest.Reply{}
			for i := range k.tools {
				name := []string{"ls", "read", "grep"}[i%3]
				path := s.file
				if name == "ls" {
					path = filepath.Dir(path)
				}
				args, _ := json.Marshal(map[string]string{"path": path, "pattern": "stress-secret-"})
				reply.ToolCalls = append(reply.ToolCalls, llmtest.ToolCall{ID: fmt.Sprintf("stress-%s-%d", hex.EncodeToString([]byte(s.id)), i) + fmt.Sprintf("-%x", randv2.Uint64()), Name: name, Args: []string{string(args)}})
			}
		}
		s.mu.Lock()
		b := s.hold
		s.hold = nil
		s.mu.Unlock()
		if b != nil {
			close(b.arrived)
			select {
			case <-b.release:
			case <-r.Context().Done():
				return
			}
		}
	}
	var bodies []string
	var mu sync.Mutex
	llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + modelID + `","context_length":1000000,"max_output_tokens":4000}`)}, Reply: reply}, &bodies, &mu)(w, r)
}
func (k *kernel) append(s *session, text string) (int64, error) {
	st, err := k.set.ForConversation(s.id)
	if err != nil {
		return 0, err
	}
	return st.AppendEvent(store.NewEvent{Channel: "console", Conversation: s.id, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: text, CreatedAt: time.Now().UnixMilli()}, []string{s.character})
}
