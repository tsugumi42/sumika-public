package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func mcpFixtureStore(t *testing.T) store.EcosystemStore {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := set.Close(); err != nil {
			t.Error(err)
		}
	})
	return store.EcosystemStore{DB: set.TraceStore().Trace}
}
func mcpAccess(t *testing.T, cap string) (*ecosystem.Authority, ecosystem.Access) {
	t.Helper()
	a := ecosystem.NewAuthority()
	grant, err := a.Issue(kernel.Scope{CharacterID: "fixture", Conversation: "fixture", ScopeID: "fixture", Platform: "local", Channel: "console", Scene: "private", AccountID: "70001", ActorKind: "owner", ActorID: "70002", AuthorityID: ecosystem.ID(), AuthorityRevision: 1}, []string{cap}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return a, grant
}
func fixtureServer(calls *atomic.Int32) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "search", Description: "搜索编造的公开文档", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`)}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		var args struct{ Query string }
		if err := json.Unmarshal(r.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "文档章节：" + args.Query}}, StructuredContent: map[string]any{"source": "https://example.com/fixture"}}, nil
	})
	return s
}
func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("SUMIKA_K4_MCP_HELPER") != "1" {
		return
	}
	var count atomic.Int32
	s := fixtureServer(&count)
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
func TestMCPResolveApproveDispatch(t *testing.T) {
	for _, transport := range []string{"stdio", "http"} {
		t.Run(transport, func(t *testing.T) {
			journal := mcpFixtureStore(t)
			auth, a := mcpAccess(t, "mcp:docs")
			client := New(journal)
			cfg := ServerConfig{ID: "docs", Sumika: ServerPolicy{Enabled: true, Include: []string{"search"}}}
			var count atomic.Int32
			if transport == "http" {
				server := fixtureServer(&count)
				handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
				httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("X-Fixture-Credential") != "first" {
						t.Error("已批准连接使用了调用方后来替换的凭据")
					}
					handler.ServeHTTP(w, r)
				}))
				defer httpServer.Close()
				cfg.Type = "http"
				cfg.URL = httpServer.URL
				cfg.Headers = map[string]string{"X-Fixture-Credential": "first"}
			} else {
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cfg.Command = exe
				cfg.Args = []string{"-test.run=^TestMCPStdioHelper$"}
				cfg.CWD = t.TempDir()
				cfg.Env = map[string]string{"SUMIKA_K4_MCP_HELPER": "1", "TEMP": cfg.CWD, "TMP": cfg.CWD}
			}
			defer client.Close()
			if err := client.Connect(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			t.Logf("transport=%s negotiated_protocol=%s", transport, client.servers[cfg.ID].session.InitializeResult().ProtocolVersion)
			list, err := client.Search(t.Context(), a, SearchRequest{Query: "搜索"})
			if err != nil || len(list.Items) != 1 {
				t.Fatalf("search %+v %v", list, err)
			}
			entry := list.Items[0]
			p, err := client.Resolve(t.Context(), a, entry.PublicID, entry.SchemaDigest, json.RawMessage(`{"query":"fixture"}`))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Execute(t.Context()); err == nil {
				t.Fatal("未审批仍派发")
			}
			if err := p.Approve(t.Context(), func(ctx context.Context, p *ResolvedCall) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if transport == "http" {
				cfg.Headers["X-Fixture-Credential"] = "second"
			}
			result, err := p.Execute(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if result.StructuredContent == nil || result.IsError {
				t.Fatalf("错误结果 %+v", result)
			}
			p, err = client.Resolve(t.Context(), a, entry.PublicID, entry.SchemaDigest, json.RawMessage(`{"query":"stale"}`))
			if err != nil {
				t.Fatal(err)
			}
			if err = p.Approve(t.Context(), func(ctx context.Context, p *ResolvedCall) error { auth.Revoke(a.Scope().AuthorityID); return nil }); err == nil {
				t.Fatal("撤权后仍派发")
			}
			if transport == "http" && count.Load() != 1 {
				t.Fatalf("误调用 %d", count.Load())
			}
		})
	}
}
func TestMCPUnknownSurvivesReconnect(t *testing.T) {
	journal := mcpFixtureStore(t)
	if err := journal.MCPDispatch(t.Context(), "unknown-call", "docs", "old-binding", "http"); err != nil {
		t.Fatal(err)
	}
	if err := journal.MCPEnd(t.Context(), "unknown-call", false, "error"); err != nil {
		t.Fatal(err)
	}
	blocked, err := journal.MCPBlocked(t.Context(), "docs")
	if err != nil || !blocked {
		t.Fatal("未持久隔离")
	}
	journal2 := store.EcosystemStore{DB: journal.DB}
	if err := journal2.MCPDispatch(t.Context(), "new-call", "docs", "new-binding", "http"); err == nil {
		t.Fatal("新绑定清掉隔离")
	}
}
func TestMCPConfigurationRejectsDuplicateAndMissingCredentials(t *testing.T) {
	for _, data := range []string{`{"sumika":{"enabled":true,"enabled":false}}`, `{"sumika":{"enabled":true},"mcpServers":{"docs":{"command":"fixture.exe","args":["${secret:missing}"],"sumika":{"enabled":true}}}}`, `{"sumika":{"enabled":true},"mcpServers":{"docs":{"env":{"TOKEN":"${secret:missing}"},"sumika":{"enabled":false}}}}`} {
		if _, err := ParseConfiguration([]byte(data), filepath.Join(t.TempDir(), "mcp.json"), func(string, string) (string, error) { return "", os.ErrNotExist }); err == nil {
			t.Fatalf("配置未拒绝 %s", data)
		}
	}
}

// 放行规则写进磁盘，认的指纹必须跨重启稳定；每个进程随机的 Binding 不行。
func TestMCPFingerprintStableAcrossClients(t *testing.T) {
	var count atomic.Int32
	server := fixtureServer(&count)
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()
	resolve := func(headers map[string]string, url string) (fp, binding string) {
		_, a := mcpAccess(t, "mcp:docs")
		client := New(mcpFixtureStore(t))
		defer client.Close()
		cfg := ServerConfig{ID: "docs", Type: "http", URL: url, Headers: headers, Sumika: ServerPolicy{Enabled: true, Include: []string{"search"}}}
		if err := client.Connect(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
		list, err := client.Search(t.Context(), a, SearchRequest{Query: "搜索"})
		if err != nil || len(list.Items) != 1 {
			t.Fatalf("search %+v %v", list, err)
		}
		e := list.Items[0]
		p, err := client.Resolve(t.Context(), a, e.PublicID, e.SchemaDigest, json.RawMessage(`{"query":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		return p.Fingerprint(), p.Binding()
	}
	fp1, b1 := resolve(map[string]string{"X-Key": "one"}, httpServer.URL)
	fp2, b2 := resolve(map[string]string{"X-Key": "two"}, httpServer.URL)
	if b1 == b2 {
		t.Fatal("前提不成立：新进程的 Binding 应该不同")
	}
	if fp1 == "" || fp1 != fp2 {
		t.Fatalf("重启后指纹要一致：%q %q", fp1, fp2)
	}
	if strings.Contains(fp1, "one") {
		t.Fatal("指纹不能带凭据")
	}
	if fp3, _ := resolve(map[string]string{"X-Key": "one"}, httpServer.URL+"/other"); fp3 == fp1 {
		t.Fatal("地址变了指纹要变")
	}
}
