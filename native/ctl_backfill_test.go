package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// S02：独立实际客户端进程 → 测试专用命名管道 → 真宿主 / 协调器 / Intake → 临时库。
func TestCtlBackfillCommandsOverPipe(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("命令行走真管道的全流程，放在每日整套；命令本身有单测")
	}
	opt := logEnv(t)
	now := time.Now().Truncate(time.Second)
	opt.Now = func() time.Time { return now }
	opt.BackfillSettle = time.Millisecond // 默认等 30 秒防抖，这里的假协议端不抖
	var expanded atomic.Bool
	var queries, sends atomic.Int32
	var hold atomic.Bool
	manualGate := make(chan struct{})
	var release sync.Once
	protocol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		first, err := json.Marshal(map[string]any{"post_type": "meta_event", "meta_event_type": "lifecycle", "sub_type": "connect", "self_id": logTestAccount, "time": now.Unix()})
		if err != nil || c.Write(r.Context(), websocket.MessageText, first) != nil {
			return
		}
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				Action string `json:"action"`
				Echo   any    `json:"echo"`
			}
			if json.Unmarshal(data, &req) != nil {
				return
			}
			var body any
			switch req.Action {
			case "get_login_info":
				body = map[string]any{"user_id": logTestAccount}
			case "get_status":
				body = map[string]any{"online": true, "good": true}
			case "get_friend_msg_history":
				queries.Add(1)
				body = map[string]any{"messages": []any{}}
			case "get_group_msg_history":
				queries.Add(1)
				if hold.Load() {
					select {
					case <-manualGate:
					case <-r.Context().Done():
						return
					}
				}
				message := func(id string, age time.Duration) map[string]any {
					return map[string]any{"message_type": "group", "group_id": "300", "self_id": logTestAccount, "user_id": "200", "message_id": id, "time": now.Add(-age).Unix(), "sender": map[string]any{"user_id": "200", "nickname": "编造甲"}, "message": []any{map[string]any{"type": "text", "data": map[string]any{"text": "编造单段恢复"}}}}
				}
				messages := []any{message("1900", 10*time.Minute)}
				if expanded.Load() {
					messages = append(messages, message("1901", 15*time.Minute), message("1899", 25*time.Minute))
				}
				body = map[string]any{"messages": messages}
			default:
				sends.Add(1)
				continue
			}
			reply, _ := json.Marshal(map[string]any{"status": "ok", "retcode": 0, "echo": req.Echo, "data": body})
			if c.Write(r.Context(), websocket.MessageText, reply) != nil {
				return
			}
		}
	}))
	defer protocol.Close()
	defer release.Do(func() { close(manualGate) })
	cfgPath := filepath.Join(opt.DataDir, "config", "sumika.yml")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = bytes.ReplaceAll(cfg, []byte("ws://127.0.0.1:1"), []byte("ws"+strings.TrimPrefix(protocol.URL, "http")))
	if err = os.WriteFile(cfgPath, cfg, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := paths.Open(opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	qq, _ := set.For("qq")
	_, err = qq.AppendInbound(store.NewInbound{Event: store.NewEvent{Channel: "qq", Conversation: "qq:group:300", ActorKind: "human", ActorID: "qq:200", Kind: "message", Text: "编造已存右锚点", CreatedAt: now.Add(-10 * time.Minute).UnixMilli()}, PlatformID: "1900", Observers: []string{logTestChar}})
	if err != nil {
		t.Fatal(err)
	}
	if err = qq.RecordChannelGap(context.Background(), store.ChannelGap{Channel: "qq", Conversation: "qq:group:300", FromMS: now.Add(-20 * time.Minute).UnixMilli(), ToMS: now.Add(-10 * time.Minute).UnixMilli(), Reason: "outage", LastTryMS: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	gaps, err := qq.ChannelGaps(context.Background(), "qq")
	if err != nil {
		t.Fatal(err)
	}
	gap := gaps[0]
	gap.State = "abandoned"
	gap.Tries = 3
	if err = qq.UpdateChannelGap(context.Background(), gap); err != nil {
		t.Fatal(err)
	}
	if err = qq.SetQQCheckedThrough(context.Background(), logTestAccount, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	set.Close()
	exe := buildCtlTestClient(t)
	e := newCtlEnvWith(t, false, opt)
	e.sh.host.start()
	e.sh.host.startReceive()
	commands := 0
	rawCommand := func(args ...string) (int, []byte) {
		t.Helper()
		commands++
		cmd := exec.Command(exe, args...)
		cmd.Env = append(os.Environ(), "SUMIKA_CTL_TEST_PIPE="+e.name)
		output, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		return code, output
	}
	command := func(args ...string) (ctlproto.Reply, int, string) {
		t.Helper()
		code, output := rawCommand(args...)
		var reply ctlproto.Reply
		if json.Unmarshal(output, &reply) != nil {
			t.Fatalf("客户端输出不是 JSON code=%d %s", code, output)
		}
		return reply, code, string(output)
	}
	waitStatus := func(predicate func(ctlproto.BackfillStatus) bool) ctlproto.BackfillStatus {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for {
			r := e.ask(t, ctlproto.Request{Cmd: "backfill", Args: []string{"status"}})
			if r.OK {
				v := dataAs[ctlproto.BackfillStatus](t, r)
				if predicate(v) {
					return v
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("对齐状态未到预期 %+v", r)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitStatus(func(v ctlproto.BackfillStatus) bool { return v.LastAttempt != nil && v.CurrentJob == nil })
	expanded.Store(true)
	r, code, out := command("backfill", "gaps", "-n", "1", "--json")
	if code != 0 || !r.OK {
		t.Fatalf("gaps code=%d %s", code, out)
	}
	list := dataAs[ctlproto.BackfillGaps](t, r)
	if len(list.Items) != 1 || !list.Items[0].CanRetry || !ctlproto.IsHandle(list.Items[0].GapHandle) {
		t.Fatal(list)
	}
	hold.Store(true)
	// 规格的快反馈是接受请求到回包；每次仍新连真实管道，覆盖 SID 核对和完整宿主处理。
	retry := ctlproto.Request{Cmd: "backfill", Args: []string{"retry", list.Items[0].GapHandle}}
	acceptedAt := time.Now()
	r = e.ask(t, retry)
	feedback := time.Since(acceptedAt)
	if !r.OK {
		t.Fatalf("retry %+v", r)
	}
	accepted := dataAs[ctlproto.BackfillAccepted](t, r)
	if !accepted.Accepted || accepted.Scope != "gap" || accepted.JobID == "" {
		t.Fatal(accepted)
	}
	feedbacks := make([]time.Duration, 200)
	feedbacks[0] = feedback
	for i := 1; i < len(feedbacks); i++ {
		start := time.Now()
		reply := e.ask(t, retry)
		feedbacks[i] = time.Since(start)
		if !reply.OK || dataAs[ctlproto.BackfillAccepted](t, reply).JobID != accepted.JobID {
			t.Fatal("重复接受没有合并", reply)
		}
	}
	// 独立 CLI 进程另验参数、输出和退出码，不能把 Windows 启动 / 退出调度混进后台反馈预算。
	r, code, out = command("backfill", "retry", list.Items[0].GapHandle, "--json")
	if code != 0 || !r.OK || dataAs[ctlproto.BackfillAccepted](t, r).JobID != accepted.JobID {
		t.Fatalf("实际客户端 retry code=%d %s", code, out)
	}
	release.Do(func() { close(manualGate) })
	sort.Slice(feedbacks, func(i, j int) bool { return feedbacks[i] < feedbacks[j] })
	// 耗时门槛是性能验收，机器忙时会误红：-short（合并前、推送前）只记数，整套（每日巡检、--full）才判红（ADR-225 测试分两档）
	t.Logf("反馈 P95=%v P99=%v", feedbacks[189], feedbacks[197])
	if !testing.Short() && (feedbacks[189] > 100*time.Millisecond || feedbacks[197] > 200*time.Millisecond) {
		t.Fatalf("反馈超预算 P95=%v P99=%v", feedbacks[189], feedbacks[197])
	}
	v := waitStatus(func(v ctlproto.BackfillStatus) bool {
		return v.LastAttempt != nil && v.LastAttempt.ID == accepted.JobID && v.LastAttempt.State == "completed"
	})
	if v.LastAttempt.CompleteCoverage || v.LastAttempt.Inserted != 1 || v.LastAttempt.Duplicates != 1 {
		t.Fatal("补录计数或覆盖不真实", v)
	}
	reader := e.sh.host.current().Reader
	page, err := reader.ReadMessages(context.Background(), api.ConversationRequest{Key: api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:300"}, Limit: 100}, "")
	if err != nil || len(page.Value.Items) != 2 {
		t.Fatalf("实际消息未入库 %+v %v", page, err)
	}
	textCode, text := rawCommand("backfill", "status")
	if textCode != 0 {
		t.Fatalf("实际中文状态退出码 %d：%s", textCode, text)
	}
	t.Logf("S04 actual-json inserted=%d duplicates=%d open=%d abandoned=%d; actual-text=%s", v.LastAttempt.Inserted, v.LastAttempt.Duplicates, v.RemainingOpen, v.RemainingAbandoned, text)
	r, code, out = command("backfill", "gaps", "--json")
	if code != 0 || !r.OK {
		t.Fatal("gaps", code, out)
	}
	var retained *ctlproto.BackfillGap
	items := dataAs[ctlproto.BackfillGaps](t, r).Items
	for i := range items {
		if items[i].FromMS == gap.FromMS && items[i].ToMS == gap.ToMS && items[i].MaskedConversation == list.Items[0].MaskedConversation {
			retained = &items[i]
			break
		}
	}
	if retained == nil || retained.State != "abandoned" {
		t.Fatal("没有连续证明却清除了原缺口", out)
	}
	r, code, out = command("backfill", "run", "--json")
	if code != 0 || !r.OK {
		t.Fatal("run", code, out)
	}
	account := dataAs[ctlproto.BackfillAccepted](t, r)
	waitStatus(func(v ctlproto.BackfillStatus) bool {
		return v.LastAttempt != nil && v.LastAttempt.ID == account.JobID && v.LastAttempt.State == "completed"
	})
	r, code, out = command("backfill", "status", "--json")
	if code != 0 || !r.OK {
		t.Fatal("status", code, out)
	}
	status := dataAs[ctlproto.BackfillStatus](t, r)
	if status.RemainingOpen+status.RemainingAbandoned == 0 {
		t.Fatal("无连续证明的账号补录清空了缺口", out)
	}
	if sends.Load() != 0 {
		t.Fatal("补录触发出站", sends.Load())
	}
	file, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	// 只读后依旧可列状态，但 run 不会暗中开始接收。
	e.sh.host.stopReceive()
	r, code, out = command("backfill", "run", "--json")
	if code != 1 || r.Code != "readonly" {
		t.Fatal("archive写动作", code, out)
	}
	if e.sh.host.current().Phase != "archive" {
		t.Fatal("补录暗中开始接收")
	}
	t.Logf("S02 commands=%d historyRequests=%d pipeSamples=%d pipeP95=%v pipeP99=%v pipeMax=%v testClientBytes=%d", commands, queries.Load(), len(feedbacks), feedbacks[189], feedbacks[197], feedbacks[199], file.Size())
}

// 两个管道验收共用正式客户端；overlay 只替换测试管道，不给产品加入环境变量入口。
func buildCtlTestClient(t *testing.T) string {
	t.Helper()
	fixtureDir := t.TempDir()
	exe := filepath.Join(fixtureDir, "sumika-ctl.exe")
	mainFile, err := filepath.Abs(filepath.Join("cmd", "sumika-ctl", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	fixtureMain := filepath.Join(fixtureDir, "main.go")
	const mainSource = `package main
	import (
		"context"
		"net"
		"os"
		"sumika-native/internal/ctlproto"
	)
	func main() {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, func(ctx context.Context, _ bool) (net.Conn, error) {
			return ctlproto.Dial(ctx, os.Getenv("SUMIKA_CTL_TEST_PIPE"))
		}))
	}
	`
	if err := os.WriteFile(fixtureMain, []byte(mainSource), 0600); err != nil {
		t.Fatal(err)
	}
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{mainFile: fixtureMain}})
	if err != nil {
		t.Fatal(err)
	}
	overlayFile := filepath.Join(fixtureDir, "overlay.json")
	if err := os.WriteFile(overlayFile, overlay, 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-overlay="+overlayFile, "-o", exe, "./cmd/sumika-ctl")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("编译客户端 %v %s", err, output)
	}
	return exe
}
