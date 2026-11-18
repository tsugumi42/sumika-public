package turn

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/llm"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 复用实例时，这一轮的思考档位照新的配置发（agent.ts runTurn 每轮设 thinkingLevel），历史照旧接着用
func TestRefreshThinkingOnReuse(t *testing.T) {
	t.Parallel()
	var c testCase
	b, err := os.ReadFile(filepath.Join("testdata", "cases", "live-second.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for rel, text := range c.Files {
		f := filepath.Join(p.Root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(f), 0o755)
		os.WriteFile(f, []byte(text), 0o644)
	}
	st, err := store.OpenFresh(p)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var bodies []string
	var mu sync.Mutex
	srv := gateway(t, c, &bodies, &mu)
	defer srv.Close()
	client := llm.New(llm.Magpie(srv.URL + "/v1"))
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, c.Now)
	wd := t.TempDir()
	cfg := Config{Root: factoryRoot(t), Data: p, Thinking: "low", RecentMessages: 30, Workdir: wd, Models: map[string]string{c.CharacterID: c.Characters[c.CharacterID].Model},
		Now: func() time.Time { return now }, Gate: testGate(p.Root)}
	hub := NewHub(context.Background(), cfg, anyConv{st}, client, models)
	if o := <-hub.Send(c.CharacterID, "第一句"); o.Err != nil {
		t.Fatal(o.Err)
	}
	hub.Cfg.Thinking = "high"
	if o := <-hub.Send(c.CharacterID, "第二句"); o.Err != nil {
		t.Fatal(o.Err)
	}
	var efforts []string
	for _, body := range bodies {
		var r struct {
			Effort   string `json:"reasoning_effort"`
			Messages []any  `json:"messages"`
		}
		json.Unmarshal([]byte(body), &r)
		efforts = append(efforts, r.Effort)
	}
	if len(efforts) != 2 || efforts[0] != "low" || efforts[1] != "high" {
		t.Fatalf("两轮发出去的思考档位是 %v，应该是 [low high]", efforts)
	}
}
