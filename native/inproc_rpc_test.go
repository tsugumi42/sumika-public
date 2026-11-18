package main

// 主窗口默认经过协议客户端（规格-Bun后台第2批 第四、八、九节）：
//  1. 桥必须自己声明那 63 个方法（Reader 的 8 个加第 2 批的 35 个、本地群聊 3 个、悄悄话 5 个、目标、定期任务、提醒 12 个），不能靠内嵌的 *DesktopReader 提升后直接调；
//  2. 逐个调用，抓管道上发出的协议请求，确认每个方法都走了客户端；
//  3. 真的后台（临时数据目录、假网关，不调真模型）上跑一轮：发送、审批、停止、插话，哨兵错误经桥后 errors.Is 仍成立。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/rpcclient"
	"sumika-native/internal/rpcwire"
	"sumika-native/internal/turn"
)

// bridgeInterfaces 是桥要自己声明方法的 23 个接口（api.Reader 加 22 个可选接口）
func bridgeInterfaces() []reflect.Type {
	return []reflect.Type{
		reflect.TypeFor[api.Reader](), reflect.TypeFor[api.AvatarReader](), reflect.TypeFor[api.ForwardReader](), reflect.TypeFor[api.ReadMarker](),
		reflect.TypeFor[api.VideoReader](), reflect.TypeFor[api.LocalChat](), reflect.TypeFor[api.LocalImageChat](), reflect.TypeFor[api.LocalImageSaver](),
		reflect.TypeFor[api.LocalPartsChat](), reflect.TypeFor[api.LocalGroupWriter](), reflect.TypeFor[api.WhisperChat](), reflect.TypeFor[api.WhisperLogReader](), reflect.TypeFor[api.LocalStatusReader](), reflect.TypeFor[api.LoopControl](), reflect.TypeFor[api.ReminderControl](), reflect.TypeFor[api.LocalInputs](), reflect.TypeFor[api.LocalHeld](), reflect.TypeFor[api.LocalQuestions](),
		reflect.TypeFor[api.ApprovalRules](), reflect.TypeFor[api.ApprovalRuleEditor](), reflect.TypeFor[api.ConversationApproval](),
		reflect.TypeFor[api.LocalWorkspaces](), reflect.TypeFor[api.CheckpointActions](),
	}
}

func bridgeMethodNames() []string {
	var names []string
	for _, it := range bridgeInterfaces() {
		for i := 0; i < it.NumMethod(); i++ {
			names = append(names, it.Method(i).Name)
		}
	}
	sort.Strings(names)
	return names
}

// 用源码读出桥自己声明的方法：反射分不清"自己声明的"和"内嵌提升来的"
func declaredOnBridge(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 {
					continue
				}
				rt := fd.Recv.List[0].Type
				if star, ok := rt.(*ast.StarExpr); ok {
					rt = star.X
				}
				if id, ok := rt.(*ast.Ident); ok && id.Name == "rpcBridge" {
					out[fd.Name.Name] = true
				}
			}
		}
	}
	return out
}

func TestBridgeDeclaresEveryMethod(t *testing.T) {
	t.Parallel()
	names := bridgeMethodNames()
	if len(names) != 63 {
		t.Fatalf("接口方法数 = %d，应是 63（Reader 8 + 第 2 批 35 + 本地群聊 3 + 悄悄话 5 + 目标、定期任务、提醒 12）：接口变了就要补桥", len(names))
	}
	declared := declaredOnBridge(t)
	for _, n := range names {
		if !declared[n] {
			t.Errorf("rpcBridge 没有自己声明 %s：不能靠内嵌的 DesktopReader 提升后直接调", n)
		}
	}
}

// ───── 假后台 ─────

const (
	bAccount = "100"
	bOwner   = "200"
	bGroup   = "300"
	bModel   = "编的模型"
	bChar    = "example"
	bChar2   = "second" // 本地群聊至少要两位角色
)

func bridgeKey() api.ConversationKey {
	return api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:" + bChar}
}

func bridgeGroupKey() api.ConversationKey {
	return api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: "console:group:不存在的群"}
}

type bridgeEnv struct {
	opt  backend.Options
	work string
	mu   sync.Mutex
	body []string
}

