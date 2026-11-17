package envcheck

import (
	"strings"
	"testing"
	"time"
)

type itemCase struct {
	name       string
	item       Item
	want       State
	detailHas  string
	nextHas    string
	affectsHas string
}

var t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local)

func res(state, note, next string) CheckResult {
	return CheckResult{State: state, Note: note, Next: next}
}

func allItemCases() []itemCase {
	return []itemCase{
		{name: "密钥：没有文件且不用令牌", item: Secrets(SecretsInput{}), want: NA},
		{name: "密钥：没有文件但要令牌", item: Secrets(SecretsInput{NeedToken: true, Account: "12345678"}), want: Bad, detailHas: "***78", nextHas: "sumika-ctl secrets set qq.<号>.token"},
		{name: "密钥：锁着", item: Secrets(SecretsInput{Exists: true, NeedToken: true, Account: "1234", Note: "需要主密码或恢复码"}), want: Bad, detailHas: "密钥库锁着", nextHas: "sumika-ctl secrets unlock"},
		{name: "密钥：没有本机副本", item: Secrets(SecretsInput{Exists: true, Unlocked: true, HasLocalCopy: false}), want: Warn, nextHas: "sumika-ctl secrets unlock"},
		{name: "密钥：正常", item: Secrets(SecretsInput{Exists: true, Unlocked: true, HasLocalCopy: true}), want: OK},
		{name: "密钥：读状态失败", item: Secrets(SecretsInput{ReadErr: "permission denied"}), want: Unknown, nextHas: "重启 Sumika"},

		{name: "QQ：没开", item: Platform(PlatformInput{Enabled: false}), want: NA},
		{name: "QQ：没有账号", item: Platform(PlatformInput{Enabled: true}), want: Bad, nextHas: "设置 · 平台账号"},
		{name: "QQ：缺地址和令牌", item: Platform(PlatformInput{Enabled: true, Accounts: []PlatformAccount{
			{ID: "12345678", URL: "ws://127.0.0.1:1", HasToken: true},
			{ID: "87654321", URL: "", HasToken: false},
		}}), want: Bad, detailHas: "***21", nextHas: "sumika-ctl secrets set"},
		{name: "QQ：没写号", item: Platform(PlatformInput{Enabled: true, Accounts: []PlatformAccount{{URL: "ws://127.0.0.1:1", HasToken: true}}}), want: Bad, detailHas: "没写号"},
		{name: "QQ：密钥库锁着", item: Platform(PlatformInput{Enabled: true, Locked: true, Accounts: []PlatformAccount{{ID: "12345678", URL: "ws://127.0.0.1:1"}}}), want: Bad, detailHas: "密钥库锁着", nextHas: "sumika-ctl secrets unlock"},
		{name: "QQ：齐全", item: Platform(PlatformInput{Enabled: true, Accounts: []PlatformAccount{{ID: "12345678", URL: "ws://127.0.0.1:1", HasToken: true}}}), want: OK},

		{name: "模型来源：没接", item: ModelsGateway(GatewayInput{}), want: Unknown, detailHas: "后台没开", nextHas: "启动接收"},
		{name: "模型来源：没接且有原因", item: ModelsGateway(GatewayInput{Reason: "开始接收失败：密钥库锁着"}), want: Unknown, detailHas: "开始接收失败"},
		{name: "模型来源：连不上", item: ModelsGateway(GatewayInput{Available: true, Source: "http://127.0.0.1:3425", Err: "connection refused"}), want: Bad, detailHas: "connection refused", nextHas: "打开 Magpie"},
		{name: "模型来源：还没读到目录", item: ModelsGateway(GatewayInput{Available: true, Source: "http://127.0.0.1:3425"}), want: Unknown, nextHas: "打开 Magpie"},
		{name: "模型来源：正常", item: ModelsGateway(GatewayInput{Available: true, Source: "http://127.0.0.1:3425", ReadAt: t0}), want: OK, detailHas: "127.0.0.1:3425"},

		{name: "资料库：没接", item: ModelsCatalog(CatalogInput{}), want: Unknown, detailHas: "后台没开", nextHas: "启动接收"},
		{name: "资料库：关着", item: ModelsCatalog(CatalogInput{Available: true, Enabled: false}), want: NA},
		{name: "资料库：取不到", item: ModelsCatalog(CatalogInput{Available: true, Enabled: true, Entries: 20, Err: "timeout"}), want: Warn, detailHas: "旧缓存", nextHas: "联网"},
		{name: "资料库：用缓存", item: ModelsCatalog(CatalogInput{Available: true, Enabled: true, Entries: 20, FromCache: true}), want: Warn, nextHas: "联网"},
		{name: "资料库：没缓存", item: ModelsCatalog(CatalogInput{Available: true, Enabled: true}), want: Warn, detailHas: "还没有"},
		{name: "资料库：正常", item: ModelsCatalog(CatalogInput{Available: true, Enabled: true, Entries: 20, FetchedAt: t0}), want: OK, detailHas: "20 条"},

		{name: "嵌入：没接", item: Embed(CheckInput{}), want: Unknown, detailHas: "后台没开", nextHas: "启动接收"},
		{name: "嵌入：打开失败的原因", item: Embed(CheckInput{Reason: "打开接收失败：密钥库锁着"}), want: Unknown, detailHas: "密钥库锁着"},
		{name: "嵌入：关着", item: Embed(CheckInput{Available: true, CheckResult: res("ok", "嵌入关着（embed.enabled: false）", "")}), want: OK, detailHas: "嵌入关着"},
		{name: "嵌入：就绪", item: Embed(CheckInput{Available: true, CheckResult: res("ok", "", "")}), want: OK, detailHas: "就绪"},
		{name: "嵌入：缺模型", item: Embed(CheckInput{Available: true, CheckResult: res("bad", "找不到模型文件：D:\\数据\\models\\x.onnx\n第二行", "把模型放到它在找的目录里，修好以后重启接收")}), want: Bad, detailHas: "D:\\数据\\models\\x.onnx", nextHas: "重启接收"},
		{name: "嵌入：启动中", item: Embed(CheckInput{Available: true, CheckResult: res("unknown", "嵌入还在启动", "等它启动完")}), want: Unknown, detailHas: "还在启动", nextHas: "等它启动"},
		{name: "嵌入：bad 没有下一步时兜底", item: Embed(CheckInput{Available: true, CheckResult: res("bad", "起不来", "")}), want: Bad, nextHas: "embed.modelsRoot"},

		{name: "内核：没接", item: Kernel(CheckInput{}), want: Unknown, detailHas: "后台没开", nextHas: "启动接收"},
		{name: "内核：正常", item: Kernel(CheckInput{Available: true, CheckResult: res("ok", "", "")}), want: OK},
		{name: "内核：整个停止受理", item: Kernel(CheckInput{Available: true, CheckResult: CheckResult{State: "bad", Note: "整个内核停止受理新输入", Next: "运行 sumika-ctl kernel retry 重试", Example: map[string]string{"scope": "kernel", "kind": "startup_recover"}}}), want: Bad, detailHas: "startup_recover", nextHas: "kernel retry"},
		{name: "内核：一个会话停止受理", item: Kernel(CheckInput{Available: true, CheckResult: CheckResult{State: "bad", Note: "有会话停止受理新输入，别的会话照常", Example: map[string]string{"scope": "conversation", "conversation": "console:c1", "kind": "run_state"}}}), want: Bad, detailHas: "console:c1", nextHas: "kernel retry"},
		{name: "内核：unknown", item: Kernel(CheckInput{Available: true, CheckResult: res("unknown", "内核状态还没读到", "")}), want: Unknown, nextHas: "kernel retry"},

		{name: "插件：没接", item: Plugins(PluginsInput{}), want: Unknown, detailHas: "后台没开", nextHas: "启动接收"},
		{name: "插件：正常", item: Plugins(PluginsInput{Available: true}), want: OK},
		{name: "插件：启用失败", item: Plugins(PluginsInput{Available: true, Failed: []PluginFailure{{ID: "memory", Err: "bad config"}}}), want: Bad, detailHas: "memory", nextHas: "设置 · 插件"},

		{name: "MCP：没接", item: MCP(MCPInput{}), want: Unknown, detailHas: "后台没开", nextHas: "启动接收"},
		{name: "MCP：正常", item: MCP(MCPInput{CheckInput: CheckInput{Available: true, CheckResult: res("ok", "", "")}}), want: OK},
		{name: "MCP：被隔离", item: MCP(MCPInput{CheckInput: CheckInput{Available: true, CheckResult: res("bad", "有 MCP 服务被隔离", "用 sumika-ctl mcp status 看原因")}}), want: Bad, detailHas: "被隔离", nextHas: "mcp status"},
		{name: "MCP：从没连上", item: MCP(MCPInput{CheckInput: CheckInput{Available: true, CheckResult: res("ok", "", "")}, NeverConnected: []NeverConnectedService{{Name: "files", Cause: "exec: not found"}}}), want: Bad, detailHas: "files", nextHas: "命令和参数"},
		{name: "MCP：隔离加从没连上", item: MCP(MCPInput{CheckInput: CheckInput{Available: true, CheckResult: res("bad", "docs 被隔离", "用 mcp release 解除")}, NeverConnected: []NeverConnectedService{{Name: "files"}}}), want: Bad, detailHas: "files", nextHas: "mcp release"},
		{name: "MCP：s11 不知道", item: MCP(MCPInput{CheckInput: CheckInput{Available: true, CheckResult: res("unknown", "没有运行记录库", "")}}), want: Unknown, nextHas: "mcp status"},
		{name: "MCP：读不到隔离记录时不把从没连上的报成 bad", item: MCP(MCPInput{CheckInput: CheckInput{Available: true, CheckResult: res("unknown", "没有运行记录库", "")}, NeverConnected: []NeverConnectedService{{Name: "files"}}}), want: Unknown, detailHas: "分不清是没连上还是被隔离", nextHas: "mcp status"},

		{name: "分析器：没接", item: Analyzer(AnalyzerInput{}), want: Unknown, detailHas: "后台没开", nextHas: "启动接收"},
		{name: "分析器：起不来", item: Analyzer(AnalyzerInput{Available: true, LastErr: "exec: pwsh: executable file not found"}), want: Warn, detailHas: "exec: pwsh", nextHas: "PowerShell 7"},
		{name: "分析器：PATH 里没有", item: Analyzer(AnalyzerInput{Available: true}), want: Warn, detailHas: "PATH 里没有 pwsh", nextHas: "PowerShell 7"},
		{name: "分析器：可用", item: Analyzer(AnalyzerInput{Available: true, OK: true, Path: "C:\\Program Files\\PowerShell\\7\\pwsh.exe"}), want: OK, detailHas: "pwsh.exe"},

		{name: "备份：没接", item: Backup(BackupInput{}), want: Unknown, detailHas: "后台没开", nextHas: "启动接收"},
		{name: "备份：都正常", item: Backup(BackupInput{Available: true, Daily: res("ok", "每天备份正常", ""), Drill: res("ok", "每周抽验正常", "")}), want: OK, detailHas: "每天备份正常"},
		{name: "备份：每天失败", item: Backup(BackupInput{Available: true, Daily: res("bad", "最近一次每天备份失败：写不进去", "运行 sumika-ctl backup now 重做一份"), Drill: res("ok", "每周抽验正常", "")}), want: Bad, detailHas: "写不进去", nextHas: "backup now"},
		{name: "备份：抽验过期", item: Backup(BackupInput{Available: true, Daily: res("ok", "每天备份正常", ""), Drill: res("bad", "备份抽验已经很久没有通过", "运行 sumika-ctl backup verify 再验一次")}), want: Bad, detailHas: "很久", nextHas: "backup verify"},
		{name: "备份：不知道", item: Backup(BackupInput{Available: true, Daily: res("unknown", "正在备份", ""), Drill: res("ok", "", "")}), want: Unknown, detailHas: "正在备份", nextHas: "backup list"},
		{name: "备份：两项都有问题时合成一条", item: Backup(BackupInput{Available: true, Daily: res("bad", "每天失败", "backup now"), Drill: res("bad", "抽验失败", "backup verify")}), want: Bad, detailHas: "抽验失败", nextHas: "backup now"},

		{name: "每日整套：不适用", item: Nightly(), want: NA, detailHas: "开发机"},

		{name: "外部程序：还没查", item: Tools(ToolsInput{}), want: Unknown, nextHas: "重查"},
		{name: "外部程序：必需缺", item: Tools(ToolsInput{Checked: true, Results: []ToolResult{{Name: "rg", Required: true, Purpose: "搜索"}, {Name: "pwsh", Purpose: "命令分析器", Found: true, Path: "C:\\pwsh.exe", Source: "path"}}}), want: Bad, detailHas: "没找到", nextHas: "vendor/bin"},
		{name: "外部程序：可选缺", item: Tools(ToolsInput{Checked: true, Results: []ToolResult{{Name: "rg", Required: true, Found: true, Path: "C:\\rg.exe", Source: "path"}, {Name: "ffmpeg", Purpose: "收视频"}}}), want: Warn, nextHas: "qq.media.ffmpeg"},
		{name: "外部程序：齐全", item: Tools(ToolsInput{Checked: true, Results: []ToolResult{{Name: "rg", Required: true, Found: true, Path: "C:\\rg.exe", Source: "path"}}}), want: OK},

		{name: "路径：不知道数据目录", item: Paths(PathsInput{}), want: Unknown, nextHas: "sumika.root"},
		{name: "路径：有绝对路径", item: Paths(PathsInput{Root: "D:\\Sumika", Outside: []PathValue{{Key: "embed.modelsRoot", Value: "E:\\models"}}}), want: Warn, detailHas: "E:\\models", nextHas: "相对数据目录"},
		{name: "路径：正常", item: Paths(PathsInput{Root: "D:\\Sumika", Source: "exe 旁边"}), want: OK, detailHas: "exe 旁边"},
	}
}

