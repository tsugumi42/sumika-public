package store

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/paths"
)

func mcpRepo(t *testing.T) EcosystemStore {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := OpenGo(p, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { set.Close() })
	clock := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	return EcosystemStore{DB: set.TraceStore().Trace, Now: func() time.Time { clock = clock.Add(time.Minute); return clock }}
}

func mcpRow(t *testing.T, repo EcosystemStore, id string) (state string, note MCPNote) {
	t.Helper()
	var raw string
	if err := repo.DB.QueryRowContext(t.Context(), `SELECT state,reason FROM kernel_mcp_calls WHERE id=?`, id).Scan(&state, &raw); err != nil {
		t.Fatal(err)
	}
	return state, parseNote(raw)
}

// 派发时记下传输种类、进程、时间；结局未知时记原因和时间。
func TestMCPDispatchAndEndKeepNote(t *testing.T) {
	t.Parallel()
	repo := mcpRepo(t)
	if err := repo.MCPDispatch(t.Context(), "call-1", "docs", "b", MCPTransportHTTP); err != nil {
		t.Fatal(err)
	}
	if err := repo.MCPEnd(t.Context(), "call-1", false, MCPCauseTimeout); err != nil {
		t.Fatal(err)
	}
	state, n := mcpRow(t, repo, "call-1")
	if state != MCPStateUnknown || n.Transport != "http" || n.Process != ProcessID() || n.Cause != MCPCauseTimeout || n.DispatchedAt == 0 || n.EndedAt <= n.DispatchedAt {
		t.Fatalf("留痕不全：%s %+v", state, n)
	}
	// 已经不是 dispatched 的行，晚到的 MCPEnd 不能把它改回去或改成成功
	if err := repo.MCPEnd(t.Context(), "call-1", true, ""); err != nil {
		t.Fatal(err)
	}
	if state, _ = mcpRow(t, repo, "call-1"); state != MCPStateUnknown {
		t.Fatalf("晚到的成功把未知改掉了：%s", state)
	}
}

// 解除必须带原因；带了以后每一行记谁、什么时候、原因，旧行保留成 risk_accepted，别的服务不动。
func TestMCPReleaseNeedsReasonAndKeepsTrace(t *testing.T) {
	t.Parallel()
	repo := mcpRepo(t)
	if err := repo.MCPDispatch(t.Context(), "a1", "docs", "b", MCPTransportHTTP); err != nil {
		t.Fatal(err)
	}
	if err := repo.MCPEnd(t.Context(), "a1", false, MCPCauseCancelled); err != nil {
		t.Fatal(err)
	}
	if err := repo.MCPDispatch(t.Context(), "b1", "other", "b", MCPTransportHTTP); err != nil {
		t.Fatal(err)
	}
	if err := repo.MCPEnd(t.Context(), "b1", false, MCPCauseError); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "   ", "\n\t", strings.Repeat("字", MaxMCPReleaseReason+1)} {
		if _, err := repo.MCPRelease(t.Context(), "docs", "主人", bad); err == nil {
			t.Fatalf("原因 %q 也解除了", bad)
		}
	}
	if _, err := repo.MCPRelease(t.Context(), "docs", " ", "有原因"); err == nil {
		t.Fatal("没有解除人也解除了")
	}
	if blocked, _ := repo.MCPBlocked(t.Context(), "docs"); !blocked {
		t.Fatal("被拒的解除也改了库")
	}
	n, err := repo.MCPRelease(t.Context(), "docs", "主人（命令行）", "核对过远端，旧调用已结束\n")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	state, note := mcpRow(t, repo, "a1")
	if state != MCPStateRiskAccepted || note.ReleasedBy != "主人（命令行）" || note.ReleaseWhy != "核对过远端，旧调用已结束" || note.ReleasedAt == 0 || note.Cause != MCPCauseCancelled || note.Transport != "http" {
		t.Fatalf("解除后旧记录被抹掉或没记全：%s %+v", state, note)
	}
	if blocked, _ := repo.MCPBlocked(t.Context(), "docs"); blocked {
		t.Fatal("解除后还挡着")
	}
	if blocked, _ := repo.MCPBlocked(t.Context(), "other"); !blocked {
		t.Fatal("解除 docs 带走了别的服务的隔离")
	}
	if n, err = repo.MCPRelease(t.Context(), "docs", "主人", "再解一次"); err != nil || n != 0 {
		t.Fatalf("重复解除应该什么都不动：n=%d err=%v", n, err)
	}
}