// newBridgeEnv：临时数据目录 + 示例卡 + 假网关；模型一律是 llmtest，不连任何真服务
func newBridgeEnv(t *testing.T, script llmtest.Script) *bridgeEnv {
	t.Helper()
	root, err := config.FindRoot("..")
	if err != nil {
		t.Fatalf("找项目根：%v", err)
	}
	data := t.TempDir()
	if _, err := paths.Init(data); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{bChar, bChar2} {
		cardDst := filepath.Join(data, "characters", id, "card")
		if err := os.MkdirAll(cardDst, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"character.yml", "character.md"} {
			b, err := os.ReadFile(filepath.Join(root, "characters", "example", name))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cardDst, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	e := &bridgeEnv{work: t.TempDir()}
	script.Models = []json.RawMessage{json.RawMessage(`{"id":"` + bModel + `","context_length":32768}`)}
	srv := httptest.NewServer(llmtest.Handler(script, &e.body, &e.mu))
	t.Cleanup(srv.Close)
	// freeApi 探测关掉：它用自己的 HTTP 客户端，开着会真连外网（2026-11-12）
	cfg := fmt.Sprintf(`magpie:
  baseUrl: http://127.0.0.1:1/v1
freeApi:
  probe:
    enabled: false
qq:
  owner: %s
  accounts:
    - id: %s
      character: %s
      url: "ws://127.0.0.1:1"
      token: "编的令牌"
  groups:
    - %s
  privateAllow: []
group:
  frequency: 0.3
  jitter: 0
  mode: dry
characters:
  %s:
    model: %s
  %s:
    model: %s
embed:
  enabled: false
agent:
  recentMessages: 5
tools:
  workdir: '%s'
`, bOwner, bAccount, bChar, bGroup, bChar, bModel, bChar2, bModel, filepath.ToSlash(e.work))
	if err := os.MkdirAll(filepath.Join(data, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "config", "sumika.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	analyzer := testAnalyzer()
	e.opt = backend.Options{Root: root, DataDir: data, Mode: backend.Receive, Account: bAccount, Logf: t.Logf,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		},
		SharedAnalyzer: analyzer, LLM: llm.New(llm.Magpie(srv.URL)),
		ModelRetry: turn.RetryPolicy{Base: 5 * time.Millisecond, Cap: 20 * time.Millisecond}}
	return e
}

// open 开后台（只装配，不连网）和它的桌面入口，再套一座桥
func (e *bridgeEnv) open(t *testing.T, wrap func(rpcclient.Transport) rpcclient.Transport) (*backend.Desktop, *rpcBridge) {
	t.Helper()
	a, err := backend.Open(context.Background(), e.opt)
	if err != nil {
		t.Fatalf("Open：%v", err)
	}
	d := a.Desktop()
	b, err := newRPCBridge(context.Background(), &d.Reader, wrap, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close(); d.Close(); a.Close() })
	return d, b
}

type countTransport struct {
	rpcclient.Transport
	mu   sync.Mutex
	seen map[string]int
}

func (c *countTransport) Write(ctx context.Context, b []byte) error {
	var m rpcwire.Message
	if json.Unmarshal(b, &m) == nil && m.Method != "" {
		c.mu.Lock()
		c.seen[m.Method]++
		c.mu.Unlock()
	}
	return c.Transport.Write(ctx, b)
}

func (c *countTransport) count(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[method]
}

func localCtx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return c
}

func recvTimeout[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(20 * time.Second):
		t.Fatal("20 秒内没等到")
	}
	var z T
	return z
}

// ───── 63 个方法逐个走客户端 ─────

type bridgeRow struct {
	name, wire string
	call       func(ctx context.Context, b *rpcBridge)
}

func closeSub[T any](sub api.Subscription[T], err error) {
	if err == nil && sub != nil {
		sub.Close()
	}
}

