package main

// 桥的分流（bunroutes.go、inproc_rpc.go，规格-本地私聊切到Bun 第四节）：
//   - 方法表覆盖协议里的每个方法，一个不漏、一个不多；
//   - bun 模式下桥的每个方法都按表走对客户端；Bun 不在时走 Bun 的方法回“本地私聊的后台不在”，Go 那边一个请求都收不到；
//   - go 模式（bun 为空）所有方法照旧只走 Go；
//   - 流式草稿从 Bun 的连接推，桥原样转给界面；订阅跟着 Bun 的连接重订。
// 两边都用假传输：只记收到了哪个协议方法，不起真后台。

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/rpcwire"
)

// bunPrefixes 是规格第四节写的“走 Bun”的方法族；表里这些前缀的方法必须全是 routeBun（独立于 bunroutes.go 的写法，用来对它）
var bunPrefixes = []string{"local.", "local_group.", "local_run.", "local_status.", "local_approvals.", "local_questions.", "whisper.", "whisper_log.",
	"loop.", "reminder.", "held.", "approval_rules.", "conversation_approval.", "workspace.", "checkpoints.", "checkpoint_progress.", "selftest."}

func TestRouteTableCoversEveryWireMethod(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, m := range rpcwire.Methods {
		seen[m.Name] = true
		r, ok := routes[m.Name]
		if !ok || r == routeUnset {
			t.Errorf("协议方法 %s 没在 bunroutes.go 里归类：加了新方法就要说清它归 Go 还是 Bun（会写本地数据的归 Bun）", m.Name)
			continue
		}
		isBunFamily := false
		for _, p := range bunPrefixes {
			isBunFamily = isBunFamily || strings.HasPrefix(m.Name, p)
		}
		switch {
		case m.Name == rpcwire.MethodReadMarkSet:
			if r != routeByKey {
				t.Errorf("read_mark.set 该按会话键分流（本地走 Bun，QQ 走 Go）：%v", r)
			}
		case strings.HasPrefix(m.Name, "local_status.") && m.Name != rpcwire.MethodLocalStatusRead:
			if r != routeMerged {
				t.Errorf("%s 该两边合并（Bun 管本地的状态，Go 管 QQ 的正在输入）：%v", m.Name, r)
			}
		case isBunFamily && r != routeBun:
			t.Errorf("%s 属于本地私聊的方法族，必须走 Bun：%v", m.Name, r)
		case !isBunFamily && r != routeGo:
			t.Errorf("%s 不是本地私聊的方法族，该走 Go：%v", m.Name, r)
		}
	}
	for name := range routes {
		if !seen[name] {
			t.Errorf("方法表里的 %s 不在协议里：协议改名或删了方法，表要跟着改", name)
		}
	}
}

type twoSided struct {
	b      *rpcBridge
	goSide *fakeRPC
	bun    *fakeRPC
	link   *bunLink
}

// newTwoSided：bunMode 为假就是 go 模式（桥没有 bun）；bunUp 为假是 Bun 不在
func newTwoSided(t *testing.T, bunMode, bunUp bool) *twoSided {
	t.Helper()
	ts := &twoSided{goSide: newFakeRPC(), bun: newFakeRPC()}
	b := &rpcBridge{c: ts.goSide.full(t)}
	if bunMode {
		ts.link = newBunLink("测试：Bun 还没起")
		if bunUp {
			ts.link.up(ts.bun.full(t))
		}
		b.bun = ts.link
	}
	ts.b = b
	return ts
}

// callAll 对桥的每个方法（63 个接口方法）用零值参数调一遍，回每个方法的错误；订阅类的拿到就关
func callAll(t *testing.T, b *rpcBridge) map[string]error {
	t.Helper()
	out := map[string]error{}
	rv := reflect.ValueOf(b)
	for _, name := range bridgeMethodNames() {
		m := rv.MethodByName(name)
		if !m.IsValid() {
			t.Fatalf("桥没有方法 %s", name)
		}
		mt := m.Type()
		args := make([]reflect.Value, mt.NumIn())
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		for i := range args {
			if mt.In(i) == reflect.TypeFor[context.Context]() {
				args[i] = reflect.ValueOf(ctx)
			} else {
				args[i] = reflect.Zero(mt.In(i))
			}
		}
		var res []reflect.Value
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s 用零值参数调用 panic：%v", name, r)
				}
			}()
			res = m.Call(args)
		}()
		var err error
		for _, r := range res {
			if e, ok := r.Interface().(error); ok && e != nil {
				err = e
			}
			if c, ok := r.Interface().(interface{ Close() }); ok && !r.IsNil() {
				c.Close()
			}
		}
		cancel()
		out[name] = err
	}
	return out
}

