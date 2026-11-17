package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/approval"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/llmtest"
)

// 检查点范围：只读命令不拍；只改一个文件的命令只拍它，回退能还原；看不清的命令和超大文件照常执行并说明。
func TestCheckpointScopeByCommand(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	work := t.TempDir()
	for name, body := range map[string]string{"only.txt": "old", "other.txt": "untouched"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "big.bin"), bytes.Repeat([]byte{1}, 17<<20), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var lastBody atomic.Value
	var firstResultHasCheckpoint atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := llmtest.Text("完成。")
		if r.Method == http.MethodPost {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
			n := calls.Add(1) - 1
			lastBody.Store(string(raw))
			if n == 1 && strings.Contains(string(raw), "[checkpoint ") {
				firstResultHasCheckpoint.Store(true)
			}
			switch n {
			case 0:
				reply = llmtest.Call("c-readonly", "powershell", `{"purpose":"编造看时间","command":"Get-Date"}`)
			case 1:
				reply = llmtest.Call("c-one", "powershell", `{"purpose":"编造改一个文件","command":"Set-Content -LiteralPath only.txt -Value new -NoNewline"}`)
			case 2:
				reply = llmtest.Call("c-unclear", "powershell", `{"purpose":"编造看不清的命令","command":"$x = 'Get-Date'; & $x"}`)
			case 3:
				reply = llmtest.Call("c-big", "write", `{"purpose":"编造覆盖大文件","path":"big.bin","content":"small"}`)
			}
		}
		llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}, Reply: reply}, &e.bodies, &e.mu)(w, r)
	}))
	defer gateway.Close()
	e.llm = gateway
	e.configure("tools:\n  workdir: '" + filepath.ToSlash(work) + "'\n")
	a := e.open()
	d := a.Desktop()
	defer func() {
		d.Close()
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	}()
	key := localKey("console:" + testChar)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	a.approvals.Subscribe(func(req approval.Request) {
		if err := d.Reader.DecideLocal(ctx, key, req.ID, api.ApproveOnce); err != nil {
			t.Error(err)
			cancel()
		}
	}, nil)
	if err := d.Reader.SendLocal(ctx, key, "编造文件任务", nil); err != nil {
		t.Fatal(err)
	}
	if firstResultHasCheckpoint.Load() {
		t.Fatal("只读命令也拍了快照")
	}
	body, _ := lastBody.Load().(string)
	for _, want := range []string{"看不清这条命令会动哪些文件", "检查点容量或扫描预算不足", "改动不能用检查点回退"} {
		if !strings.Contains(body, want) {
			t.Fatalf("模型没收到说明 %q", want)
		}
	}
	records, err := d.Reader.ListCheckpoints(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("只读命令不该有记录，应有 改单文件 + 两条 skipped：%+v", records)
	}
	// 新的在前：big.bin、看不清、改单文件
	if records[0].State != "skipped" || records[1].State != "skipped" || records[2].State != "ready" || records[2].Files != 1 || records[0].Note == "" {
		t.Fatalf("记录状态不符：%+v", records)
	}
	if got, _ := os.ReadFile(filepath.Join(work, "big.bin")); string(got) != "small" {
		t.Fatalf("大文件没有照常写入：%d 字节", len(got))
	}
	plan, err := d.Reader.PreviewCheckpoint(ctx, key, records[2].ID, "step")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].Path != "only.txt" {
		t.Fatalf("回退计划不止目标文件：%+v", plan)
	}
	if _, err := d.Reader.PreviewCheckpoint(ctx, key, records[0].ID, "step"); err == nil {
		t.Fatal("没拍快照的记录给出了回退计划")
	}
	if _, err := d.Reader.RestoreCheckpoint(ctx, key, records[2].ID, "step", plan.Version); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(work, "only.txt")); string(got) != "old" {
		t.Fatalf("没有还原：%q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(work, "other.txt")); string(got) != "untouched" {
		t.Fatalf("动了无关文件：%q", got)
	}
}

// 不是主人 × 本地私聊：只读命令、看不清的命令、写文件一样被拦，工具不执行。
func TestCheckpointedToolsStillNeedOwner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	work := t.TempDir()
	target := filepath.Join(work, "a.txt")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	e.configure("tools:\n  workdir: '" + filepath.ToSlash(work) + "'\n")
	a := e.open()
	defer a.Close()
	scope := kernel.Scope{Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:101:202", ActorKind: "human", ActorID: "202", AccountID: "101", CharacterID: testChar, ScopeID: "qq", AuthorityID: "x", AuthorityRevision: 1}
	ctx := kernel.WithInvocation(t.Context(), kernel.Invocation{Scope: scope, RootID: "root", RunID: "run", ToolID: "tool", Kind: kernel.KindForeground})
	for name, raw := range map[string]string{
		"powershell": `{"command":"Get-Date"}`,
		"write":      `{"path":"a.txt","content":"hacked"}`,
	} {
		args, err := jsjson.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := a.executeCheckpointed(ctx, work, name, args); err == nil {
			t.Fatalf("%s 被放行：%+v", name, res)
		}
	}
	args, _ := jsjson.Parse(`{"command":"$x = 'Get-Date'; & $x"}`)
	if _, err := a.executeCheckpointed(ctx, work, "powershell", args); err == nil {
		t.Fatal("看不清的命令被放行")
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatalf("文件被改了：%q", got)
	}
}