func TestItems(t *testing.T) {
	for _, tc := range allItemCases() {
		if tc.item.State != tc.want {
			t.Errorf("%s：想要 %s，实际 %+v", tc.name, tc.want, tc.item)
			continue
		}
		if tc.detailHas != "" && !strings.Contains(tc.item.Detail, tc.detailHas) {
			t.Errorf("%s：详情要含 %q，实际 %q", tc.name, tc.detailHas, tc.item.Detail)
		}
		if tc.nextHas != "" && !strings.Contains(tc.item.Next, tc.nextHas) {
			t.Errorf("%s：下一步要含 %q，实际 %q", tc.name, tc.nextHas, tc.item.Next)
		}
		if tc.affectsHas != "" && !strings.Contains(tc.item.Affects, tc.affectsHas) {
			t.Errorf("%s：影响要含 %q，实际 %q", tc.name, tc.affectsHas, tc.item.Affects)
		}
	}
}

// warn/bad/unknown 都必须有下一步和影响面；unknown 不能空着当正常。
func TestProblemStatesHaveNextAndAffects(t *testing.T) {
	for _, tc := range allItemCases() {
		switch tc.item.State {
		case Warn, Bad, Unknown:
			if tc.item.Next == "" {
				t.Errorf("%s：%s 没有下一步：%+v", tc.name, tc.item.State, tc.item)
			}
			if tc.item.Affects == "" {
				t.Errorf("%s：%s 没有影响面：%+v", tc.name, tc.item.State, tc.item)
			}
		}
	}
}