func bridgeRows() []bridgeRow {
	k := bridgeKey()
	ref := api.MediaRef{Hash: strings.Repeat("a", 64)}
	return []bridgeRow{
		{"WatchOverview", rpcwire.MethodOverviewSubscribe, func(c context.Context, b *rpcBridge) {
			closeSub(b.WatchOverview(c, api.OverviewRequest{}))
		}},
		{"ReadMessages", rpcwire.MethodMessagesRead, func(c context.Context, b *rpcBridge) {
			b.ReadMessages(c, api.ConversationRequest{Key: k, Limit: 5}, "")
		}},
		{"WatchConversation", rpcwire.MethodConversationSubscribe, func(c context.Context, b *rpcBridge) {
			closeSub(b.WatchConversation(c, api.ConversationRequest{Key: k, Limit: 5}))
		}},
		{"ReadMedia", rpcwire.MethodMediaRead, func(c context.Context, b *rpcBridge) { b.ReadMedia(c, api.MediaRef{}, 0) }},
		{"ReadFaceIndex", rpcwire.MethodFacesIndex, func(c context.Context, b *rpcBridge) { b.ReadFaceIndex(c) }},
		{"ReadFacePNG", rpcwire.MethodFacesPNG, func(c context.Context, b *rpcBridge) { b.ReadFacePNG(c, "不存在") }},
		{"ReadSettings", rpcwire.MethodSettingsRead, func(c context.Context, b *rpcBridge) { b.ReadSettings(c) }},
		{"WatchRuntime", rpcwire.MethodRuntimeSubscribe, func(c context.Context, b *rpcBridge) { closeSub(b.WatchRuntime(c)) }},

		{"ReadAvatar", rpcwire.MethodAvatarRead, func(c context.Context, b *rpcBridge) { b.ReadAvatar(c, "user:12345") }},
		{"ReadForward", rpcwire.MethodForwardRead, func(c context.Context, b *rpcBridge) { b.ReadForward(c, api.ForwardRequest{}) }},
		{"MarkRead", rpcwire.MethodReadMarkSet, func(c context.Context, b *rpcBridge) { b.MarkRead(c, k, 1) }},
		{"PrepareVideo", rpcwire.MethodVideoPrepare, func(c context.Context, b *rpcBridge) { b.PrepareVideo(c, ref) }},

		{"SendLocal", rpcwire.MethodLocalSend, func(c context.Context, b *rpcBridge) { b.SendLocal(c, k, "", nil) }}, // 空话被后台拒收，不开一轮
		{"StopLocal", rpcwire.MethodLocalStop, func(c context.Context, b *rpcBridge) { b.StopLocal(c, k) }},
		{"WatchLocalApprovals", rpcwire.MethodLocalApprovalsSubscribe, func(c context.Context, b *rpcBridge) { closeSub(b.WatchLocalApprovals(c)) }},
		{"DecideLocal", rpcwire.MethodLocalDecide, func(c context.Context, b *rpcBridge) { b.DecideLocal(c, k, "没有这张卡", api.ApproveOnce) }},
		{"SendLocalImages", rpcwire.MethodLocalSendImages, func(c context.Context, b *rpcBridge) { b.SendLocalImages(c, k, "", nil, nil) }},
		{"SaveLocalImage", rpcwire.MethodLocalSaveImage, func(c context.Context, b *rpcBridge) { b.SaveLocalImage(c, k, []byte("不是 PNG")) }},
		{"SendLocalParts", rpcwire.MethodLocalSendParts, func(c context.Context, b *rpcBridge) { b.SendLocalParts(c, k, nil, nil) }},
		{"CreateLocalGroup", rpcwire.MethodLocalGroupCreate, func(c context.Context, b *rpcBridge) {
			b.CreateLocalGroup(c, api.LocalGroupCreate{Name: " ", Characters: nil}) // 空群名被后台拒收，不建群
		}},
		{"SendLocalGroup", rpcwire.MethodLocalGroupSend, func(c context.Context, b *rpcBridge) {
			b.SendLocalGroup(c, api.LocalGroupSend{Key: bridgeGroupKey(), Text: ""}) // 空话被后台拒收，不开一轮
		}},
		{"StopLocalGroup", rpcwire.MethodLocalGroupStop, func(c context.Context, b *rpcBridge) { b.StopLocalGroup(c, bridgeGroupKey()) }},
		{"SendWhisper", rpcwire.MethodWhisperSend, func(c context.Context, b *rpcBridge) { b.SendWhisper(c, bridgeGroupKey(), nil) }}, // 没有块被后台拒收，不叫醒任何人
		{"ReadWhisperLog", rpcwire.MethodWhisperLogRead, func(c context.Context, b *rpcBridge) { b.ReadWhisperLog(c, bridgeGroupKey(), "", 0) }},
		{"ReadWhisperLogEntry", rpcwire.MethodWhisperLogEntry, func(c context.Context, b *rpcBridge) { b.ReadWhisperLogEntry(c, bridgeGroupKey(), "say:1", false) }},
		{"WatchWhisperLog", rpcwire.MethodWhisperLogSubscribe, func(c context.Context, b *rpcBridge) { closeSub(b.WatchWhisperLog(c, bridgeGroupKey())) }},
		{"CancelWhisper", rpcwire.MethodWhisperLogCancel, func(c context.Context, b *rpcBridge) { b.CancelWhisper(c, bridgeGroupKey(), "没有", "没有") }},
		{"ReadLocalStatus", rpcwire.MethodLocalStatusRead, func(c context.Context, b *rpcBridge) { b.ReadLocalStatus(c, k) }},
		{"WatchLocalStatus", rpcwire.MethodLocalStatusSubscribe, func(c context.Context, b *rpcBridge) { closeSub(b.WatchLocalStatus(c)) }},
		{"AddLoop", rpcwire.MethodLoopAdd, func(c context.Context, b *rpcBridge) { b.AddLoop(c, k, api.LoopSpec{Kind: "self"}) }}, // 空任务被后台拒收，不建任务
		{"EditLoop", rpcwire.MethodLoopEdit, func(c context.Context, b *rpcBridge) { b.EditLoop(c, k, 999, api.LoopSpec{Prompt: "x", Kind: "self"}) }},
		{"PauseLoop", rpcwire.MethodLoopPause, func(c context.Context, b *rpcBridge) { b.PauseLoop(c, k, 999) }},
		{"ResumeLoop", rpcwire.MethodLoopResume, func(c context.Context, b *rpcBridge) { b.ResumeLoop(c, k, 999) }},
		{"RemoveLoop", rpcwire.MethodLoopRemove, func(c context.Context, b *rpcBridge) { b.RemoveLoop(c, k, 999) }},
		{"ReminderOptions", rpcwire.MethodReminderOptions, func(c context.Context, b *rpcBridge) { b.ReminderOptions() }},
		{"CreateReminder", rpcwire.MethodReminderCreate, func(c context.Context, b *rpcBridge) { b.CreateReminder(c, k, api.ReminderDraft{}) }}, // 空提醒被后台拒收，不建
		{"UpdateReminder", rpcwire.MethodReminderUpdate, func(c context.Context, b *rpcBridge) { b.UpdateReminder(c, k, 999, api.ReminderPatch{}) }},
		{"FinishReminder", rpcwire.MethodReminderFinish, func(c context.Context, b *rpcBridge) { b.FinishReminder(c, k, 999, true) }},
		{"DeleteReminder", rpcwire.MethodReminderDelete, func(c context.Context, b *rpcBridge) { b.DeleteReminder(c, k, 999) }},
		{"SubmitLocal", rpcwire.MethodLocalSubmit, func(c context.Context, b *rpcBridge) { b.SubmitLocal(c, k, api.LocalInputSubmission{}) }},
		{"StopLocalRun", rpcwire.MethodLocalStopRun, func(c context.Context, b *rpcBridge) { b.StopLocalRun(c, k, "没有这一轮", 0) }},
		{"WatchLocalRun", rpcwire.MethodLocalRunSubscribe, func(c context.Context, b *rpcBridge) { closeSub(b.WatchLocalRun(c, k, "没有这一轮")) }},
		{"HeldLocal", rpcwire.MethodHeldList, func(c context.Context, b *rpcBridge) { b.HeldLocal(c) }},
		{"ReleaseHeldLocal", rpcwire.MethodHeldRelease, func(c context.Context, b *rpcBridge) { b.ReleaseHeldLocal(c, k, "没有") }},
		{"DiscardHeldLocal", rpcwire.MethodHeldDiscard, func(c context.Context, b *rpcBridge) { b.DiscardHeldLocal(c, k, "没有") }},
		{"WithdrawQueuedLocal", rpcwire.MethodLocalWithdraw, func(c context.Context, b *rpcBridge) { b.WithdrawQueuedLocal(c, k, "没有") }},
		{"WatchLocalQuestions", rpcwire.MethodLocalQuestionsSubscribe, func(c context.Context, b *rpcBridge) { closeSub(b.WatchLocalQuestions(c)) }},
		{"AnswerLocal", rpcwire.MethodLocalAnswer, func(c context.Context, b *rpcBridge) { b.AnswerLocal(c, k, "没有", 0) }},
		{"ListApprovalRules", rpcwire.MethodApprovalRulesList, func(c context.Context, b *rpcBridge) { b.ListApprovalRules(c) }},
		{"RevokeApprovalRule", rpcwire.MethodApprovalRulesRevoke, func(c context.Context, b *rpcBridge) { b.RevokeApprovalRule(c, api.ApprovalRule{}) }},
		{"AddApprovalRule", rpcwire.MethodApprovalRulesAdd, func(c context.Context, b *rpcBridge) { b.AddApprovalRule(c, "", "allow", "", "") }},
		{"ConversationApprovalMode", rpcwire.MethodConversationApprovalRead, func(c context.Context, b *rpcBridge) { b.ConversationApprovalMode(c, k) }},
		{"SetConversationApprovalMode", rpcwire.MethodConversationApprovalSet, func(c context.Context, b *rpcBridge) { b.SetConversationApprovalMode(c, k, "不是档位") }},
		{"ReadLocalWorkspace", rpcwire.MethodWorkspaceRead, func(c context.Context, b *rpcBridge) { b.ReadLocalWorkspace(c, k) }},
		{"SetLocalWorkspace", rpcwire.MethodWorkspaceSet, func(c context.Context, b *rpcBridge) { b.SetLocalWorkspace(c, k, "相对路径") }},
		{"ClearLocalWorkspace", rpcwire.MethodWorkspaceClear, func(c context.Context, b *rpcBridge) { b.ClearLocalWorkspace(c, k) }},
		{"RecentWorkspaces", rpcwire.MethodWorkspaceRecent, func(c context.Context, b *rpcBridge) { b.RecentWorkspaces(c) }},
		{"ListCheckpoints", rpcwire.MethodCheckpointsList, func(c context.Context, b *rpcBridge) { b.ListCheckpoints(c, k) }},
		{"PreviewCheckpoint", rpcwire.MethodCheckpointsPreview, func(c context.Context, b *rpcBridge) { b.PreviewCheckpoint(c, k, "没有", "all") }},
		{"RestoreCheckpoint", rpcwire.MethodCheckpointsRestore, func(c context.Context, b *rpcBridge) { b.RestoreCheckpoint(c, k, "没有", "all", "v") }},
		{"ReadCheckpointProgress", rpcwire.MethodCheckpointsProgress, func(c context.Context, b *rpcBridge) { b.ReadCheckpointProgress(c, k, "没有") }},
		{"WatchCheckpointProgress", rpcwire.MethodCheckpointProgressSubscribe, func(c context.Context, b *rpcBridge) {
			closeSub(b.WatchCheckpointProgress(c, k, "没有"))
		}},
		{"CancelCheckpointRestore", rpcwire.MethodCheckpointsCancel, func(c context.Context, b *rpcBridge) { b.CancelCheckpointRestore(c, k, "没有") }},
	}
}

