package main

// doctor 的端到端测试：命令行 → 命名管道 → ctlserver → 假宿主快照 → 中文输出和退出码（规格-环境自检 验收第 1、2 条）。
// 快照用 envcheck.Build 造，和宿主交给服务端的是同一份结构；真正的宿主转换在根包 ctl_env_test.go 测。

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
	"sumika-native/internal/envcheck"
)

type doctorBackend struct {
	*stubBackend
	snap ctlproto.DoctorSnapshot
}

func (b *doctorBackend) EnvSnapshot(context.Context) (ctlproto.DoctorSnapshot, error) {
	return b.snap, nil
}

// toDoctor 把 envcheck 快照拷成协议结构；和宿主里的 envDoctorSnapshot 字段一一对应。
func toDoctor(s envcheck.Snapshot) ctlproto.DoctorSnapshot {
	out := ctlproto.DoctorSnapshot{At: s.At, Mode: string(s.Mode), Items: make([]ctlproto.DoctorItem, 0, len(s.Items))}
	for _, it := range s.Items {
		di := ctlproto.DoctorItem{ID: it.ID, Group: it.Group, Name: it.Name, State: string(it.State),
			Detail: it.Detail, Next: it.Next, Affects: it.Affects}
		for _, sub := range it.Sub {
			di.Sub = append(di.Sub, ctlproto.DoctorSubItem{Name: sub.Name, State: string(sub.State), Detail: sub.Detail, Next: sub.Next})
		}
		out.Items = append(out.Items, di)
	}
	return out
}

// doctorRun 起一个带假快照的服务端，用命令行入口跑一次 doctor，返回退出码和输出。
func doctorRun(t *testing.T, snap ctlproto.DoctorSnapshot, args ...string) (int, string) {
	t.Helper()
	name := testPipeName("doctor-e2e")
	ln, err := ctlserver.ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	b := &doctorBackend{stubBackend: &stubBackend{}, snap: snap}
	srv := ctlserver.New(b, ctlserver.Options{})
	go srv.Serve(ln)
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, _ bool) (net.Conn, error) { return ctlproto.Dial(ctx, name) }

	var out, errb bytes.Buffer
	code := run(args, &out, &errb, dial)
	return code, out.String() + errb.String()
}

// 验收第 1 条：PATH 里没有 pwsh 时，分析器一条 warn、外部程序里的 pwsh 子项 warn、powershell 仍正常；只有“注意”时退出码 0。
func TestDoctorE2EPwshMissing(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local)
	snap := toDoctor(envcheck.Build(envcheck.Inputs{
		At: at, Mode: envcheck.ModeReceive,
		Secrets:  envcheck.SecretsInput{Exists: true, Unlocked: true, HasLocalCopy: true},
		Platform: envcheck.PlatformInput{Enabled: false},
		Gateway:  envcheck.GatewayInput{Available: true, Source: "http://127.0.0.1:3425", ReadAt: at},
		Catalog:  envcheck.CatalogInput{Available: true, Enabled: false},
		Embed:    envcheck.CheckInput{Available: true, CheckResult: envcheck.CheckResult{State: "ok"}},
		Kernel:   envcheck.CheckInput{Available: true, CheckResult: envcheck.CheckResult{State: "ok"}},
		Plugins:  envcheck.PluginsInput{Available: true},
		MCP:      envcheck.MCPInput{CheckInput: envcheck.CheckInput{Available: true, CheckResult: envcheck.CheckResult{State: "ok"}}},
		Analyzer: envcheck.AnalyzerInput{Available: true, LastErr: "PATH 里没有 pwsh"},
		Backup: envcheck.BackupInput{Available: true,
			Daily: envcheck.CheckResult{State: "ok"}, Drill: envcheck.CheckResult{State: "ok"}},
		Tools: envcheck.ToolsInput{Checked: true, Results: []envcheck.ToolResult{
			{Name: "pwsh", Purpose: "命令分析器"},
			{Name: "powershell", Purpose: "执行工具的 PowerShell", Found: true, Path: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, Source: "system", Version: "5.1.22621.4391"},
			{Name: "rg", Required: true, Purpose: "搜索", Found: true, Path: `D:\Sumika\vendor\bin\rg.exe`, Source: "bundled"},
		}},
		Paths: envcheck.PathsInput{Root: `D:\Sumika`},
	}))

	code, out := doctorRun(t, snap, "doctor")
	t.Logf("doctor 输出（验收第 1 条）：\n%s", out)
	if code != 0 {
		t.Fatalf("只有“注意”时退出码应是 0，得到 %d：\n%s", code, out)
	}
	for _, want := range []string{
		"注意 命令分析器：", "下一步：装 PowerShell 7",
		"注意 外部程序：", "注意 pwsh：没找到", "正常 powershell：",
		`正常 powershell：C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe（系统目录，5.1.22621.4391）`,
		"0 项有问题、2 项要注意",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里应有 %q：\n%s", want, out)
		}
	}
	// --json 原样给整个快照，子项也在里面
	code, out = doctorRun(t, snap, "--json", "doctor")
	var r ctlproto.Reply
	if code != 0 || json.Unmarshal([]byte(out), &r) != nil || !r.OK {
		t.Fatalf("--json：%d %q", code, out)
	}
	var d ctlproto.DoctorSnapshot
	if err := json.Unmarshal(r.Data, &d); err != nil || len(d.Items) == 0 {
		t.Fatalf("--json 的数据不是快照：%v %s", err, r.Data)
	}
	found := false
	for _, it := range d.Items {
		for _, sub := range it.Sub {
			if sub.Name == "pwsh" && sub.State == "warn" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("--json 里应有 pwsh 子项：%s", r.Data)
	}
}