// env.tools 每个登记程序一个子项：找到的写路径和来源，没找到的写影响和下一步；整项取子项里最差的。
func TestToolsSubitems(t *testing.T) {
	it := Tools(ToolsInput{Checked: true, Results: []ToolResult{
		{Name: "rg", Required: true, Purpose: "搜索", Found: true, Path: `C:\vendor\rg.exe`, Source: "bundled", Version: "14.1.0"},
		{Name: "fd", Required: true, Purpose: "找文件"},
		{Name: "pwsh", Purpose: "命令分析器"},
		{Name: "powershell", Purpose: "执行工具的 PowerShell", Found: true, Path: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, Source: "system"},
	}})
	if it.State != Bad {
		t.Fatalf("必需程序缺了，整项应是 bad：%+v", it)
	}
	if len(it.Sub) != 4 {
		t.Fatalf("每个登记程序都要一个子项，得到 %d：%+v", len(it.Sub), it.Sub)
	}
	sub := func(name string) SubItem {
		t.Helper()
		for _, s := range it.Sub {
			if s.Name == name {
				return s
			}
		}
		t.Fatalf("没有 %s 的子项：%+v", name, it.Sub)
		return SubItem{}
	}
	if rg := sub("rg"); rg.State != OK || !strings.Contains(rg.Detail, `C:\vendor\rg.exe`) || !strings.Contains(rg.Detail, "自带，14.1.0") {
		t.Errorf("找到的程序要写路径、来源和版本：%+v", rg)
	}
	if fd := sub("fd"); fd.State != Bad || !strings.Contains(fd.Detail, "找文件") || !strings.Contains(fd.Next, "vendor/bin") {
		t.Errorf("必需的没找到：%+v", fd)
	}
	if pw := sub("pwsh"); pw.State != Warn || !strings.Contains(pw.Detail, "命令分析器") || !strings.Contains(pw.Next, "PowerShell 7") {
		t.Errorf("可选的没找到：%+v", pw)
	}
	if ps := sub("powershell"); ps.State != OK || !strings.Contains(ps.Detail, "系统目录") {
		t.Errorf("执行工具的 PowerShell 在：%+v", ps)
	}
	for _, s := range it.Sub {
		if s.State != OK && (s.Detail == "" || s.Next == "") {
			t.Errorf("子项 %s 是 %s，没有详情或下一步：%+v", s.Name, s.State, s)
		}
	}

	// 只有可选程序缺：整项 warn
	opt := Tools(ToolsInput{Checked: true, Results: []ToolResult{
		{Name: "rg", Required: true, Found: true, Path: `C:\rg.exe`, Source: "path"},
		{Name: "ffmpeg", Purpose: "收视频"},
	}})
	if opt.State != Warn || len(opt.Sub) != 2 || opt.Sub[1].State != Warn {
		t.Fatalf("只有可选程序缺应是 warn：%+v", opt)
	}
}

