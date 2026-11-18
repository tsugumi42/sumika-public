package turn

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/tools"
)

func workInput(h *harness, text string) Input {
	character := h.c.CharacterID
	conv := consoleConv(character)
	scope := kernel.Scope{CharacterID: character, Conversation: conv, ScopeID: ConsoleScope, Platform: "local", Channel: "console", AccountID: "made-desktop", Scene: "private", ActorKind: "owner", ActorID: "made-owner", AuthorityID: "made-work-grant", AuthorityRevision: 1}
	return Input{CharacterID: character, Conversation: conv, Text: text, Audience: audience.Console(conv), Scope: ConsoleScope, Identity: kernel.Invocation{Scope: scope, RootID: "made-work-root", Kind: "foreground"}}
}
func approveWork(h *harness) {
	h.approvals.OnRequest = append(h.approvals.OnRequest, func(req approval.Request) {
		if ok, err := h.approvals.Decide(req.ID, "once", ""); !ok || err != nil {
			h.hub.plugins.logf("测试审批失败 %t %v", ok, err)
		}
	})
}
func workLimits(cfg *Config) {
	cfg.WorkSettings = config.DefaultWork()
	cfg.WorkSettings.KeepRecentTokens = 1
}
func workSemantic(s kernel.WorkState) kernel.WorkState { s.Revision, s.ThroughOrdinal = 0, 0; return s }