// 逐个方法：bun 模式下收到请求的那一边由方法表定；Go 一边的请求都是表里 routeGo 的，Bun 一边的都是 routeBun 的
func TestBridgeSendsEveryMethodWhereTheTableSays(t *testing.T) {
	t.Parallel()
	ts := newTwoSided(t, true, true)
	callAll(t, ts.b)
	for _, m := range ts.goSide.took() {
		if r := routes[m]; r != routeGo && r != routeByKey && r != routeMerged {
			t.Errorf("Go 那边收到了 %s：表里它归 %v，不该走 Go", m, r)
		}
	}
	bunGot := ts.bun.took()
	for _, m := range bunGot {
		if r := routes[m]; r != routeBun && r != routeMerged && r != routeByKey {
			t.Errorf("Bun 那边收到了 %s：表里它归 %v，不该走 Bun", m, r)
		}
	}
	if len(ts.goSide.took()) == 0 || len(bunGot) == 0 {
		t.Fatalf("两边都该收到请求：go=%d bun=%d", len(ts.goSide.took()), len(bunGot))
	}
	// 几个要紧的：发送、本地审批、工作区、检查点、提醒、放行规则、悄悄话必须到 Bun；消息、总览、设置必须到 Go
	toBun := map[string]bool{}
	for _, m := range bunGot {
		toBun[m] = true
	}
	for _, m := range []string{rpcwire.MethodLocalSend, rpcwire.MethodLocalSendParts, rpcwire.MethodLocalSendImages, rpcwire.MethodLocalSaveImage, rpcwire.MethodLocalStop,
		rpcwire.MethodLocalDecide, rpcwire.MethodLocalSubmit, rpcwire.MethodLocalStopRun, rpcwire.MethodLocalAnswer, rpcwire.MethodWorkspaceSet, rpcwire.MethodWorkspaceClear,
		rpcwire.MethodCheckpointsRestore, rpcwire.MethodReminderCreate, rpcwire.MethodLoopAdd, rpcwire.MethodApprovalRulesAdd, rpcwire.MethodConversationApprovalSet,
		rpcwire.MethodWhisperSend, rpcwire.MethodLocalGroupSend, rpcwire.MethodLocalGroupCreate} {
		if !toBun[m] {
			t.Errorf("%s 会写本地数据，该送到 Bun，实际 Bun 收到的是 %v", m, bunGot)
		}
	}
	toGo := map[string]bool{}
	for _, m := range ts.goSide.took() {
		toGo[m] = true
	}
	for _, m := range []string{rpcwire.MethodMessagesRead, rpcwire.MethodOverviewSubscribe, rpcwire.MethodConversationSubscribe, rpcwire.MethodSettingsRead, rpcwire.MethodRuntimeSubscribe, rpcwire.MethodMediaRead} {
		if !toGo[m] {
			t.Errorf("%s 是读方法，该走 Go，实际 Go 收到的是 %v", m, ts.goSide.took())
		}
	}
}