func TestBuildOrderAndCount(t *testing.T) {
	at := t0
	s := Build(Inputs{At: at, Mode: ModeArchive})
	if !s.At.Equal(at) || s.Mode != ModeArchive {
		t.Fatalf("快照头和输入不一致：%+v", s)
	}
	want := []string{
		"env.secrets", "env.platform", "env.models.gateway", "env.models.catalog",
		"env.embed", "env.kernel", "env.plugins", "env.mcp", "env.analyzer",
		"env.backup", "env.nightly", "env.tools", "env.paths",
	}
	if len(s.Items) != len(want) {
		t.Fatalf("项数：%d，要 %d", len(s.Items), len(want))
	}
	for i, id := range want {
		if s.Items[i].ID != id {
			t.Errorf("第 %d 项：%q，要 %q", i, s.Items[i].ID, id)
		}
	}
}

// 后台没开、什么都没给：运行部分记 unknown，基础部分按实际（没有密钥/没开 QQ/没查程序）记 na 或 unknown，不能报成正常。
func TestBuildWithoutRuntime(t *testing.T) {
	s := Build(Inputs{At: t0, Mode: ModeStopped})
	states := map[string]State{}
	for _, it := range s.Items {
		states[it.ID] = it.State
		if (it.State == Warn || it.State == Bad || it.State == Unknown) && (it.Next == "" || it.Affects == "") {
			t.Errorf("%s 是 %s，没有下一步或影响面：%+v", it.ID, it.State, it)
		}
	}
	for _, id := range []string{"env.models.gateway", "env.models.catalog", "env.embed", "env.kernel", "env.plugins", "env.mcp", "env.analyzer", "env.backup"} {
		if states[id] != Unknown {
			t.Errorf("%s 没给运行状态，该是 unknown，实际 %s", id, states[id])
		}
	}
	if states["env.nightly"] != NA {
		t.Errorf("每日整套该是 na，实际 %s", states["env.nightly"])
	}
}

// 号码按 config.MaskID 的规则打码，详情里不能出现完整号码。
func TestSecretsMasksAccount(t *testing.T) {
	it := Secrets(SecretsInput{NeedToken: true, Account: "acct90"})
	if strings.Contains(it.Detail, "acct90") || !strings.Contains(it.Detail, "***90") {
		t.Fatalf("号码没打码：%q", it.Detail)
	}
}

// env.backup 只呈递自检结果：S9 说 ok 就 ok，不按自己的阈值再判一次。
func TestBackupPresentsSelfcheckResult(t *testing.T) {
	it := Backup(BackupInput{Available: true,
		Daily: res("ok", "每天自动备份已关闭", ""),
		Drill: res("ok", "每周自动抽验已关闭", "")})
	if it.State != OK || !strings.Contains(it.Detail, "已关闭") {
		t.Fatalf("应照 S9/S12 的结果显示：%+v", it)
	}
}