// K2-S1 同时使用真实 PowerShell 和正式 Hub→Gate→finalizeTool→重开→result_read 链路。
func TestWorkProjectionColdHot(t *testing.T) {
	t.Parallel()
	var executed int
	todo := callReply("todo_write", `{"todos":[{"content":"核对最终标记","status":"pending"}]}`)
	command := callReply("powershell", `{"purpose":"生成隔离验收正文","command":"Write-Output ('x' * 24000); Write-Output 'made-evidence-tail'"}`)
	failed := callReply("powershell", `{"purpose":"记录已知失败","command":"Write-Output 'made-known-failure'; exit 7"}`)
	mutate := func(cfg *Config) {
		workLimits(cfg)
		cfg.ExecTool = func(ctx context.Context, cwd, name string, args jsjson.Value) (tools.Result, error) {
			executed++
			return tools.Execute(ctx, cwd, name, args)
		}
	}
	h := newHarnessOpts(t, harnessOpts{replies: `[` + todo + `,` + command + `,` + failed + `,` + textReply + `]`, mutate: mutate})
	approveWork(h)
	in := workInput(h, "目标：保留证据后继续核对最终标记")
	wait(t, h.hub.Respond(in))
	if executed != 3 {
		t.Fatalf("执行次数 %d", executed)
	}
	if !strings.Contains(h.body(0), `"name":"result_read"`) || !strings.Contains(h.body(2), "result_read({id,offset,limit})") || !strings.Contains(h.body(3), "made-known-failure") {
		t.Fatal("模型没有看到工具声明、结果引用或失败证据")
	}
	p := h.hub.Cfg.Work.(*workPort)
	hot, _, err := p.Load(context.Background(), in.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if hot.Goal != in.Text || len(hot.Pending) != 1 || hot.Pending[0] != "核对最终标记" || len(hot.Unknown) != 1 || len(hot.Evidence) < 2 {
		t.Fatalf("工作语义缺失：%+v", hot)
	}
	compacted, err := h.hub.compactWork(context.Background(), in)
	if err != nil || !compacted {
		t.Fatalf("压缩未发生：%t %v", compacted, err)
	}
	after, _, err := p.Load(context.Background(), in.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(workSemantic(hot), workSemantic(after)) {
		t.Fatalf("压缩损失工作语义\n%+v\n%+v", hot, after)
	}
	var ref kernel.ResultRef
	for _, e := range hot.Evidence {
		if e.Ref.Bytes > 16384 {
			ref = e.Ref
		}
	}
	if ref.ID == "" {
		t.Fatal("没有 >16 KiB 的原文引用")
	}
	h.hub.RestartIn(in.CharacterID, in.Conversation)
	h.cancel()
	h.hub.Idle()
	h.st.Close()
	start := time.Now()
	r := newHarnessOpts(t, harnessOpts{data: &h.data, wd: h.wd, mutate: mutate, replies: `[` + callReply("result_read", fmt.Sprintf(`{"id":%q,"offset":20000,"limit":20480}`, ref.ID)) + `,` + textReply + `]`})
	reopened := time.Since(start)
	approveWork(r)
	next := workInput(r, "接着核对证据尾部")
	wait(t, r.hub.Respond(next))
	if executed != 3 {
		t.Fatalf("恢复重放了工具：%d", executed)
	}
	if !strings.Contains(r.body(0), "保留证据") || !strings.Contains(r.body(0), "核对最终标记") || !strings.Contains(r.body(1), "made-evidence-tail") {
		t.Fatal("重开后的模型请求缺目标/计划/续读原文")
	}
	if strings.Contains(r.body(0), "reasoning_content") {
		t.Fatal("思考被恢复回灌")
	}
	cold, _, err := r.hub.Cfg.Work.Load(context.Background(), next.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if cold.Goal != hot.Goal || !reflect.DeepEqual(cold.Pending, hot.Pending) || len(cold.Unknown) != len(hot.Unknown) {
		t.Fatalf("重开语义不一致：%+v", cold)
	}
	var bytes int64
	files := 0
	entries, err := os.ReadDir(r.data.WorkResultsDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		files++
		bytes += info.Size()
	}
	t.Logf("K2-S1 hash=%s full_bytes=%d result_files=%d result_bytes=%d reopen_ms=%.3f executed=%d", ref.SHA256, ref.Bytes, files, bytes, float64(reopened.Microseconds())/1000, executed)
	t.Run("save-failed-unknown", func(t *testing.T) {
		executions := 0
		var broken *harness
		broken = newHarnessOpts(t, harnessOpts{replies: `[` + callReply("read", `{"path":"made.txt"}`) + `,` + textReply + `]`, mutate: func(cfg *Config) {
			workLimits(cfg)
			cfg.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				executions++
				_, err := broken.st.Trace.Exec(`CREATE TRIGGER made_fail_result BEFORE INSERT ON result_refs BEGIN SELECT RAISE(ABORT,'made evidence failure'); END`)
				return tools.Result{Text: "made executed evidence"}, err
			}
		}})
		input := workInput(broken, "记住失败后的未知结局")
		out := <-broken.hub.Respond(input)
		if out.Err == nil || !strings.Contains(out.Err.Error(), "证据保存失败") {
			t.Fatalf("保存失败没有如实返回：%v", out.Err)
		}
		port := broken.hub.Cfg.Work.(*workPort)
		state, _, err := port.Load(context.Background(), input.Identity)
		if err != nil {
			t.Fatal(err)
		}
		again, _, err := port.Load(context.Background(), input.Identity)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Unknown) != 1 || !reflect.DeepEqual(state, again) {
			t.Fatalf("unknown 丢失或重复：%+v %+v", state, again)
		}
		if _, err := broken.st.Trace.Exec(`DROP TRIGGER made_fail_result`); err != nil {
			t.Fatal(err)
		}
		if err := port.Record(context.Background(), input.Identity, Msg{Role: "user", Kind: KindSaid, Text: "后续输入留在尾部"}); err != nil {
			t.Fatal(err)
		}
		if ok, err := broken.hub.compactWork(context.Background(), input); !ok || err != nil {
			t.Fatalf("unknown 压缩失败：%t %v", ok, err)
		}
		head, err := port.service.DB.LoadWork(context.Background(), input.Identity)
		if err != nil || len(head.State.Unknown) != 1 {
			t.Fatalf("unknown 没有进入持久工作快照：%+v %v", head.State, err)
		}
		broken.hub.RestartIn(input.CharacterID, input.Conversation)
		wait(t, broken.hub.Respond(workInput(broken, "继续但不要重新执行")))
		if executions != 1 || !strings.Contains(broken.body(1), "legacy_unavailable") {
			t.Fatalf("恢复丢失未知或重放副作用：%d", executions)
		}
	})
}
func TestWorkNeverBecomesSharedChat(t *testing.T) {
	t.Parallel()
	secret := "made-local-secret-不得共享"
	h := newHarnessOpts(t, harnessOpts{replies: `[` + callReply("read", `{"path":"made.txt"}`) + `,` + textReply + `]`, mutate: func(cfg *Config) {
		workLimits(cfg)
		cfg.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
			return tools.Result{Text: secret}, nil
		}
	}})
	in := workInput(h, "查当前任务材料")
	wait(t, h.hub.Respond(in))
	if ok, err := h.hub.compactWork(context.Background(), in); !ok || err != nil {
		t.Fatalf("未压缩 %t %v", ok, err)
	}
	l := h.hub.live[liveKey(in)]
	for _, m := range l.compactSource(h.hub.Cfg).msgs {
		if strings.Contains(m.Text, secret) {
			t.Fatal("聊天摘要混入秘密工具结果")
		}
	}
	var events int
	if err := h.st.Events.QueryRow(`SELECT count(*) FROM events WHERE text LIKE ?`, "%"+secret+"%").Scan(&events); err != nil || events != 0 {
		t.Fatalf("记忆所读聊天库含工具正文 %d %v", events, err)
	}
	rows, err := h.st.Trace.Query(`SELECT id FROM result_refs`)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for rows.Next() {
		rows.Scan(&id)
	}
	rows.Close()
	for _, tc := range []struct{ name, channel, scene, conv, character string }{{"本地私聊另角色", "console", "private", in.Conversation, "made-other"}, {"本地群聊", "console", "group", "console:group:made-g", in.CharacterID}, {"QQ私聊", "qq", "private", "qq:private:made-bot:made-user", in.CharacterID}, {"QQ群", "qq", "group", "qq:group:made-bot:made-g", in.CharacterID}} {
		t.Run(tc.name, func(t *testing.T) {
			scope := in.Identity.Scope
			scope.Channel, scope.Scene, scope.Conversation, scope.CharacterID = tc.channel, tc.scene, tc.conv, tc.character
			if tc.channel == "qq" {
				scope.Platform = "qq"
			}
			args, _ := jsjson.Parse(fmt.Sprintf(`{"id":%q,"offset":0,"limit":1024}`, id))
			res, err := readResult(context.Background(), h.hub.Cfg.Results, scope, args)
			if err != nil || !res.IsError || strings.Contains(res.Text, secret) {
				t.Fatalf("跨场合泄漏：%+v %v", res, err)
			}
			bad := in.Identity
			bad.Scope = scope
			if _, _, err := h.hub.Cfg.Work.Load(context.Background(), bad); err == nil {
				t.Fatal("工作越权恢复未拒绝")
			}
		})
	}
	// 服务与模型入口拒绝同一批字段形状；参数不能伪造身份。
	for _, raw := range []string{`{}`, `{"id":1,"offset":0,"limit":1024}`, `{"id":"../x","offset":0,"limit":1024}`, fmt.Sprintf(`{"id":%q,"offset":0.5,"limit":1024}`, id), fmt.Sprintf(`{"id":%q,"offset":-1,"limit":1024}`, id), fmt.Sprintf(`{"id":%q,"offset":0,"limit":0}`, id)} {
		args, _ := jsjson.Parse(raw)
		res, err := readResult(context.Background(), h.hub.Cfg.Results, in.Identity.Scope, args)
		if err != nil || !res.IsError {
			t.Fatalf("坏参数被接受 %s %+v %v", raw, res, err)
		}
	}
}