// 启动收尾：只动别的进程留下的 dispatched。本机（恰好是 stdio）的记 terminated；其余一律改 unknown 继续挡；unknown 的原样不动（原因保留）。
func TestMCPRecoverAfterRestart(t *testing.T) {
	t.Parallel()
	repo := mcpRepo(t)
	insert := func(id, server, state, reason string) {
		t.Helper()
		if _, err := repo.DB.ExecContext(t.Context(), `INSERT INTO kernel_mcp_calls(id,server_id,state,binding,reason) VALUES(?,?,?,?,?)`, id, server, state, "b", reason); err != nil {
			t.Fatal(err)
		}
	}
	foreign := func(id, server, transport, state string, cause string) {
		t.Helper()
		insert(id, server, state, MCPNote{Transport: transport, Process: "old-process", DispatchedAt: 1, Cause: cause}.encode())
	}
	foreign("local-d", "local", MCPTransportStdio, MCPStateDispatched, "")
	foreign("remote-d", "remote", MCPTransportHTTP, MCPStateDispatched, "")
	foreign("remote-u", "remote2", MCPTransportHTTP, MCPStateUnknown, MCPCauseTimeout)
	foreign("bridge-d", "bridge", MCPTransportStdioRemote, MCPStateDispatched, "")
	// 本机服务上次因为“已终止没写进库”保持 unknown：重启不能让它悄悄恢复，原因也不能被盖掉
	foreign("local-u", "local2", MCPTransportStdio, MCPStateUnknown, MCPCauseTimeout)
	// 没有备注的旧行、reason 不是合法 JSON、是 JSON 但缺传输种类、传输种类不认识：都不能当成本机
	insert("legacy", "legacy", MCPStateDispatched, "")
	insert("garbage", "garbage", MCPStateDispatched, "这不是 JSON {")
	insert("no-transport", "notransport", MCPStateDispatched, `{"process":"old-process","dispatchedAt":1}`)
	insert("weird-transport", "weird", MCPStateDispatched, `{"transport":"STDIO","process":"old-process"}`)
	insert("weird-transport2", "weird2", MCPStateDispatched, `{"transport":"carrier-pigeon","process":"old-process"}`)
	insert("garbage-json-types", "garbage2", MCPStateDispatched, `{"transport":42}`)
	// 本进程自己正在进行的调用不能被收尾
	if err := repo.MCPDispatch(t.Context(), "mine", "mine", "b", MCPTransportStdio); err != nil {
		t.Fatal(err)
	}
	got, err := repo.MCPRecoverAfterRestart(t.Context(), true)
	if err != nil || got.Terminated != 1 || got.Unknown != 8 {
		t.Fatalf("%+v %v", got, err)
	}
	want := map[string]string{"local-d": MCPStateTerminated, "remote-d": MCPStateUnknown, "remote-u": MCPStateUnknown, "bridge-d": MCPStateUnknown, "local-u": MCPStateUnknown,
		"legacy": MCPStateUnknown, "garbage": MCPStateUnknown, "no-transport": MCPStateUnknown, "weird-transport": MCPStateUnknown, "weird-transport2": MCPStateUnknown,
		"garbage-json-types": MCPStateUnknown, "mine": MCPStateDispatched}
	for id, w := range want {
		state, note := mcpRow(t, repo, id)
		if state != w {
			t.Errorf("%s：%s %+v，应为 %s", id, state, note, w)
		}
		if state == MCPStateSettled {
			t.Errorf("%s 被记成了成功", id)
		}
	}
	if _, note := mcpRow(t, repo, "local-d"); note.Cause != MCPCauseHostExit || note.EndedAt == 0 {
		t.Errorf("收尾要如实写原因：%+v", note)
	}
	if _, note := mcpRow(t, repo, "local-u"); note.Cause != MCPCauseTimeout {
		t.Errorf("已经是 unknown 的行原因被盖掉了：%+v", note)
	}
	for _, id := range []string{"remote", "remote2", "bridge", "local2", "legacy", "garbage", "notransport", "weird", "weird2", "garbage2", "mine"} {
		if blocked, _ := repo.MCPBlocked(t.Context(), id); !blocked {
			t.Errorf("%s 收尾后不该放行", id)
		}
	}
	if blocked, _ := repo.MCPBlocked(t.Context(), "local"); blocked {
		t.Error("本机服务遗留的已派发收尾后还被挡")
	}
	// 平台不保证宿主退出带走子进程：本机服务的行也保持隔离
	repo2 := mcpRepo(t)
	if _, err := repo2.DB.ExecContext(t.Context(), `INSERT INTO kernel_mcp_calls(id,server_id,state,binding,reason) VALUES('x','local','dispatched','b',?)`, MCPNote{Transport: MCPTransportStdio, Process: "old"}.encode()); err != nil {
		t.Fatal(err)
	}
	got, err = repo2.MCPRecoverAfterRestart(t.Context(), false)
	if err != nil || got.Terminated != 0 || got.Unknown != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if blocked, _ := repo2.MCPBlocked(t.Context(), "local"); !blocked {
		t.Fatal("平台不保证子进程退出时本机服务不该放行")
	}
}

// 解除原因里的格式字符、行分隔符也清掉；只剩这些的当没写
func TestCleanReleaseReasonStripsInvisibleAndSeparators(t *testing.T) {
	t.Parallel()
	rlo, ls, ps, zw, esc, nl := string(rune(0x202e)), string(rune(0x2028)), string(rune(0x2029)), string(rune(0x200b)), string(rune(0x1b)), string(rune(10))
	got, err := CleanReleaseReason("  已核对" + rlo + "反过来" + ls + "第二行" + ps + zw + "结束" + esc + " ")
	if err != nil || strings.ContainsAny(got, rlo+ls+ps+zw+esc+nl) {
		t.Fatalf("%q %v", got, err)
	}
	for _, only := range []string{rlo, ls + ps, zw + zw, " " + esc + " "} {
		if _, err := CleanReleaseReason(only); err == nil {
			t.Fatalf("%q 只有不可见字符也算写了原因", only)
		}
	}
}