func TestBridgeRoutesEveryMethodThroughClient(t *testing.T) {
	t.Parallel()
	rows := bridgeRows()
	// 表要和接口方法一一对上：接口多出的方法没有行，就是漏测
	var tableNames []string
	for _, r := range rows {
		tableNames = append(tableNames, r.name)
	}
	sort.Strings(tableNames)
	if want := bridgeMethodNames(); !reflect.DeepEqual(tableNames, want) {
		t.Fatalf("测试表和接口方法对不上\n表   %v\n接口 %v", tableNames, want)
	}
	e := newBridgeEnv(t, llmtest.Script{Reply: llmtest.Text("嗯")})
	var ct *countTransport
	_, b := e.open(t, func(tr rpcclient.Transport) rpcclient.Transport {
		ct = &countTransport{Transport: tr, seen: map[string]int{}}
		return ct
	})
	for _, r := range rows {
		before := ct.count(r.wire)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		r.call(ctx, b)
		cancel()
		if ct.count(r.wire) != before+1 {
			t.Errorf("%s 没有发出协议请求 %s（没走客户端？）", r.name, r.wire)
		}
	}
}

// ───── 真的后台上跑一轮 ─────

func hasChoice(l []api.ApprovalChoice, c api.ApprovalChoice) bool {
	for _, x := range l {
		if x == c {
			return true
		}
	}
	return false
}

