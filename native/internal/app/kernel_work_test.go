package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
)

func TestKernelWorkEvidenceRoundTrip(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "proof.txt"), []byte(strings.Repeat("made-work-proof-tail\n", 80)), 0600); err != nil {
		t.Fatal(err)
	}
	const criterion = "proof.txt 的内容包含 made-work-proof-tail"
	const reviewer = "fixture-verifier"
	var mu sync.Mutex
	mainCalls, reviewCalls := 0, 0
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`), json.RawMessage(`{"id":"` + reviewer + `","context_length":32768}`)}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := llmtest.Text("普通聊天。")
		if r.Method == http.MethodPost {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
			var request struct {
				Model    string
				Tools    []any
				Messages []struct{ Role, Content string }
			}
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			if request.Model == reviewer {
				reviewCalls++
				if len(request.Tools) != 0 {
					t.Error("核验请求取得工具权限")
				}
				var snapshot struct {
					Criteria []string
					Evidence []struct{ Claim, Text string }
				}
				if len(request.Messages) != 2 {
					t.Error("核验请求包含主会话历史")
				} else if err := json.Unmarshal([]byte(request.Messages[1].Content), &snapshot); err != nil {
					t.Error(err)
				}
				if len(snapshot.Criteria) != 1 || snapshot.Criteria[0] != criterion || len(snapshot.Evidence) != 1 || snapshot.Evidence[0].Claim != "read" || !strings.Contains(snapshot.Evidence[0].Text, "made-work-proof-tail") {
					t.Error("核验没有读到实际文件证据", string(raw))
				}
				reply = llmtest.Text(`{"status":"done","criteria":[{"criterion":"` + criterion + `","status":"supported","evidence":[0]}],"findings":[]}`)
			} else {
				n := mainCalls
				mainCalls++
				if n == 0 {
					reply = llmtest.Call("read-proof", "read", `{"path":"proof.txt"}`)
				} else if n == 1 {
					reply = llmtest.Text("读到了 made-work-proof-tail。")
				}
			}
			mu.Unlock()
		}
		llmtest.Handler(llmtest.Script{Models: models, Reply: reply}, &e.bodies, &e.mu)(w, r)
	}))
	defer gateway.Close()
	e.llm = gateway
	e.configure("  verify:\n    enabled: true\n    model: " + reviewer + "\n  work:\n    readPageBytes: 1024\ntools:\n  workdir: '" + filepath.ToSlash(dir) + "'\n")
	a := e.open()
	d := a.Desktop()
	defer func() {
		d.Close()
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	}()
	key := localKey("console:" + testChar)
	started := time.Now()
	out := a.RunWork(t.Context(), key, "读取编造的 proof.txt 并核对内容", []string{criterion})
	if out.Outcome.Err != nil || out.Verdict.Status != "done" || out.Outcome.Result == nil || out.Outcome.Result.Reply != "读到了 made-work-proof-tail。" {
		t.Fatalf("实际工作未通过核验 verdict=%+v outcome=%+v", out.Verdict, out.Outcome)
	}
	if len(out.Runs) != 1 {
		t.Fatalf("意外续轮 %+v", out.Runs)
	}
	inv := out.Runs[0]
	state, _, err := a.hub.Cfg.Work.Load(t.Context(), inv)
	if err != nil || state.Revision != 0 || len(state.Evidence) != 1 {
		t.Fatalf("未压缩工作证据无效 %+v %v", state, err)
	}
	if state.Evidence[0].Ref.Bytes <= int64(a.hub.Cfg.WorkSettings.ReadPageBytes) {
		t.Fatal("证据夹具没有跨过读取页预算，未覆盖分页核验")
	}
	page, err := a.hub.Cfg.Results.Read(t.Context(), inv.Scope, state.Evidence[0].Ref.ID, 0, int64(a.hub.Cfg.WorkSettings.ReadPageBytes))
	if err != nil || !strings.Contains(page.Text, "made-work-proof-tail") {
		t.Fatalf("核验引用未落真实文件 %+v %v", page, err)
	}
	budget, err := a.hub.Cfg.Budget.Snapshot(t.Context(), inv.RootID)
	if err != nil || budget.Requests != 3 {
		t.Fatalf("主模型与核验未共享三次请求 %+v %v", budget, err)
	}
	for _, forbidden := range []api.ConversationKey{{Platform: "local", Channel: "console", Scene: "group", Conversation: "console:" + testChar + ":group:made"}, {Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:41001:41002"}, {Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:41003"}} {
		if rejected := a.RunWork(t.Context(), forbidden, "伪造显式工作", []string{criterion}); rejected.Outcome.Err == nil {
			t.Fatal("其它场合取得本地工作权限", forbidden)
		}
	}
	if err := d.Reader.SendLocal(t.Context(), key, "普通闲聊不要开审阅", nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if mainCalls != 3 || reviewCalls != 1 {
		t.Fatalf("普通聊天额外触发核验 main=%d review=%d", mainCalls, reviewCalls)
	}
	t.Logf("实际 App→工作材料→独立核验：root_requests=%d evidence_bytes=%d snapshot_revision=%d review_calls=%d elapsed_ms=%d", budget.Requests, state.Evidence[0].Ref.Bytes, state.Revision, reviewCalls, time.Since(started).Milliseconds())
}