// 验收第 2 条：嵌入模型目录不存在、密钥库锁着且开了 QQ；doctor 列出这两条 bad，嵌入带完整路径，退出码 1。
func TestDoctorE2EBadEnvironment(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local)
	const modelPath = `D:\数据\models\x.onnx`
	snap := toDoctor(envcheck.Build(envcheck.Inputs{
		At: at, Mode: envcheck.ModeArchive,
		Secrets: envcheck.SecretsInput{NeedToken: true, Account: "90001", Exists: true, Unlocked: false, Note: "需要主密码或恢复码"},
		Platform: envcheck.PlatformInput{Enabled: true, Locked: true, Accounts: []envcheck.PlatformAccount{
			{ID: "90001", URL: "ws://127.0.0.1:1"},
		}},
		Gateway: envcheck.GatewayInput{Available: true, Source: "http://127.0.0.1:3425", ReadAt: at},
		Catalog: envcheck.CatalogInput{Available: true, Enabled: false},
		Embed: envcheck.CheckInput{Available: true, CheckResult: envcheck.CheckResult{
			State: "bad", Note: "找不到模型文件：" + modelPath + "\n第二行",
			Next: "把模型放到 embed.modelsRoot 或 <数据目录>/models 里，修好以后重启接收",
		}},
		Kernel:  envcheck.CheckInput{Available: true, CheckResult: envcheck.CheckResult{State: "ok"}},
		Plugins: envcheck.PluginsInput{Available: true},
		MCP:     envcheck.MCPInput{CheckInput: envcheck.CheckInput{Available: true, CheckResult: envcheck.CheckResult{State: "ok"}}},
		Analyzer: envcheck.AnalyzerInput{Available: true, OK: true,
			Path: `C:\Program Files\PowerShell\7\pwsh.exe`},
		Backup: envcheck.BackupInput{Available: true,
			Daily: envcheck.CheckResult{State: "ok"}, Drill: envcheck.CheckResult{State: "ok"}},
		Tools: envcheck.ToolsInput{Checked: true, Results: []envcheck.ToolResult{
			{Name: "pwsh", Purpose: "命令分析器", Found: true, Path: `C:\Program Files\PowerShell\7\pwsh.exe`, Source: "path"},
			{Name: "powershell", Purpose: "执行工具的 PowerShell", Found: true, Path: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, Source: "system"},
			{Name: "rg", Required: true, Purpose: "搜索", Found: true, Path: `D:\Sumika\vendor\bin\rg.exe`, Source: "bundled"},
		}},
		Paths: envcheck.PathsInput{Root: `D:\Sumika`},
	}))

	code, out := doctorRun(t, snap, "doctor")
	t.Logf("doctor 输出（验收第 2 条）：\n%s", out)
	if code != 1 {
		t.Fatalf("有“有问题”时退出码应是 1，得到 %d：\n%s", code, out)
	}
	for _, want := range []string{
		"有问题 本地嵌入：找不到模型文件：" + modelPath,
		"下一步：把模型放到 embed.modelsRoot",
		"有问题 密钥库：密钥库锁着",
		"下一步：用 sumika-ctl secrets unlock",
		"有问题 QQ 账号：",
		"下一步：用 sumika-ctl secrets unlock",
		"3 项有问题、0 项要注意",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里应有 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "第二行") {
		t.Errorf("详情只该有一行：\n%s", out)
	}
}