// 发送、草稿、审批、批准：经桥和直接调结果一致；批准前文件不存在，批准后写进去
func TestBridgeSendApproveMatchesDirect(t *testing.T) {
	t.Parallel()
	target := "编的文件.txt"
	args, _ := json.Marshal(map[string]string{"purpose": "写测试文件", "path": target, "content": "获批才写"})
	e := newBridgeEnv(t, llmtest.Script{Replies: []llmtest.Reply{
		llmtest.Call("b-write", "write", string(args)), llmtest.Text("经桥写好了"),
		llmtest.Text("直接调的回复"),
	}})
	d, b := e.open(t, nil)
	ctx := localCtx(t)
	key := bridgeKey()

	sub, err := b.WatchLocalApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	drafts := make(chan api.LocalDraft, 64)
	done := make(chan error, 1)
	go func() { done <- b.SendLocal(ctx, key, "写编的文件", func(v api.LocalDraft) { drafts <- v }) }()
	var card api.LocalApproval
	for card.ID == "" {
		snap := recvTimeout(t, sub.Updates())
		if len(snap.Value) > 0 {
			card = snap.Value[0]
		}
	}
	if card.Key != key || card.CharacterID != bChar || card.Tool != "write" || !strings.Contains(card.Subject, target) {
		t.Fatalf("经桥看到的审批卡不对：%+v", card)
	}
	if !hasChoice(card.Options, api.ApproveConversation) || !hasChoice(card.Options, api.ApproveAlways) {
		t.Fatalf("审批卡的档位丢了：%v", card.Options)
	}
	// 和直接调看到的是同一张卡
	dsub, err := d.Reader.WatchLocalApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer dsub.Close()
	var direct api.LocalApproval
	for direct.ID == "" {
		snap := recvTimeout(t, dsub.Updates())
		if len(snap.Value) > 0 {
			direct = snap.Value[0]
		}
	}
	if !reflect.DeepEqual(direct, card) {
		t.Fatalf("经桥和直接看到的审批卡不一样\n直接 %#v\n经桥 %#v", direct, card)
	}
	full := filepath.Join(e.work, target)
	if _, err := os.Stat(full); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("批准前已经执行：%v", err)
	}
	// 别的会话（不存在的话题）的批准：直接和经桥都拒，文字一样（这个错误不在协议的哨兵表里，经桥是 1002 普通错误）
	other := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: key.Conversation + ":topic:另一个"}
	dErr := d.Reader.DecideLocal(ctx, other, card.ID, api.ApproveOnce)
	bErr := b.DecideLocal(ctx, other, card.ID, api.ApproveOnce)
	if dErr == nil || bErr == nil || dErr.Error() != bErr.Error() {
		t.Fatalf("跨会话批准：直接 %v 经桥 %v", dErr, bErr)
	}
	if err := b.DecideLocal(ctx, key, card.ID, api.ApproveOnce); err != nil {
		t.Fatal(err)
	}
	if err := recvTimeout(t, done); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(full); err != nil || string(got) != "获批才写" {
		t.Fatalf("批准没执行：%q %v", got, err)
	}
	if err := b.DecideLocal(ctx, key, card.ID, api.ApproveOnce); !errors.Is(err, api.ErrApprovalGone) {
		t.Fatalf("迟到的批准：%v", err)
	}
	// 草稿经桥到了回调，最后一条是"这一步存好了"（Text 为空）
	var got []api.LocalDraft
	for len(drafts) > 0 {
		got = append(got, <-drafts)
	}
	if len(got) == 0 || got[len(got)-1].Text != "" || got[0].TurnID == "" {
		t.Fatalf("草稿没经桥送到：%+v", got)
	}
	// 落库的消息：直接读和经桥读一样
	req := api.ConversationRequest{Key: key, Limit: 20}
	dp, dErr2 := d.Reader.ReadMessages(ctx, req, "")
	bp, bErr2 := b.ReadMessages(ctx, req, "")
	if dErr2 != nil || bErr2 != nil {
		t.Fatal(dErr2, bErr2)
	}
	if len(bp.Value.Items) != len(dp.Value.Items) || len(bp.Value.Items) < 2 {
		t.Fatalf("消息条数不一样：直接 %d 经桥 %d", len(dp.Value.Items), len(bp.Value.Items))
	}
	for i := range dp.Value.Items {
		if dp.Value.Items[i].Text != bp.Value.Items[i].Text || dp.Value.Items[i].ActorKind != bp.Value.Items[i].ActorKind {
			t.Fatalf("第 %d 条不一样：%+v %+v", i, dp.Value.Items[i], bp.Value.Items[i])
		}
	}
	// 直接调再发一条：同一个后台，经桥和直接互不影响
	if err := d.Reader.SendLocal(ctx, key, "直接发", nil); err != nil {
		t.Fatal(err)
	}
}