// 改坏检查 6、8 的守门测试：Bun 不在，走 Bun 的方法一律回“后台不在”，Go 那边一个请求都不能收到——绝不退回 Go 去写
func TestBridgeNeverFallsBackToGoWhenBunIsDown(t *testing.T) {
	t.Parallel()
	ts := newTwoSided(t, true, false)
	errs := callAll(t, ts.b)
	for _, m := range ts.goSide.took() {
		if routes[m] == routeBun {
			t.Errorf("Bun 不在时 Go 收到了本该走 Bun 的 %s：这是退回 Go 去写", m)
		}
	}
	if got := ts.bun.took(); len(got) != 0 {
		t.Fatalf("Bun 不在，它不该收到请求：%v", got)
	}
	down := 0
	var names []string
	for n := range errs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if errors.Is(errs[n], api.ErrLocalBackendDown) {
			down++
			if !strings.Contains(errs[n].Error(), "本地私聊的后台不在") {
				t.Errorf("%s 的错误要说清楚后台不在：%v", n, errs[n])
			}
		}
	}
	if down < 40 {
		t.Fatalf("Bun 不在时该有一大批方法回 ErrLocalBackendDown，只有 %d 个：%v", down, errs)
	}
	for _, n := range []string{"SendLocal", "SendLocalParts", "DecideLocal", "SetLocalWorkspace", "RestoreCheckpoint", "CreateReminder", "AddLoop", "SendWhisper", "SendLocalGroup", "AddApprovalRule", "StopLocal"} {
		if !errors.Is(errs[n], api.ErrLocalBackendDown) {
			t.Errorf("%s 在 Bun 不在时该回 ErrLocalBackendDown：%v", n, errs[n])
		}
	}
	// 读方法不受影响
	if errors.Is(errs["ReadMessages"], api.ErrLocalBackendDown) || errors.Is(errs["ReadSettings"], api.ErrLocalBackendDown) {
		t.Fatal("读方法走 Go，Bun 不在不该影响它们")
	}
	// 连接已经断了、监护还没来得及标记：也当不在
	ts2 := newTwoSided(t, true, true)
	ts2.bun.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := ts2.b.StopLocal(context.Background(), api.ConversationKey{}); errors.Is(err, api.ErrLocalBackendDown) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Bun 的连接断了，桥该把走 Bun 的方法当作后台不在")
}

// go 模式：bun 为空，所有方法照旧只走 Go（邻居不变）
func TestBridgeGoModeSendsEverythingToGo(t *testing.T) {
	t.Parallel()
	ts := newTwoSided(t, false, false)
	callAll(t, ts.b)
	sawLocal := false
	for _, m := range ts.goSide.took() {
		sawLocal = sawLocal || m == rpcwire.MethodLocalSend
	}
	if !sawLocal {
		t.Fatalf("go 模式下 SendLocal 该照旧走 Go：%v", ts.goSide.took())
	}
}

