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
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/llmtest"
)

// 正式 App→模型工具→真实文件/PowerShell→桌面审批→恢复→新 Root 资料，所有数据均在临时目录。
func TestCheckpointDesktopRoundTrip(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	work := t.TempDir()
	original := []byte("old\r\n")
	target := filepath.Join(work, "a.txt")
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var sawCheckpoint, sawRecovery atomic.Bool
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
			if n > 0 && strings.Contains(string(raw), "[checkpoint ") {
				sawCheckpoint.Store(true)
			}
			if n >= 4 && strings.Contains(string(raw), "检查点恢复") {
				sawRecovery.Store(true)
			}
			switch n {
			case 0:
				reply = llmtest.Call("fixture-write", "write", `{"purpose":"编造改写","path":"a.txt","content":"new\r\n"}`)
			case 1:
				reply = llmtest.Call("fixture-edit", "edit", `{"purpose":"编造修改","path":"a.txt","edits":[{"oldText":"new","newText":"updated"}]}`)
			case 2:
				reply = llmtest.Call("fixture-shell", "powershell", `{"purpose":"编造改名和创建","command":"Rename-Item -LiteralPath a.txt -NewName renamed.txt; Set-Content -LiteralPath created.txt -Value created -NoNewline"}`)
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
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	var approveRestore atomic.Bool
	var restoreCards atomic.Int32
	a.approvals.Subscribe(func(req approval.Request) {
		allow := true
		if req.Tool == "checkpoint_restore" {
			restoreCards.Add(1)
			allow = approveRestore.Load()
			if len(req.Options) != 2 || req.Options[0] != "once" || req.Identity.ToolID != req.ToolCallID || req.Identity.Scope.Conversation != key.Conversation {
				t.Error("恢复审批不是精确目标单次身份", req)
			}
		}
		if err := d.Reader.DecideLocal(ctx, key, req.ID, approvalAnswer(allow)); err != nil {
			t.Error(err)
			cancel()
		}
	}, nil)
	if err := d.Reader.SendLocal(ctx, key, "编造文件任务", nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(work, "renamed.txt"))
	if err != nil || string(got) != "updated\r\n" {
		t.Fatalf("真实 PowerShell 未改名：%q %v", got, err)
	}
	records, err := d.Reader.ListCheckpoints(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || !sawCheckpoint.Load() {
		t.Fatalf("正式工具或下一轮回执缺失：records=%+v saw=%v", records, sawCheckpoint.Load())
	}
	id := records[len(records)-1].ID
	plan, err := d.Reader.PreviewCheckpoint(ctx, key, id, "turn")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 3 {
		t.Fatalf("回合变化不完整：%+v", plan)
	}
	a.approvalGate.Mode = "yolo"
	if _, err := d.Reader.RestoreCheckpoint(ctx, key, id, "turn", plan.Version); err == nil {
		t.Fatal("YOLO 绕过恢复单次拒绝")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("拒绝后仍恢复了原路径", err)
	}
	approveRestore.Store(true)
	restored, err := d.Reader.RestoreCheckpoint(ctx, key, id, "turn", plan.Version)
	if err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != string(original) || restored.InverseID == "" || len(restored.Completed) != 3 {
		t.Fatalf("正式恢复不符：%q %+v %v", got, restored, err)
	}
	for _, name := range []string{"renamed.txt", "created.txt"} {
		if _, err := os.Stat(filepath.Join(work, name)); !os.IsNotExist(err) {
			t.Fatalf("新文件未撤销 %s %v", name, err)
		}
	}
	if restoreCards.Load() != 2 {
		t.Fatalf("恢复卡片数 %d", restoreCards.Load())
	}
	if err := d.Reader.SendLocal(ctx, key, "编造恢复后验证", nil); err != nil {
		t.Fatal(err)
	}
	if !sawRecovery.Load() {
		t.Fatal("新 Root 模型请求没收到持久恢复通知")
	}
	var approvals int
	if err := a.set.TraceStore().Trace.QueryRow(`SELECT count(*) FROM steps WHERE name='checkpoint_restore' AND identity_json IS NOT NULL AND approval_json IS NOT NULL AND result_ref IS NOT NULL`).Scan(&approvals); err != nil || approvals != 2 {
		t.Fatalf("恢复审计没有同一身份/审批/结果：%d %v", approvals, err)
	}
	for _, bad := range []api.ConversationKey{{Platform: "local", Channel: "console", Scene: "group", Conversation: "console:group:invented"}, {Platform: "qq", Channel: "qq", Scene: "private", Conversation: "qq:private:101:202"}, {Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:303"}} {
		if _, err := d.Reader.PreviewCheckpoint(ctx, bad, id, "turn"); err == nil {
			t.Fatalf("其它场合得到恢复计划：%+v", bad)
		}
	}
	d.Close()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = e.open()
	d = a.Desktop()
	sawRecovery.Store(false)
	cold, err := d.Reader.ListCheckpoints(ctx, key)
	if err != nil || len(cold) != 4 {
		t.Fatalf("冷恢复检查点不符：%+v %v", cold, err)
	}
	if err := d.Reader.SendLocal(ctx, key, "编造重启后验证", nil); err != nil {
		t.Fatal(err)
	}
	if !sawRecovery.Load() {
		t.Fatal("冷启动新 Root 未收到恢复事实")
	}
	d.Close()
	if _, err := d.Reader.ListCheckpoints(ctx, key); err == nil {
		t.Fatal("关闭中的桌面仍可读检查点")
	}
	if _, err := d.Reader.RestoreCheckpoint(ctx, key, id, "turn", plan.Version); err == nil {
		t.Fatal("关闭中的桌面仍可恢复")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	e.configure("tools:\n  workdir: '" + filepath.ToSlash(work) + "'\n  checkpoints:\n    enabled: false\n")
	a = e.open()
	d = a.Desktop()
	if _, err := d.Reader.ListCheckpoints(ctx, key); err == nil {
		t.Fatal("关闭检查点后仍公开记录")
	}
	if _, err := d.Reader.PreviewCheckpoint(ctx, key, id, "turn"); err == nil {
		t.Fatal("关闭检查点后仍生成计划")
	}
	if _, err := d.Reader.RestoreCheckpoint(ctx, key, id, "turn", plan.Version); err == nil {
		t.Fatal("关闭检查点后仍可恢复")
	}
	t.Logf("App smoke: captures=%d restoreCards=%d restoredFiles=%d modelCalls=%d recoveryNotice=%v", len(records), restoreCards.Load(), len(restored.Completed), calls.Load(), sawRecovery.Load())
}

// 父回合回退不吞子任务；子记录只通过持久 TaskRecord 归还原主人。
func TestCheckpointChildOwner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	work := t.TempDir()
	target := filepath.Join(work, "child.txt")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	e.configure("tools:\n  workdir: '" + filepath.ToSlash(work) + "'\nsubagents:\n  enabled: true\n  model: fixture-checkpoint-child\n")
	var parentCalls, childCalls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := llmtest.Text("编造任务完成")
		if r.Method == http.MethodPost {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
			var request struct{ Model string }
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Error(err)
				return
			}
			if request.Model == "fixture-checkpoint-child" {
				if childCalls.Add(1) == 1 {
					reply = llmtest.Call("child-write", "write", `{"purpose":"编造子修改","path":"child.txt","content":"child"}`)
				}
			} else {
				switch parentCalls.Add(1) {
				case 1:
					reply = llmtest.Call("parent-write", "write", `{"purpose":"编造父修改","path":"parent.txt","content":"parent"}`)
				case 2:
					reply = llmtest.Call("child-task", "task", `{"agent":"worker","description":"编造子任务","prompt":"修改 child.txt 后结束"}`)
				}
			}
		}
		llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`), json.RawMessage(`{"id":"fixture-checkpoint-child","context_length":32768}`)}, Reply: reply}, &e.bodies, &e.mu)(w, r)
	}))
	defer gateway.Close()
	e.llm = gateway
	a := e.open()
	d := a.Desktop()
	defer func() { d.Close(); a.Close() }()
	a.approvalGate.Mode = "yolo"
	a.approvalGate.Ask = func(context.Context, approval.Request, *approval.Pending) approval.Decision {
		return approval.Decision{Kind: "approved"}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(ctx, key, "编造父子任务", nil); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		if err := a.set.TraceStore().Trace.QueryRow(`SELECT count(*) FROM kernel_tasks WHERE state='completed'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("子任务未完成", ctx.Err())
		}
	}
	owner, err := d.Reader.localInputScope(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	records, err := a.checkpoints.List(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("父子捕获不全：%+v", records)
	}
	var parentID, childID string
	for _, record := range records {
		if record.Invocation.Kind == kernel.KindChild {
			childID = record.ID
			if record.Owner != owner || record.Invocation.Scope.Conversation == owner.Conversation {
				t.Fatal("子身份被冒充为父", record)
			}
			forged := record.Invocation
			forged.RootID += "-forged"
			if _, err := a.checkpointOwner(ctx, forged); err == nil {
				t.Fatal("伪造子 Root 取得主人所有权")
			}
		} else {
			parentID = record.ID
		}
	}
	if parentID == "" || childID == "" {
		t.Fatal("缺父子记录", records)
	}
	parentPlan, err := d.Reader.PreviewCheckpoint(ctx, key, parentID, "turn")
	if err != nil {
		t.Fatal(err)
	}
	if len(parentPlan.Files) != 1 {
		t.Fatal("父回合吞了子变化", parentPlan)
	}
	if _, err := d.Reader.RestoreCheckpoint(ctx, key, parentID, "turn", parentPlan.Version); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "child" {
		t.Fatal("父回退改了子路径", string(got), err)
	}
	childPlan, err := d.Reader.PreviewCheckpoint(ctx, key, childID, "step")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Reader.RestoreCheckpoint(ctx, key, childID, "step", childPlan.Version); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "old" {
		t.Fatal("原主人未恢复子文件", string(got), err)
	}
	if _, err := os.Stat(filepath.Join(work, "parent.txt")); !os.IsNotExist(err) {
		t.Fatal("恢复子变化重新写了父路径", err)
	}
}

// 后态拍不成（写出超过单文件上限的文件）：工具已执行，结果照常交回并说明"不要重跑"，整轮不失败；记录停在 interrupted。
func TestCheckpointAfterStateFailureKeepsResult(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := localDesktop(t, e, llmtest.Script{Replies: []llmtest.Reply{
		// 15 MiB 的源文件复制四份：前态 15 MiB，后态 75 MiB，超过 64 MiB 的扫描字节预算。
		llmtest.Call("oversized-copy", "powershell", `{"purpose":"编造后态超限","command":"Copy-Item -LiteralPath src.bin -Destination d1.bin; Copy-Item -LiteralPath src.bin -Destination d2.bin; Copy-Item -LiteralPath src.bin -Destination d3.bin; Copy-Item -LiteralPath src.bin -Destination d4.bin"}`),
		llmtest.Text("继续"),
	}})
	if err := os.WriteFile(filepath.Join(a.workdir(), "src.bin"), bytes.Repeat([]byte{7}, 15<<20), 0600); err != nil {
		t.Fatal(err)
	}
	a.approvalGate.Ask = func(context.Context, approval.Request, *approval.Pending) approval.Decision {
		return approval.Decision{Kind: "approved"}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(ctx, key, "编造后态失败任务", nil); err != nil {
		t.Fatal("工具已执行，整轮不该失败：", err)
	}
	if info, err := os.Stat(filepath.Join(a.workdir(), "d4.bin")); err != nil || info.Size() != 15<<20 {
		t.Fatal("命令没有真正执行", info, err)
	}
	records, err := d.Reader.ListCheckpoints(ctx, key)
	if err != nil || len(records) != 1 || records[0].State != "interrupted" {
		t.Fatal("后态失败未保留未知结局", records, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.bodies) != 2 || !strings.Contains(string(e.bodies[1]), "不要重跑") {
		t.Fatalf("模型没收到“不要重跑”的说明：%d 次调用", len(e.bodies))
	}
}

// approvalAnswer 把测试里的 允许 / 拒绝 换成审批卡上的回答。
func approvalAnswer(allow bool) api.ApprovalChoice {
	if allow {
		return api.ApproveOnce
	}
	return api.ApproveDeny
}