// 停止：一轮停在半句上，经桥 StopLocal 停掉；同一会话重复发送得到 ErrLocalChatBusy
func TestBridgeStopAndBusy(t *testing.T) {
	t.Parallel()
	e := newBridgeEnv(t, llmtest.Script{Reply: llmtest.Text("前半句", "后半句"), Hold: func(int) int { return 1 }})
	d, b := e.open(t, nil)
	ctx := localCtx(t)
	key := bridgeKey()
	drafts := make(chan api.LocalDraft, 16)
	done := make(chan error, 1)
	go func() { done <- b.SendLocal(ctx, key, "第一条", func(v api.LocalDraft) { drafts <- v }) }()
	if first := recvTimeout(t, drafts); first.Text != "前半句" {
		t.Fatalf("第一条草稿：%+v", first)
	}
	dErr := d.Reader.SendLocal(ctx, key, "重复发送", nil)
	bErr := b.SendLocal(ctx, key, "重复发送", nil)
	if !errors.Is(dErr, api.ErrLocalChatBusy) || !errors.Is(bErr, api.ErrLocalChatBusy) || dErr.Error() != bErr.Error() {
		t.Fatalf("重复发送：直接 %v 经桥 %v", dErr, bErr)
	}
	if err := b.StopLocal(ctx, key); err != nil {
		t.Fatal(err)
	}
	recvTimeout(t, done) // 停止后这一次发送返回（停掉不算错）
}

