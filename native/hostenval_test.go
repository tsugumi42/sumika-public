package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	backend "sumika-native/internal/app"
	"sumika-native/internal/envcheck"
	"sumika-native/internal/paths"
	"sumika-native/internal/secrets"
	"sumika-native/internal/store"
	"sumika-native/internal/toolchain"
)

func hostEnvOptions(t *testing.T, configText string) backend.Options {
	t.Helper()
	data := filepath.Join(t.TempDir(), "环境 #%&")
	p, err := paths.Init(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	set.Close()
	return backend.Options{Root: t.TempDir(), DataDir: data, Logf: t.Logf, DialContext: refuseDial}
}

// writeLockedSecrets 造一个只带主密码包裹、没有本机副本的密钥文件：换电脑或重装系统后的“锁着”。
func writeLockedSecrets(t *testing.T, dataDir string) {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "seed-secrets.bin")
	st, err := secrets.Create(seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetPassphrase("passphrase-123"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(seed)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(b, &shape); err != nil {
		t.Fatal(err)
	}
	if wraps, ok := shape["wraps"].(map[string]any); ok {
		delete(wraps, "dpapi")
	}
	b, err = json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(dataDir, "config")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets.bin"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func envItem(t *testing.T, snap envcheck.Snapshot, id string) envcheck.Item {
	t.Helper()
	for _, it := range snap.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("快照里没有 %s：%+v", id, snap.Items)
	return envcheck.Item{}
}

// 验收第 2 条：配置开了 QQ、密钥库锁着，接收开不了退到只看历史，基础部分照样报出密钥和账号两条 bad。
func TestHostEnvSnapshotLockedSecretsAfterReceiveFallback(t *testing.T) {
	opt := hostEnvOptions(t, "qq:\n  accounts:\n    - id: \"90001\"\n      character: a\n      url: \"ws://127.0.0.1:1\"\n")
	writeLockedSecrets(t, opt.DataDir)

	var log stateLog
	h := newHost(opt, log.add)
	defer h.shutdown()
	h.start()
	if h.account != "90001" {
		t.Fatalf("配置里只有一个小号时应选它：%q %q", h.account, h.noRecv)
	}
	r := h.startReceive()
	if r.Outcome != receiveOpenFailed || r.Err == nil {
		t.Fatalf("密钥库锁着应以可重试的 open 失败回到只看历史：%+v", r)
	}
	if s := log.last(); s.Phase != phaseArchive {
		t.Fatalf("应退到只看历史：%+v", s)
	}

	snap := h.EnvSnapshot()
	if snap.Mode != envcheck.ModeArchive {
		t.Errorf("快照模式应是只看历史：%s", snap.Mode)
	}
	sec := envItem(t, snap, "env.secrets")
	if sec.State != envcheck.Bad || !strings.Contains(sec.Next, "secrets unlock") {
		t.Errorf("env.secrets 应是 bad 且下一步含 secrets unlock：%+v", sec)
	}
	plat := envItem(t, snap, "env.platform")
	if plat.State != envcheck.Bad || !strings.Contains(plat.Next, "secrets unlock") {
		t.Errorf("env.platform 应是 bad 且下一步含 secrets unlock：%+v", plat)
	}
}

// 后台没开：基础部分有值，运行部分 unknown 且原因是“后台没开”。
func TestHostEnvSnapshotWithoutBackend(t *testing.T) {
	opt := hostEnvOptions(t, "qq: {}\n")
	var log stateLog
	h := newHost(opt, log.add)
	h.set(hostState{Phase: phaseBlocked, Reason: "后台没在运行：测试"})

	snap := h.EnvSnapshot()
	if snap.Mode != envcheck.ModeStopped {
		t.Errorf("后台没开时模式应是 stopped：%s", snap.Mode)
	}
	if p := envItem(t, snap, "env.paths"); p.State != envcheck.OK || !strings.Contains(p.Detail, opt.DataDir) {
		t.Errorf("基础部分 env.paths 应有数据目录：%+v", p)
	}
	if n := envItem(t, snap, "env.nightly"); n.State != envcheck.NA {
		t.Errorf("env.nightly 应是 na：%+v", n)
	}
	for _, id := range []string{"env.models.gateway", "env.models.catalog", "env.embed", "env.kernel", "env.plugins", "env.mcp", "env.analyzer", "env.backup"} {
		it := envItem(t, snap, id)
		if it.State != envcheck.Unknown || !strings.Contains(it.Detail, "后台没开") {
			t.Errorf("%s 应 unknown 且写后台没开：%+v", id, it)
		}
	}
}

// 验收第 1 条：PATH 里没有 pwsh 时，命令分析器 warn，外部程序里的 pwsh 子项 warn、powershell 仍找到。
func TestHostEnvSnapshotAnalyzerWithoutPwsh(t *testing.T) {
	opt := hostEnvOptions(t, "qq: {}\n")
	var log stateLog
	h := newHost(opt, log.add)
	h.envSnap.resolve = func(toolchain.Env) []toolchain.Result {
		return []toolchain.Result{
			{Name: "pwsh", Purpose: "命令分析器"},
			{Name: "powershell", Purpose: "执行工具的 PowerShell", Found: true, Path: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, Source: toolchain.SourceSystem},
			{Name: "rg", Required: true, Purpose: "搜索", Found: true, Path: "vendor/bin/rg.exe", Source: toolchain.SourceBundled, Version: "14.1.0"},
			{Name: "fd", Required: true, Purpose: "找文件", Found: true, Path: "vendor/bin/fd.exe", Source: toolchain.SourceBundled},
		}
	}
	h.envSnap.runtime = func(context.Context) envcheck.Inputs {
		return envcheck.Inputs{Analyzer: envcheck.AnalyzerInput{Available: true, LastErr: "PATH 里没有 pwsh"}}
	}
	h.setTools(h.resolveTools())
	h.refreshEnv()

	snap := h.EnvSnapshot()
	an := envItem(t, snap, "env.analyzer")
	if an.State != envcheck.Warn || !strings.Contains(an.Next, "PowerShell 7") {
		t.Errorf("env.analyzer 应 warn 并让装 PowerShell 7：%+v", an)
	}
	tools := envItem(t, snap, "env.tools")
	if tools.State != envcheck.Warn {
		t.Errorf("env.tools 应 warn：%+v", tools)
	}
	sub := func(name string) envcheck.SubItem {
		t.Helper()
		for _, s := range tools.Sub {
			if s.Name == name {
				return s
			}
		}
		t.Fatalf("env.tools 没有 %s 子项：%+v", name, tools.Sub)
		return envcheck.SubItem{}
	}
	if pw := sub("pwsh"); pw.State != envcheck.Warn || !strings.Contains(pw.Detail, "没找到") {
		t.Errorf("env.tools 的 pwsh 应 warn 且没找到：%+v", pw)
	}
	if ps := sub("powershell"); ps.State != envcheck.OK || !strings.Contains(ps.Detail, "系统目录") {
		t.Errorf("执行工具的 powershell 仍应找到：%+v", ps)
	}
	if rg := sub("rg"); rg.State != envcheck.OK || !strings.Contains(rg.Detail, "自带，14.1.0") {
		t.Errorf("登记表的版本要进 env.tools：%+v", rg)
	}
}

// 坑 11：从没连上的 MCP 服务（不在 S11 的隔离/重连用完里）也要报 bad。
func TestHostEnvSnapshotMCPNeverConnected(t *testing.T) {
	opt := hostEnvOptions(t, "qq: {}\n")
	var log stateLog
	h := newHost(opt, log.add)
	h.envSnap.runtime = func(context.Context) envcheck.Inputs {
		return envcheck.Inputs{MCP: envcheck.MCPInput{
			CheckInput:     envcheck.CheckInput{Available: true, CheckResult: envcheck.CheckResult{State: "ok"}},
			NeverConnected: []envcheck.NeverConnectedService{{Name: "files", Cause: "exec: not found"}},
		}}
	}
	h.refreshEnv()

	it := envItem(t, h.EnvSnapshot(), "env.mcp")
	if it.State != envcheck.Bad || !strings.Contains(it.Detail, "files") || !strings.Contains(it.Next, "命令和参数") {
		t.Fatalf("从没连上的 MCP 应报 bad 并点名服务：%+v", it)
	}
}

// 插件 failed 要报 bad，例子（详情）里有插件 id。
func TestHostEnvSnapshotPluginFailed(t *testing.T) {
	opt := hostEnvOptions(t, "qq: {}\n")
	var log stateLog
	h := newHost(opt, log.add)
	h.envSnap.runtime = func(context.Context) envcheck.Inputs {
		return envcheck.Inputs{Plugins: envcheck.PluginsInput{Available: true,
			Failed: []envcheck.PluginFailure{{ID: "memory", Err: "bad config"}}}}
	}
	h.refreshEnv()

	it := envItem(t, h.EnvSnapshot(), "env.plugins")
	if it.State != envcheck.Bad || !strings.Contains(it.Detail, "memory") {
		t.Fatalf("插件启用失败应报 bad 并带 id：%+v", it)
	}
}

// 环境快照重算后交给界面（不走阶段发布），并记进最近一次状态：下一次阶段发布带着同一份。
func TestHostEnvSnapshotTravelsWithHostState(t *testing.T) {
	opt := hostEnvOptions(t, "qq: {}\n")
	var log stateLog
	h := newHost(opt, log.add)
	var got []envcheck.Snapshot
	h.onEnv = func(snap envcheck.Snapshot) { got = append(got, snap) }
	h.set(hostState{Phase: phaseBlocked, Reason: "后台没在运行：测试"})

	if s := log.last(); s.Phase != phaseBlocked {
		t.Fatalf("最后发布的状态应是后台没在运行：%+v", s)
	}
	if len(got) == 0 || got[0].At.IsZero() || len(got[0].Items) == 0 {
		t.Fatalf("重算完应把快照交给界面：%+v", got)
	}
	// 下一次阶段发布要带着这份快照，不然换状态会把界面上的一份清空
	h.set(hostState{Phase: phaseStopped, Reason: "已停"})
	if s := log.last(); len(s.Env.Items) == 0 || s.Env.At.IsZero() {
		t.Fatalf("阶段发布应带着最近一次环境快照：%+v", s)
	}
}

// 版本命令卡住时，宿主持 op 锁只做查路径；开始接收（拿 op 锁）不能被版本等待挡住。
// waitVersions 顶上版本等待，模拟假版本命令睡住；取到版本后快照再补一次。
func TestHostVersionWaitDoesNotHoldOpLock(t *testing.T) {
	opt := hostEnvOptions(t, "qq: {}\n")
	var log stateLog
	h := newHost(opt, log.add)
	entered := make(chan struct{})
	release := make(chan struct{})
	h.envSnap.waitVersions = func(ctx context.Context) []toolchain.Result {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return []toolchain.Result{{Name: "rg", Required: true, Purpose: "搜索", Found: true, Path: "vendor/bin/rg.exe", Source: toolchain.SourceBundled, Version: "9.9.9"}}
	}
	done := make(chan struct{})
	go func() {
		h.refreshToolsAsync()
		close(done)
	}()
	<-entered

	locked := make(chan struct{})
	go func() {
		h.op.Lock()
		h.op.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("版本还在等时，op 锁没放开，开始接收会被挡住")
	}
	close(release)
	<-done

	tools := h.envToolsInput()
	for _, r := range tools.Results {
		if r.Name == "rg" {
			if r.Version != "9.9.9" {
				t.Fatalf("版本取到后应补进快照：%+v", r)
			}
			return
		}
	}
	t.Fatalf("版本取到后快照里应有 rg：%+v", tools)
}
