package app

// 线上字节对拍（规格-模型对话编解码 设计第 1 条）：后台 memoryGenerator 的 []llm.Message 路径，
// 走 marshalNoEscape，和前台 messagesJSON 的转义风格不同。重录：先在 testdata/wire/intended.md 登记，再 WIRE_UPDATE=1 跑。

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"

	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	bodywire "sumika-native/internal/llm/llmtest/wire"
	"sumika-native/internal/modelcall"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turnbudget"
)

func TestWireBackgroundMemory(t *testing.T) {
	t.Parallel()
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Reply: llmtest.Text("记下了")}, &bodies, &mu))
	defer srv.Close()
	ledger := &turnbudget.Ledger{Store: st}
	caller := modelcall.New(llm.New(llm.Magpie(srv.URL+"/v1")), ledger, st, 2, 256)
	g := memoryGenerator{runtime: &ModelRuntime{Models: []llm.Model{{ID: "fiction-model", MaxOutputTokens: 64}}}, caller: caller, budget: ledger, character: "fiction"}
	if _, err := g.Call(context.Background(), "fiction-model", "记住：周五看牙医", "memory:fiction"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("应该发一次请求，发了 %d 次", len(bodies))
	}
	bodywire.Check(t, "testdata/wire", "background-memory", []byte(bodies[0]))
}
