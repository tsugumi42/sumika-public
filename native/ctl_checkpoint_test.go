package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/llm/llmtest"
)

// 独立客户端进程→测试专用 Windows 管道→真宿主→原有桌面审批；不启动真实 Sumika 或协议端。
func TestCtlCheckpointCommandsOverPipe(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("命令行走真管道的全流程，放在每日整套；命令本身有单测")
	}
	opt := logEnv(t)
	work := t.TempDir()
	target := filepath.Join(work, "fixture.txt")
	if err := os.WriteFile(target, []byte("old\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var modelCalls atomic.Int32
	var bulk atomic.Bool
	var bodies []string
	var mu sync.Mutex
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := llmtest.Text("编造任务结束")
		if r.Method == http.MethodPost && modelCalls.Add(1) == 1 {
			reply = llmtest.Call("fixture-write", "write", `{"purpose":"编造改写","path":"fixture.txt","content":"agent"}`)
		}
		if r.Method == http.MethodPost && bulk.Swap(false) {
			// 目标要写明才会拍快照：一千条写明文件名的命令。
			names := make([]string, 1000)
			for i := range names {
				names[i] = fmt.Sprintf("bulk-%04d.txt", i)
			}
			bulkCommand := "Set-Content -LiteralPath " + strings.Join(names, ",") + " -Value bulk -NoNewline"
			args, _ := json.Marshal(map[string]string{"purpose": "编造批量文件", "command": bulkCommand})
			reply = llmtest.Call("fixture-bulk", "powershell", string(args))
		}
		llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + logTestModel + `","context_length":32768}`)}, Reply: reply}, &bodies, &mu)(w, r)
	}))
	defer gateway.Close()
	cfgPath := filepath.Join(opt.DataDir, "config", "sumika.yml")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// 保留同一套临时宿主配置，只替换模型地址并指定受保护的临时 CWD。
	lines := strings.Split(string(cfg), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "  baseUrl:") {
			lines[i] = "  baseUrl: " + gateway.URL
		}
	}
	cfg = []byte(strings.Join(lines, "\n") + "\ntools:\n  workdir: '" + filepath.ToSlash(work) + "'\n")
	if err := os.WriteFile(cfgPath, cfg, 0600); err != nil {
		t.Fatal(err)
	}
	exe := buildCtlTestClient(t)
	e := newCtlEnvWith(t, false, opt)
	e.sh.host.start()
	// 整条测试的总预算：空闲机器上约 50 秒（两轮 1000 个文件的恢复，每个文件都要落盘），忙时翻几倍；超了现在会报错而不是卡住
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	var commands atomic.Int32
	// runCommand 只返回错误、不碰 t：它也在后台 goroutine 里跑，t.Fatal 在那里只会让这个 goroutine 退出，
	// 主 goroutine 等的回复就永远不来（2026-11-08 实测：ctx 到期后测试卡到总超时 20 分钟）。
	runCommand := func(args ...string) (ctlproto.Reply, int, string, error) {
		commands.Add(1)
		argv := append([]string{"--json", "checkpoint"}, args...)
		cmd := exec.CommandContext(ctx, exe, argv...)
		cmd.Env = append(os.Environ(), "SUMIKA_CTL_TEST_PIPE="+e.name)
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				return ctlproto.Reply{}, 0, string(out), err
			}
			code = exit.ExitCode()
		}
		var reply ctlproto.Reply
		if err := json.Unmarshal(out, &reply); err != nil {
			return ctlproto.Reply{}, code, string(out), fmt.Errorf("客户端输出不是 JSON：%w %s（ctx：%v）", err, out, ctx.Err())
		}
		return reply, code, string(out), nil
	}
	command := func(args ...string) (ctlproto.Reply, int, string) {
		t.Helper()
		reply, code, out, err := runCommand(args...)
		if err != nil {
			t.Fatal(err)
		}
		return reply, code, out
	}
	// commandAsync 在后台跑一条命令；结果（含错误）一定会送到返回的通道里，由主 goroutine 取了以后判断
	type commandResult struct {
		reply ctlproto.Reply
		err   error
	}
	commandAsync := func(args ...string) <-chan commandResult {
		done := make(chan commandResult, 1)
		go func() {
			reply, _, _, err := runCommand(args...)
			done <- commandResult{reply, err}
		}()
		return done
	}
	await := func(done <-chan commandResult) ctlproto.Reply {
		t.Helper()
		res := <-done
		if res.err != nil {
			t.Fatal(res.err)
		}
		return res.reply
	}
	conv := "console:" + logTestChar
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: conv}
	demo := newCtlEnvWith(t, true, opt)
	demoReply := demo.ask(t, ctlproto.Request{Cmd: ctlproto.CmdCheckpoint, Args: []string{"list", conv}})
	if demoReply.OK || demoReply.Code != ctlproto.CodeDemo {
		t.Fatal("演示实例公开了检查点", demoReply)
	}
	if reply, code, _ := command("list", conv); code != ctlproto.ExitFailed || reply.Code != ctlproto.CodeReadOnly {
		t.Fatal("archive 允许了检查点", reply, code)
	}
	e.sh.host.startReceive()
	r := &e.sh.host.desk.Reader
	sub, err := r.WatchLocalApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var allowRestore atomic.Bool
	var cards atomic.Int32
	worker := make(chan struct{})
	go func() {
		defer close(worker)
		seen := map[string]bool{}
		for {
			select {
			case <-ctx.Done():
				return
			case snap, ok := <-sub.Updates():
				if !ok {
					return
				}
				for _, card := range snap.Value {
					if seen[card.ID] {
						continue
					}
					seen[card.ID] = true
					answer := api.ApproveOnce
					if card.Tool == "checkpoint_restore" {
						cards.Add(1)
						if !allowRestore.Load() {
							answer = api.ApproveDeny
						}
					}
					if err := r.DecideLocal(ctx, key, card.ID, answer); err != nil && ctx.Err() == nil {
						t.Error(err)
						cancel()
						return
					}
				}
			}
		}
	}()
	defer func() { cancel(); <-worker }()
	if err := r.SendLocal(ctx, key, "编造管道任务", nil); err != nil {
		t.Fatal(err)
	}
	reply, code, _ := command("list", conv)
	list := dataAs[ctlproto.CheckpointList](t, reply)
	if code != 0 || len(list.Items) != 1 {
		t.Fatal("列出缺失", reply, code)
	}
	id := list.Items[0].ID
	reply, code, _ = command("preview", conv, id)
	plan := dataAs[ctlproto.CheckpointPlan](t, reply)
	if code != 0 || len(plan.Files) != 1 || plan.Files[0].CurrentHash == plan.Files[0].TargetHash {
		t.Fatal("预览没反映真实变化", reply, code)
	}
	for _, subcommand := range []string{"status", "cancel"} {
		for _, args := range [][]string{{subcommand, "qq:private:101:202", id}, {subcommand, "qq:group:303", id}, {subcommand, "console:group:303", id}, {subcommand, conv}, {subcommand, conv, ""}, {subcommand, conv, "0"}, {subcommand, conv, "../outside"}, {subcommand, conv, id, "force"}} {
			reply, code, _ := command(args...)
			if reply.OK || code != ctlproto.ExitDenied {
				t.Fatal("进度/取消坏参数被接受", args, reply, code)
			}
		}
	}
	for _, args := range [][]string{{"list", "qq:private:101:202"}, {"list", "qq:group:303"}, {"list", "console:group:303"}, {"list", ""}, {"list", conv + ":topic:"}, {"list", conv, "force"}, {"preview", conv}, {"preview", conv, ""}, {"preview", conv, "0"}, {"preview", conv, "../outside"}, {"preview", conv, id, "invalid"}, {"restore", conv, id, plan.Version, "step", "force"}, {"restore", conv, id, "bad-version"}, {"restore", conv, id, plan.Version, "0"}} {
		reply, code, _ := command(args...)
		if reply.OK || code != ctlproto.ExitDenied || cards.Load() != 0 {
			t.Fatalf("坏参数进了恢复链：%v %+v code=%d cards=%d", args, reply, code, cards.Load())
		}
	}
	reply, code, _ = command("restore", conv, id, plan.Version)
	current, err := os.ReadFile(target)
	if err != nil || code != ctlproto.ExitFailed || reply.OK || !bytes.Equal(current, []byte("agent")) {
		t.Fatalf("拒绝仍写了文件：%+v %d %q %v", reply, code, current, err)
	}
	allowRestore.Store(true)
	if err := os.WriteFile(target, []byte("manual"), 0600); err != nil {
		t.Fatal(err)
	}
	reply, code, _ = command("restore", conv, id, plan.Version)
	current, err = os.ReadFile(target)
	if err != nil || reply.OK || code != ctlproto.ExitFailed || cards.Load() != 1 || !bytes.Equal(current, []byte("manual")) {
		t.Fatal("旧版本覆盖了人工修改", reply, code, cards.Load(), string(current), err)
	}
	if err := os.WriteFile(target, []byte("agent"), 0600); err != nil {
		t.Fatal(err)
	}
	reply, code, _ = command("restore", conv, id, plan.Version)
	restored := dataAs[ctlproto.CheckpointRestore](t, reply)
	current, err = os.ReadFile(target)
	if err != nil || code != 0 || !bytes.Equal(current, []byte("old\r\n")) || restored.InverseID == "" || len(restored.Completed) != 1 {
		t.Fatalf("管道恢复失败：%+v %q %d %v", reply, current, code, err)
	}
	reply, code, _ = command("preview", conv, restored.InverseID)
	undo := dataAs[ctlproto.CheckpointPlan](t, reply)
	if code != 0 {
		t.Fatal(reply)
	}
	reply, code, _ = command("restore", conv, undo.ID, undo.Version)
	current, err = os.ReadFile(target)
	if err != nil || code != 0 || !bytes.Equal(current, []byte("agent")) || cards.Load() != 3 {
		t.Fatalf("恢复的恢复失败：%+v %q cards=%d %v", reply, current, cards.Load(), err)
	}
	latest := dataAs[ctlproto.CheckpointRestore](t, reply)
	// 普通文本输出也实际走客户端主流程，不以 JSON 代替给人的用法。
	cmd := exec.CommandContext(ctx, exe, "checkpoint", "preview", conv, latest.InverseID)
	cmd.Env = append(os.Environ(), "SUMIKA_CTL_TEST_PIPE="+e.name)
	var text, errs bytes.Buffer
	cmd.Stdout = &text
	cmd.Stderr = &errs
	if err := cmd.Run(); err != nil || !strings.Contains(text.String(), "checkpoint restore") || !strings.Contains(text.String(), latest.InverseID) {
		t.Fatalf("人读预览不可操作：%v %s %s", err, text.String(), errs.String())
	}
	bulk.Store(true)
	if err := r.SendLocal(ctx, key, "编造长恢复", nil); err != nil {
		t.Fatal(err)
	}
	reply, code, _ = command("list", conv)
	list = dataAs[ctlproto.CheckpointList](t, reply)
	if code != 0 {
		t.Fatal(reply)
	}
	id = list.Items[0].ID
	reply, code, _ = command("preview", conv, id)
	plan = dataAs[ctlproto.CheckpointPlan](t, reply)
	if code != 0 || len(plan.Files) != 1000 {
		t.Fatal("批量捕获不符", reply, len(plan.Files))
	}
	progress, err := r.WatchCheckpointProgress(ctx, key, id)
	if err != nil {
		t.Fatal(err)
	}
	defer progress.Close()
	restoredReply := commandAsync("restore", conv, id, plan.Version)
	var live api.CheckpointProgress
	for live.Done == 0 {
		select {
		case snap := <-progress.Updates():
			if snap.Err != nil {
				t.Fatal(snap.Err)
			}
			live = snap.Value
		case <-ctx.Done():
			t.Fatal("桌面未收到恢复进度", ctx.Err())
		}
	}
	if live.Total != 1000 || live.Done >= live.Total {
		t.Fatal("桌面进度不符", live)
	}
	reply, code, _ = command("status", conv, id)
	status := dataAs[ctlproto.CheckpointProgress](t, reply)
	if code != 0 || status.Total != 1000 || status.Done < live.Done {
		t.Fatal("ctl 无法查询运行中恢复", reply, live)
	}
	reply, code, _ = command("cancel", conv, id)
	stopped := dataAs[ctlproto.CheckpointProgress](t, reply)
	if code != 0 || stopped.State != "partial" || stopped.Done == 0 || stopped.Done >= 1000 || stopped.Done+len(stopped.Remaining) != 1000 {
		t.Fatal("ctl 取消未得到一致结局", reply)
	}
	end := await(restoredReply)
	if end.OK {
		t.Fatal("被取消恢复回成功", end)
	}
	for {
		select {
		case snap := <-progress.Updates():
			if snap.Err != nil {
				t.Fatal(snap.Err)
			}
			if snap.Value.State == "partial" {
				if snap.Value.Done != stopped.Done {
					t.Fatal("桌面终态计数不符", snap.Value, stopped)
				}
				goto observed
			}
		case <-ctx.Done():
			t.Fatal("桌面没收到取消终态")
		}
	}