// MarkRead 按会话键分流：本地会话的已读标记写 local.db（归 Bun），QQ 的走 Go；没归类的方法不放行
func TestBridgeMarkReadAndUnroutedMethod(t *testing.T) {
	t.Parallel()
	ts := newTwoSided(t, true, true)
	ctx := context.Background()
	_ = ts.b.MarkRead(ctx, api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:a"}, 1)
	_ = ts.b.MarkRead(ctx, api.ConversationKey{Platform: "qq", Channel: "qq", Scene: "group", Conversation: "qq:group:1"}, 1)
	if got := ts.bun.took(); len(got) != 1 || got[0] != rpcwire.MethodReadMarkSet {
		t.Fatalf("本地会话的 MarkRead 该到 Bun：%v", got)
	}
	if got := ts.goSide.took(); len(got) != 1 || got[0] != rpcwire.MethodReadMarkSet {
		t.Fatalf("QQ 会话的 MarkRead 该到 Go：%v", got)
	}
	ts.link.down("测试")
	if err := ts.b.MarkRead(ctx, api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:a"}, 1); !errors.Is(err, api.ErrLocalBackendDown) {
		t.Fatalf("Bun 不在时本地会话的 MarkRead 不能落到 Go：%v", err)
	}
	// 表里没有的方法名：via 不放行（改坏检查 7 的运行时那一半）
	if c, err := ts.b.via("local.invented_write"); err == nil || c != nil {
		t.Fatalf("没归类的方法不该放行：%v", err)
	}
}

// 流式草稿从 Bun 的连接推来，桥原样转给界面的回调；Go 那边一个请求都没有
func TestBridgeForwardsBunDrafts(t *testing.T) {
	t.Parallel()
	ts := newTwoSided(t, true, true)
	ts.bun.onCall = func(f *fakeRPC, m rpcwire.Message) {
		if m.Method != rpcwire.MethodLocalSend {
			return
		}
		var p rpcwire.LocalSendParams
		_ = json.Unmarshal(m.Params, &p)
		raw, _ := json.Marshal(rpcwire.LocalDraftNotice{Stream: p.Stream, Draft: rpcwire.LocalDraft{TurnID: "t1", Step: 1, Text: "来自 Bun 的流式回复"}})
		f.push(rpcwire.Message{JSONRPC: rpcwire.Version, Method: rpcwire.MethodLocalDraft, Params: raw})
	}
	got := make(chan api.LocalDraft, 4)
	k := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:a"}
	if err := ts.b.SendLocal(context.Background(), k, "你好", func(d api.LocalDraft) { got <- d }); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-got:
		if d.Text != "来自 Bun 的流式回复" || d.TurnID != "t1" {
			t.Fatalf("草稿不对：%+v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("桥没把 Bun 的流式草稿转给界面")
	}
	if len(ts.goSide.took()) != 0 {
		t.Fatalf("发送本地消息不该碰 Go：%v", ts.goSide.took())
	}
}

func recvSnap[T any](t *testing.T, sub api.Subscription[T]) api.Snapshot[T] {
	t.Helper()
	select {
	case s, ok := <-sub.Updates():
		if !ok {
			t.Fatal("订阅提前结束了")
		}
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("等订阅的快照超时")
		return api.Snapshot[T]{}
	}
}

// 审批卡 / 提问 / 状态三个长期订阅跟着 Bun 的连接走：Bun 不在先发空值；连上以后订；连接换了（Bun 重启）重订
func TestBridgeBunWatchesFollowTheConnection(t *testing.T) {
	t.Parallel()
	ts := newTwoSided(t, true, false)
	ctx := context.Background()
	sub, err := ts.b.WatchLocalApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if s := recvSnap(t, sub); s.State != api.Ready || len(s.Value) != 0 || s.Version != 1 {
		t.Fatalf("Bun 不在时先发一份空值：%+v", s)
	}
	if len(ts.bun.took()) != 0 || len(ts.goSide.took()) != 0 {
		t.Fatalf("Bun 不在时订阅不该发请求：bun=%v go=%v", ts.bun.took(), ts.goSide.took())
	}
	// 界面一直在读快照（真界面就是这样）：后面几步里订阅发出来的空值有人收
	go func() {
		for range sub.Updates() {
		}
	}()
	// Bun 连上了：订阅发到 Bun
	ts.link.up(ts.bun.full(t))
	waitSeen(t, ts.bun, rpcwire.MethodLocalApprovalsSubscribe)
	// 连接断了又换了一个新的（Bun 重启）：重订，不卡在旧连接上
	second := newFakeRPC()
	ts.bun.Close()
	ts.link.down("Bun 退出了")
	ts.link.up(second.full(t))
	waitSeen(t, second, rpcwire.MethodLocalApprovalsSubscribe)
	if len(ts.goSide.took()) != 0 {
		t.Fatalf("审批订阅不该碰 Go：%v", ts.goSide.took())
	}
}

func waitSeen(t *testing.T, f *fakeRPC, method string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-f.seen:
			if m == method {
				return
			}
		case <-deadline:
			t.Fatalf("等不到请求 %s，收到过 %v", method, f.took())
		}
	}
}

// 状态订阅两边拼：Go 管 QQ 的“正在输入”，Bun 管本地的；谁也不等谁，Bun 不在时只剩 Go 那份
func TestMergeSubsConcatenatesBothSides(t *testing.T) {
	t.Parallel()
	mk := func() (*manualSub[[]string], *manualSub[[]string]) {
		return newManualSub[[]string](), newManualSub[[]string]()
	}
	a, b := mk()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := mergeSubs[string](ctx, a, b)
	defer m.Close()
	a.send([]string{"qq-typing"})
	if s := recvSnap(t, m); strings.Join(s.Value, ",") != "qq-typing" || s.Version != 1 {
		t.Fatalf("一边先到就先发：%+v", s)
	}
	b.send([]string{"local-goal"})
	if s := recvSnap(t, m); strings.Join(s.Value, ",") != "qq-typing,local-goal" || s.Version != 2 {
		t.Fatalf("两边拼起来：%+v", s)
	}
	a.send(nil) // 对方不输入了
	if s := recvSnap(t, m); strings.Join(s.Value, ",") != "local-goal" {
		t.Fatalf("一边整份换掉：%+v", s)
	}
}

type manualSub[T any] struct {
	ch chan api.Snapshot[T]
	v  uint64
}