// 本地群聊经桥：建群、点名发送（草稿、落库）、同群重复发送报忙、停止；哨兵经桥 errors.Is 仍成立
func TestBridgeLocalGroupCreateSendBusyStop(t *testing.T) {
	t.Parallel()
	e := newBridgeEnv(t, llmtest.Script{Reply: llmtest.Text("前半句", "后半句"), Hold: func(int) int { return 1 }})
	d, b := e.open(t, nil)
	ctx := localCtx(t)

	// 建群走协议：坏输入被后台拒收，不留半个群
	for _, bad := range []api.LocalGroupCreate{{Name: " ", Characters: []string{bChar, bChar2}}, {Name: "x", Characters: []string{bChar}}, {Name: "x", Characters: []string{bChar, "不存在"}}} {
		if _, err := b.CreateLocalGroup(ctx, bad); err == nil {
			t.Fatalf("坏输入建群成功：%+v", bad)
		}
	}
	v, err := b.CreateLocalGroup(ctx, api.LocalGroupCreate{Name: "编造的小屋", Characters: []string{bChar, bChar2}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "编造的小屋" || v.Key.Scene != "group" || len(v.Observers) != 2 {
		t.Fatalf("建群结果经桥不对：%+v", v)
	}
	gk := v.Key

	drafts := make(chan api.LocalDraft, 32)
	done := make(chan error, 1)
	go func() {
		done <- b.SendLocalGroup(ctx, api.LocalGroupSend{Key: gk, Text: "大家好", Mentions: []string{bChar},
			Draft: func(v api.LocalDraft) { drafts <- v }, Process: func(string, []api.LocalProcessStep) {}})
	}()
	start := recvTimeout(t, drafts)
	if start.Step != -1 || start.CharacterID != bChar || start.TurnID != "0" {
		t.Fatalf("第一位开口前应先来一条开始标记：%+v", start)
	}
	if first := recvTimeout(t, drafts); first.Text != "前半句" || first.CharacterID != bChar {
		t.Fatalf("第一条草稿：%+v", first)
	}
	// 同群重复发送：直接和经桥都是 ErrLocalGroupBusy，文字一样
	dErr := d.Reader.SendLocalGroup(ctx, api.LocalGroupSend{Key: gk, Text: "不能排队"})
	bErr := b.SendLocalGroup(ctx, api.LocalGroupSend{Key: gk, Text: "不能排队"})
	if !errors.Is(dErr, api.ErrLocalGroupBusy) || !errors.Is(bErr, api.ErrLocalGroupBusy) || dErr.Error() != bErr.Error() {
		t.Fatalf("同群重复发送：直接 %v 经桥 %v", dErr, bErr)
	}
	if err := b.StopLocalGroup(ctx, gk); err != nil {
		t.Fatal(err)
	}
	// 停止后这次发送返回（取消）；不是成功，下一位不再开始
	if err := recvTimeout(t, done); err == nil {
		t.Fatal("被停掉的整轮不该返回成功")
	}
	// 坏键：伪造的平台、不存在的群，直接和经桥一样被拒
	for _, k := range []api.ConversationKey{{Platform: "qq", Channel: "console", Scene: "group", Conversation: gk.Conversation}, bridgeGroupKey()} {
		dErr := d.Reader.SendLocalGroup(ctx, api.LocalGroupSend{Key: k, Text: "x"})
		bErr := b.SendLocalGroup(ctx, api.LocalGroupSend{Key: k, Text: "x"})
		if dErr == nil || bErr == nil || dErr.Error() != bErr.Error() {
			t.Fatalf("坏键 %+v：直接 %v 经桥 %v", k, dErr, bErr)
		}
	}
}

// 插话语义：SubmitLocal 开一轮、WatchLocalRun 看进度、StopLocalRun 停；回执和直接调同形
func TestBridgeSubmitWatchStopRun(t *testing.T) {
	t.Parallel()
	e := newBridgeEnv(t, llmtest.Script{Reply: llmtest.Text("前半句", "后半句"), Hold: func(int) int { return 1 }})
	d, b := e.open(t, nil)
	ctx := localCtx(t)
	key := bridgeKey()

	rc, err := b.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "开一轮", Mode: "start", ClientKey: "ck-1"})
	if err != nil {
		t.Fatal(err)
	}
	if rc.InputID == "" || rc.RunID == "" {
		t.Fatalf("回执缺字段：%+v", rc)
	}
	// 同一个客户端键再提交：回同一张回执（幂等），经桥和直接一样
	again, err := d.Reader.SubmitLocal(ctx, key, api.LocalInputSubmission{Text: "开一轮", Mode: "start", ClientKey: "ck-1"})
	if err != nil || again.InputID != rc.InputID || again.RunID != rc.RunID {
		t.Fatalf("同键再提交：%+v %v（第一次 %+v）", again, err, rc)
	}
	wsub, err := b.WatchLocalRun(ctx, key, rc.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer wsub.Close()
	deadline := time.After(20 * time.Second)
	for running := false; !running; {
		select {
		case s := <-wsub.Updates():
			running = s.State == api.Ready && s.Value.RunID == rc.RunID && s.Value.Text == "前半句"
		case <-deadline:
			t.Fatal("没看到这一轮写出前半句")
		}
	}
	sr, err := b.StopLocalRun(ctx, key, rc.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sr.RunID != rc.RunID {
		t.Fatalf("停止回执：%+v", sr)
	}
	// 不存在的一轮：经桥和直接是同一个错误文字
	_, dErr := d.Reader.StopLocalRun(ctx, key, "没有这一轮", 0)
	_, bErr := b.StopLocalRun(ctx, key, "没有这一轮", 0)
	if (dErr == nil) != (bErr == nil) || dErr != nil && dErr.Error() != bErr.Error() {
		t.Fatalf("不存在的轮：直接 %v 经桥 %v", dErr, bErr)
	}
}

// 其余哨兵：提问卡、已读、引用目标，经桥 errors.Is 成立、文字和直接调一样
func TestBridgeSentinelsMatchDirect(t *testing.T) {
	t.Parallel()
	e := newBridgeEnv(t, llmtest.Script{Reply: llmtest.Text("嗯")})
	d, b := e.open(t, nil)
	ctx := localCtx(t)
	key := bridgeKey()
	check := func(name string, sentinel error, direct, bridged error) {
		t.Helper()
		if !errors.Is(direct, sentinel) {
			t.Fatalf("%s：直接调就不是 %v：%v", name, sentinel, direct)
		}
		if !errors.Is(bridged, sentinel) {
			t.Fatalf("%s：经桥 errors.Is 不成立：%v", name, bridged)
		}
		if direct.Error() != bridged.Error() {
			t.Fatalf("%s：文字不一样\n直接 %q\n经桥 %q", name, direct.Error(), bridged.Error())
		}
	}
	check("AnswerLocal", api.ErrQuestionGone, d.Reader.AnswerLocal(ctx, key, "没有这个问题", 0), b.AnswerLocal(ctx, key, "没有这个问题", 0))
	check("DecideLocal", api.ErrApprovalGone, d.Reader.DecideLocal(ctx, key, "没有这张卡", api.ApproveOnce), b.DecideLocal(ctx, key, "没有这张卡", api.ApproveOnce))
	check("MarkRead", api.ErrReadMarkUnsupported, d.Reader.MarkRead(ctx, key, 5), b.MarkRead(ctx, key, 5))
	parts := []api.LocalPart{{Quote: &api.LocalQuote{Ref: api.EventRef{Conversation: key, Event: api.EventKey{Platform: "local", EventID: "不存在的消息"}}, Text: "片段"}, Text: "回复"}}
	check("SendLocalParts", api.ErrQuoteTarget, d.Reader.SendLocalParts(ctx, key, parts, nil), b.SendLocalParts(ctx, key, parts, nil))
	_, dErr := d.Reader.ReadAvatar(ctx, "user:12345")
	_, bErr := b.ReadAvatar(ctx, "user:12345")
	check("ReadAvatar", api.ErrNoAvatar, dErr, bErr)
}

// 只看历史：经桥发送得到 ErrLocalChatUnsupported，和直接调一样
func TestBridgeArchiveRefusesLocalChat(t *testing.T) {
	t.Parallel()
	e := newBridgeEnv(t, llmtest.Script{Reply: llmtest.Text("嗯")})
	// 先以 receive 开一次，建好库和目录，再关掉，用同一份数据开 archive
	a, err := backend.Open(context.Background(), e.opt)
	if err != nil {
		t.Fatal(err)
	}
	a.Desktop().Close()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	e.opt.Mode = backend.Archive
	e.opt.Account = ""
	d, b := e.open(t, nil)
	ctx := localCtx(t)
	key := bridgeKey()
	dErr := d.Reader.SendLocal(ctx, key, "不写库", nil)
	bErr := b.SendLocal(ctx, key, "不写库", nil)
	if !errors.Is(dErr, api.ErrLocalChatUnsupported) || !errors.Is(bErr, api.ErrLocalChatUnsupported) || dErr.Error() != bErr.Error() {
		t.Fatalf("archive 发送：直接 %v 经桥 %v", dErr, bErr)
	}
	if err := b.StopLocal(ctx, key); err != nil && !errors.Is(err, api.ErrLocalChatUnsupported) {
		t.Fatalf("archive 停止：%v", err)
	}
	gDirect := d.Reader.SendLocalGroup(ctx, api.LocalGroupSend{Key: bridgeGroupKey(), Text: "不写库"})
	gBridge := b.SendLocalGroup(ctx, api.LocalGroupSend{Key: bridgeGroupKey(), Text: "不写库"})
	if !errors.Is(gDirect, api.ErrLocalGroupUnsupported) || !errors.Is(gBridge, api.ErrLocalGroupUnsupported) || gDirect.Error() != gBridge.Error() {
		t.Fatalf("archive 群发送：直接 %v 经桥 %v", gDirect, gBridge)
	}
}

// 宿主：默认（inprocRPC）交给界面的是桥；-inproc-rpc=false 交的是 DesktopReader；换数据源后旧桥已关
func TestHostHandsOutBridgeUnlessDisabled(t *testing.T) {
	t.Parallel()
	for _, inproc := range []bool{true, false} {
		t.Run(fmt.Sprintf("inproc=%v", inproc), func(t *testing.T) {
			opt := hostFixture(t)
			var mu sync.Mutex
			var states []hostState
			h := newHost(opt, func(s hostState) { mu.Lock(); states = append(states, s); mu.Unlock() })
			h.inprocRPC = inproc
			h.start()
			defer h.shutdown()
			mu.Lock()
			var reader api.Reader
			for _, s := range states {
				if s.Reader != nil {
					reader = s.Reader
				}
			}
			mu.Unlock()
			if reader == nil {
				t.Fatal("宿主没交出 Reader")
			}
			_, isBridge := reader.(*rpcBridge)
			if isBridge != inproc {
				t.Fatalf("inprocRPC=%v 时 Reader 是 %T", inproc, reader)
			}
			if _, ok := reader.(api.LocalChat); !ok {
				t.Fatalf("界面的类型断言丢了：%T 没有 LocalChat", reader)
			}
			// 桥能读（经协议）
			if res, err := reader.WatchRuntime(context.Background()); err != nil {
				t.Fatal(err)
			} else {
				res.Close()
			}
			oldBridge, _ := reader.(*rpcBridge)
			h.op.Lock()
			err := h.closeCurrent()
			h.op.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if oldBridge != nil {
				// 关过的桥：再调只会得到连接断了的错误，不 panic
				if _, err := oldBridge.ReadSettings(context.Background()); err == nil {
					t.Fatal("旧数据源关掉后，旧桥还在读")
				}
			}
		})
	}
}
