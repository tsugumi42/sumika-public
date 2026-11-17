package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/envcheck"
)

// 规格-环境自检 设计第 5 条：运行页“状态”里只列不是正常的项，下一步折叠在那一行下面。

func envItemOf(id, name string, state envcheck.State, detail, next, affects string) envcheck.Item {
	return envcheck.Item{ID: id, Name: name, State: state, Detail: detail, Next: next, Affects: affects}
}

func envSnapshot(items ...envcheck.Item) envcheck.Snapshot {
	return envcheck.Snapshot{At: time.Unix(1, 0), Mode: envcheck.ModeStopped, Items: items}
}

// envStatusApp 是只带环境快照和后台状态的 app：statusBody 的“现在”一组照常画，环境一组读 a.host.Env。
func envStatusApp(snap envcheck.Snapshot) *app {
	return &app{host: hostState{Phase: phaseBlocked, Reason: "后台没在运行：测试", Env: snap}}
}

func envStatusView(a *app, w int) *ui.Tester {
	return testerOf(w, 3000, func(c *ui.Context) { a.statusBody(c) })
}

// 有问题、要注意、不知道各一项：三行都在、顺序照快照，正常的不出现；点开一项出现下一步和影响。
func TestEnvStatusListsProblemsInOrder(t *testing.T) {
	a := envStatusApp(envSnapshot(
		envItemOf("env.secrets", "密钥库", envcheck.OK, "解开了", "", "账号：能开 QQ"),
		envItemOf("env.analyzer", "命令分析器", envcheck.Bad, "命令分析器没起来：PATH 里没有 pwsh", "装 PowerShell 7（pwsh 进 PATH）后重启 Sumika", "审批：每条命令都会问你，检查点不拍"),
		envItemOf("env.tools", "外部程序", envcheck.Warn, "登记了 7 个外部程序，1 个没找到", "装 ffmpeg", "外部程序：个别可选功能用不了"),
		envItemOf("env.kernel", "后台内核", envcheck.Unknown, "后台没开，不知道内核起没起来", "启动接收后重查", "后台：不知道命令能不能跑"),
		envItemOf("env.platform", "QQ 账号", envcheck.NA, "没有填号", "", "账号：不用 QQ"),
	))
	tt := envStatusView(a, 1100)

	for _, want := range []string{"命令分析器", "外部程序", "后台内核", "有问题", "要注意", "不知道"} {
		if !tt.HasText(want) {
			t.Errorf("环境一组缺 %q：%q", want, tt.Texts())
		}
	}
	for _, absent := range []string{"密钥库", "QQ 账号"} {
		if tt.HasText(absent) {
			t.Errorf("正常项 %q 不该列出来：%q", absent, tt.Texts())
		}
	}
	if tt.HasText("下一步：") {
		t.Error("没点开时不该出现下一步")
	}

	texts := tt.Texts()
	pos := func(s string) int {
		for i, text := range texts {
			if text == s {
				return i
			}
		}
		return -1
	}
	bad, warn, unknown := pos("有问题"), pos("要注意"), pos("不知道")
	if !(bad >= 0 && bad < warn && warn < unknown) {
		t.Errorf("顺序该照快照（有问题→要注意→不知道），实际位置 %d/%d/%d：%q", bad, warn, unknown, texts)
	}

	if err := tt.Click("命令分析器"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("下一步：装 PowerShell 7（pwsh 进 PATH）后重启 Sumika") {
		t.Errorf("点开后没有下一步：%q", tt.Texts())
	}
	if !tt.HasText("影响：审批：每条命令都会问你，检查点不拍") {
		t.Errorf("点开后没有影响：%q", tt.Texts())
	}

	// 宿主重算后换一份快照：界面下一帧就该是新清单，不靠轮询。
	a.host.Env = envSnapshot(envItemOf("env.secrets", "密钥库", envcheck.Bad, "密钥库锁着", "sumika-ctl secrets unlock", "账号：开不了 QQ"))
	tt.Frame()
	if !tt.HasText("密钥库锁着") {
		t.Errorf("快照换了以后没显示新详情：%q", tt.Texts())
	}
	if tt.HasText("命令分析器") {
		t.Errorf("快照换了以后旧项还在：%q", tt.Texts())
	}
}

// 外部程序：展开后只列没找到的子项。
func TestEnvStatusToolSubitems(t *testing.T) {
	tools := envItemOf("env.tools", "外部程序", envcheck.Warn, "登记了 4 个外部程序，1 个没找到", "装 PowerShell 7（pwsh 进 PATH）", "外部程序：个别可选功能用不了")
	tools.Sub = []envcheck.SubItem{
		{Name: "pwsh", State: envcheck.Warn, Detail: "没找到，影响：命令分析器", Next: "装 PowerShell 7"},
		{Name: "rg", State: envcheck.OK, Detail: "C:/tools/rg.exe（PATH）"},
		{Name: "powershell", State: envcheck.OK, Detail: "C:/Windows/System32/WindowsPowerShell/v1.0/powershell.exe（PATH）"},
		{Name: "nightly", State: envcheck.NA, Detail: "只在开发机上有"},
	}
	a := envStatusApp(envSnapshot(tools))
	tt := envStatusView(a, 1100)

	if tt.HasText("pwsh") {
		t.Fatalf("没点开时不该列子项：%q", tt.Texts())
	}
	if err := tt.Click("外部程序"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("pwsh") {
		t.Errorf("展开后该看到没找到的 pwsh：%q", tt.Texts())
	}
	if !tt.HasText("没找到，影响：命令分析器") {
		t.Errorf("展开后该看到 pwsh 的详情：%q", tt.Texts())
	}
	for _, found := range []string{"rg", "powershell", "nightly"} {
		if tt.HasText(found) {
			t.Errorf("找到或不适用（%q）的子项不该列：%q", found, tt.Texts())
		}
	}
}

// 全部正常：只有一行汇总。
func TestEnvStatusAllOK(t *testing.T) {
	var items []envcheck.Item
	for i := range 13 {
		items = append(items, envItemOf(fmt.Sprintf("env.%d", i), fmt.Sprintf("检查 %d", i), envcheck.OK, "正常", "", "没有影响"))
	}
	a := envStatusApp(envSnapshot(items...))
	tt := envStatusView(a, 1100)

	if !tt.HasText("环境检查都正常（13 项）") {
		t.Errorf("全正常时该只有一行汇总：%q", tt.Texts())
	}
	for _, absent := range []string{"有问题", "要注意", "不知道", "检查 0"} {
		if tt.HasText(absent) {
			t.Errorf("全正常时不该出现 %q：%q", absent, tt.Texts())
		}
	}
}

// 快照还没生成：一行“环境检查还没跑完”。
func TestEnvStatusNotReady(t *testing.T) {
	a := envStatusApp(envcheck.Snapshot{})
	tt := envStatusView(a, 1100)

	if !tt.HasText("环境检查还没跑完") {
		t.Errorf("快照还没生成时该写还没跑完：%q", tt.Texts())
	}
	if tt.HasText("环境检查都正常") {
		t.Errorf("还没跑完时不该写成都正常：%q", tt.Texts())
	}
}

// 很长的详情（200 字符）一行放不下就截断，不折行、不撑出窗口；记下三个宽度量到的数。
func TestEnvStatusLongDetailFitsWidth(t *testing.T) {
	long := strings.Repeat("很长的环境详情和路径", 20) + "尾巴四个字" // 10×20+4 = 204 字
	a := envStatusApp(envSnapshot(
		envItemOf("env.paths", "数据位置", envcheck.Warn, long, "改成相对数据目录的路径", "数据：换电脑后这些路径指不到"),
	))
	for _, w := range []int{2048, 1100, 900} {
		tt := testerOf(w, 3000, func(c *ui.Context) {
			ui.Column(c).Padding(20, 0).MaxWidth(860).Children(func() { a.statusBody(c) })
		})
		name, ok := tt.Find("数据位置")
		if !ok {
			t.Fatalf("%d 宽：找不到名字：%q", w, tt.Texts())
		}
		detail, ok := tt.Find(long)
		if !ok {
			t.Fatalf("%d 宽：找不到详情：%q", w, tt.Texts())
		}
		if detail.H > name.H*1.4 {
			t.Errorf("%d 宽：详情折行了，高 %.1f（名字一行 %.1f）", w, detail.H, name.H)
		}
		if detail.X+detail.W > float32(w) {
			t.Errorf("%d 宽：详情撑出窗口，右边缘 %.1f", w, detail.X+detail.W)
		}
		t.Logf("%d 宽：详情宽 %.1f、高 %.1f（名字一行 %.1f），右边缘 %.1f（窗口 %d）", w, detail.W, detail.H, name.H, detail.X+detail.W, w)
	}
}