func newManualSub[T any]() *manualSub[T]                { return &manualSub[T]{ch: make(chan api.Snapshot[T], 8)} }
func (m *manualSub[T]) Updates() <-chan api.Snapshot[T] { return m.ch }
func (m *manualSub[T]) Close()                          {}
func (m *manualSub[T]) send(v T) {
	m.v++
	m.ch <- api.Snapshot[T]{Version: m.v, Result: api.Result[T]{State: api.Ready, Value: v}}
}

// ───── 没上协议、直接调内嵌 DesktopReader 的方法（规格第四节：逐个过一遍）─────

// directClass 是 DesktopReader 上没被桥自己声明的方法（反射量出来的，现在 57 个）各归哪一类。新方法冒出来这里没写，测试就红：
//   - 读：只读本地库或别的数据（bun 模式下 Go 照读 local.db）；
//   - QQ：只碰 qq.db、QQ 媒体、QQ 补录；
//   - 配置：改 sumika.yml 或配置目录，不碰 local.db 和本地状态文件；
//   - 装配：组装 Service 用的，界面不调；
//   - 本地写：会写 local.db 或 go/ 下的本地状态文件，bun 模式下 Go 这边回 ErrLocalOwnedByBun（internal/app 的 bunOwned；local_owner_test.go 逐个断言）。
var directClass = map[string]string{
	"CancelVideoDownload": "QQ", "RequestVideoDownload": "QQ", "WatchVideoDownload": "QQ", "FetchForward": "QQ",
	"ReadAlignmentGaps": "QQ", "ReadGaps": "QQ", "RequestAlignment": "QQ", "RequestGapBackfill": "QQ", "SetGapAcknowledged": "QQ", "WatchAlignment": "QQ", "WatchBackfill": "QQ", "WatchGaps": "QQ",
	"FreeAPI": "配置", "FreeAPIAct": "配置", "LinkSettings": "配置", "SetLinkSettings": "配置", "Location": "配置", "SetLocation": "配置",
	"ModelSettings": "配置", "SetModelSettings": "配置", "RefreshModels": "配置", "SearchPlaces": "配置", "WriteSetting": "配置", "ReadSettingsRegistry": "配置",
	"SecretsStatus": "配置", "SecretsList": "配置", "SecretsSet": "配置", "SecretsDelete": "配置", "SecretsSetPassphrase": "配置", "SecretsUnlock": "配置",
	"ArtistList": "读", "ArtistImport": "配置", "ArtistUndo": "配置", "ArtistConfirm": "配置", "ArtistRemove": "配置", "LinkPreview": "读", "LinkTemplates": "读", "ResolveParts": "读", "ResolveQuote": "读", "ResolveWhisperParts": "读", "RunningTurns": "读", "SelfcheckLine": "读",
	"SelftestCharacter": "读", "SlashCommands": "读", "WebBalanceTexts": "读", "WebBalances": "读", "ProactiveStatus": "读", "WhisperList": "读", "WhisperLog": "读", "WhisperStatus": "读",
	"WithBackfills": "装配", "WithData": "装配", "WithForwardFetcher": "装配", "WithLocalGroups": "装配", "WithReadMark": "装配", "WithVideoDownloads": "装配",
	"ProactiveCloseThread": "本地写", "ProactiveSnooze": "本地写", "SelftestChat": "本地写", "SelftestWhisper": "本地写", "SlashCommand": "本地写", "WhisperCancel": "本地写",
}

func TestEveryDirectDesktopMethodIsClassified(t *testing.T) {
	t.Parallel()
	declared := declaredOnBridge(t)
	rt := reflect.TypeOf(&backend.DesktopReader{})
	seen := map[string]bool{}
	for i := 0; i < rt.NumMethod(); i++ {
		name := rt.Method(i).Name
		if declared[name] {
			continue
		}
		seen[name] = true
		if directClass[name] == "" {
			t.Errorf("DesktopReader.%s 没上协议、桥也没声明，直接调 Go：它会不会写 local.db 或本地状态文件？在 directClass 里归类（会写的要在 internal/app 加 bunOwned 守卫）", name)
		}
	}
	for name := range directClass {
		if !seen[name] {
			t.Errorf("directClass 里的 %s 已经不是直接调的方法了：表跟着改", name)
		}
	}
}