observed:
	for _, name := range stopped.Completed {
		if _, err := os.Stat(filepath.Join(work, name)); !os.IsNotExist(err) {
			t.Fatal("已完成文件仍存在", name, err)
		}
	}
	for _, name := range stopped.Remaining {
		body, err := os.ReadFile(filepath.Join(work, name))
		if err != nil || string(body) != "bulk" {
			t.Fatal("未完成文件被改动", name, err)
		}
	}
	for _, mode := range []string{"recovery", "resume"} {
		reply, code, _ = command("preview", conv, stopped.InverseID, mode)
		p := dataAs[ctlproto.CheckpointPlan](t, reply)
		want := stopped.Done
		if mode == "resume" {
			want = len(stopped.Remaining)
		}
		if code != 0 || len(p.Files) != want {
			t.Fatal("ctl 继续/撤回预览不符", mode, reply, want)
		}
	}
	lastInverseID := ""
	for _, mode := range []string{"recovery", "resume"} {
		next, err := r.PreviewCheckpoint(ctx, key, stopped.InverseID, mode)
		if err != nil {
			t.Fatal(err)
		}
		watch, err := r.WatchCheckpointProgress(ctx, key, stopped.InverseID)
		if err != nil {
			t.Fatal(err)
		}
		defer watch.Close()
		select {
		case snap, ok := <-watch.Updates():
			if !ok || snap.Err != nil || snap.Value.State != "idle" || snap.Value.TargetID != stopped.InverseID {
				t.Fatal("续做前订阅被旧终态结束", mode, ok, snap)
			}
		case <-ctx.Done():
			t.Fatal("续做前订阅未就绪", ctx.Err())
		}
		out, err := r.RestoreCheckpoint(ctx, key, stopped.InverseID, mode, next.Version)
		if err != nil || out.State != "ready" || len(out.Completed) != len(next.Files) {
			t.Fatal("桌面续做失败", mode, out, err)
		}
		lastInverseID = out.InverseID
		var terminal api.CheckpointProgress
		for snap := range watch.Updates() {
			if snap.Err != nil {
				t.Fatal(snap.Err)
			}
			terminal = snap.Value
		}
		if terminal.State != "ready" || !terminal.OutcomeSaved || terminal.Error != "" || terminal.TargetID != stopped.InverseID || terminal.InverseID != out.InverseID || terminal.Done != len(next.Files) || len(terminal.Remaining) != 0 {
			t.Fatal("订阅未绑定新的恢复结局", mode, terminal, out)
		}
		for i := range 1000 {
			name := filepath.Join(work, fmt.Sprintf("bulk-%04d.txt", i))
			if mode == "recovery" {
				body, err := os.ReadFile(name)
				if err != nil || string(body) != "bulk" {
					t.Fatal("撤回后原文不符", name, string(body), err)
				}
			} else if _, err := os.Stat(name); !os.IsNotExist(err) {
				t.Fatal("续做后仍有目标文件", name, err)
			}
		}
	}
	failedPlan, err := r.PreviewCheckpoint(ctx, key, lastInverseID, "step")
	if err != nil {
		t.Fatal(err)
	}
	failureWatch, err := r.WatchCheckpointProgress(ctx, key, lastInverseID)
	if err != nil {
		t.Fatal(err)
	}
	defer failureWatch.Close()
	restoredReply = commandAsync("restore", conv, lastInverseID, failedPlan.Version)
	for {
		select {
		case snap, ok := <-failureWatch.Updates():
			if !ok || snap.Err != nil || snap.Value.State == "ready" {
				t.Fatal("未到达进行中的结局故障注入点", ok, snap.Err)
			}
			if snap.Value.InverseID == "" {
				continue
			}
			fault := filepath.Join(opt.DataDir, "go", "work", "checkpoints", snap.Value.InverseID, "ready.bin")
			if err := os.WriteFile(fault, nil, 0600); err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("未收到故障注入前进度", ctx.Err())
		}
		break
	}
	failedReply := await(restoredReply)
	failedRestore := dataAs[ctlproto.CheckpointRestore](t, failedReply)
	if failedReply.OK || failedRestore.State != "interrupted" || len(failedRestore.Completed) != 1000 {
		t.Fatal("ctl 把保存失败当作成功", failedReply.OK, failedRestore.State, len(failedRestore.Completed))
	}
	reply, code, _ = command("status", conv, lastInverseID)
	failedStatus := dataAs[ctlproto.CheckpointProgress](t, reply)
	if code != 0 || failedStatus.State != "interrupted" || failedStatus.OutcomeSaved || failedStatus.Error == "" || failedStatus.Done != 1000 || failedStatus.InverseID != failedRestore.InverseID {
		t.Fatal("ctl status 丢失结局保存错误", failedStatus.State, failedStatus.OutcomeSaved, failedStatus.Error)
	}
	var failedTerminal api.CheckpointProgress
	for snap := range failureWatch.Updates() {
		if snap.Err != nil {
			t.Fatal(snap.Err)
		}
		failedTerminal = snap.Value
	}
	if failedTerminal.State != failedStatus.State || failedTerminal.OutcomeSaved || failedTerminal.Error != failedStatus.Error || failedTerminal.InverseID != failedStatus.InverseID || failedTerminal.Done != failedStatus.Done {
		t.Fatal("桌面订阅与 ctl status 保存错误不一致", failedTerminal.State, failedTerminal.Error)
	}
	commands.Add(1)
	cmd = exec.CommandContext(ctx, exe, "checkpoint", "status", conv, lastInverseID)
	cmd.Env = append(os.Environ(), "SUMIKA_CTL_TEST_PIPE="+e.name)
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), failedStatus.Error) || !strings.Contains(string(output), failedStatus.State) || !strings.Contains(string(output), failedStatus.InverseID) {
		t.Fatalf("人读 status 未暴露保存失败：%v %s", err, output)
	}
	t.Logf("C9 pipe progress smoke: completed=%d remaining=%d total=%d approvals=%d", stopped.Done, len(stopped.Remaining), stopped.Total, cards.Load())
	t.Logf("C8 pipe smoke: commands=%d approvals=%d modelCalls=%d restoredBytes=%d", commands.Load()+1, cards.Load(), modelCalls.Load(), len(current))
}
