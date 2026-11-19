package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// runScene 在场景面板里点一个过程场景（和主人手点同一条路），收起面板，回到聊天。
func runScene(t *testing.T, a *app, tt *ui.Tester, label string) {
	t.Helper()
	a.Scenes = true
	tt.Frame()
	click(t, tt, label)
	a.Scenes = false
	tt.Frame()
}

func allText(tt *ui.Tester) string { return strings.Join(tt.Texts(), "\n") }

// 端到端：要批准的命令从场景面板跑起来，过程气泡自动展开、摘要行写"想运行"和剩余时间；点"允许一次"以后跑完、气泡收起成总结、回复接上（ADR-287）。
func TestViewApproveCommand(t *testing.T) {
	a := newApp()
	tt := tester(a)
	runScene(t, a, tt, "要批准的命令")
	tt.Advance(time.Second)
	// 2026-11-15 审批卡改版：摘要行只写“想运行命令”，命令原文只在终端块里出现一次（chatui TestAskCardLayers）
	if !tt.HasText("想运行命令") || !strings.Contains(allText(tt), "rm -rf build") || !tt.HasText("允许一次") || !tt.HasText("清掉旧的构建，下一步重新编译。") || !tt.HasText("4:29") {
		t.Fatalf("等批准的卡不全：%v", tt.Texts())
	}
	click(t, tt, "允许一次")
	tt.Advance(3 * time.Second)
	all := allText(tt)
	if !strings.Contains(all, "运行了 1 条命令") || strings.Contains(all, "允许一次") || !strings.Contains(all, "好了，我们接着聊。") {
		t.Fatalf("批准以后应该收起成总结：%q", all)
	}
	click(t, tt, "过程摘要") // 点开看细节
	tt.Frame()
	tt.Frame()
	if all = allText(tt); !strings.Contains(all, "运行了 rm -rf build") || !strings.Contains(all, "已允许") { // 结果是行尾小图标加“已允许”（审批卡改版）
		t.Fatalf("点开以后：%q", all)
	}
}

// 命令失败：过程气泡收起成总结、挂失败角标；点开以后命令块默认展开、角标 + 退出码、部分输出留着，读搜行在前面。
func TestViewCommandFails(t *testing.T) {
	a := newApp()
	tt := tester(a)
	runScene(t, a, tt, "命令失败")
	tt.Advance(4 * time.Second)
	if !tt.HasText("读了 1 个文件 · 运行了 1 条命令") || !tt.HasText("出错了") {
		t.Fatalf("收起时的总结或失败角标不对：%v", tt.Texts())
	}
	click(t, tt, "过程摘要")
	tt.Frame()
	tt.Frame()
	all := allText(tt)
	for _, want := range []string{"读了 notes/纸灯.md", "运行了 go test ./lamp/...", "退出码 1", "--- FAIL: TestLamp (0.01s)", "好了，我们接着聊。"} {
		if !strings.Contains(all, want) {
			t.Errorf("没看到 %q：%q", want, all)
		}
	}
}

// 很慢的命令被取消：进行中摘要写"正在运行"，取消后收起成总结、点开头部"停了"；审批过期：总结写"没运行"，按钮没了。
func TestViewCancelAndExpire(t *testing.T) {
	a := newApp()
	tt := tester(a)
	runScene(t, a, tt, "很慢的命令被取消")
	tt.Advance(2 * time.Second)
	if !tt.HasText("正在运行 make all") {
		t.Fatalf("很慢的命令没在跑：%v", tt.Texts())
	}
	tt.Advance(4 * time.Second)
	click(t, tt, "过程摘要")
	tt.Frame()
	tt.Frame()
	if !tt.HasText("停了 make all") {
		t.Errorf("取消以后点开，头部不对：%v", tt.Texts())
	}
	runScene(t, a, tt, "审批过期")
	tt.Advance(9 * time.Second)
	all := allText(tt)
	if !strings.Contains(all, "1 条命令没运行") || strings.Contains(all, "允许一次") {
		t.Errorf("过期以后：%q", all)
	}
}

// 设置"进行中展开过程"开、关两种（ADR-286）：关着时进行中只有一行"正在想"，开着时正文边想边出现；想完都收成"想了 N 秒"。
func TestViewLiveOpenSetting(t *testing.T) {
	for _, live := range []bool{false, true} {
		a := newApp()
		tt := tester(a)
		if live {
			a.Page = "settings"
			tt.Frame()
			sw := geometryTree(tt).Find("live-open") // 点开关本身（旁边同名的字不是开关）
			if sw == nil {
				t.Fatal("设置里没有\"进行中展开过程\"开关")
			}
			tt.ClickAt(sw.Box.X+sw.Box.W/2, sw.Box.Y+sw.Box.H/2)
			a.Page = "local"
			tt.Frame()
		}
		if a.LiveOpen != live {
			t.Fatalf("设置开关没生效：%v", a.LiveOpen)
		}
		runScene(t, a, tt, "想一会儿再回")
		tt.Advance(time.Second)
		shown := strings.Contains(allText(tt), "先想想昨天说到哪了")
		if !tt.HasText("正在想") || shown != live {
			t.Errorf("展开=%v 进行中：正文显示 %v；%v", live, shown, tt.Texts())
		}
		tt.Advance(4 * time.Second)
		if !tt.HasText("想了 3 秒") || strings.Contains(allText(tt), "先想想昨天说到哪了") {
			t.Errorf("展开=%v 想完以后：%v", live, tt.Texts())
		}
	}
}

// 标题栏的状态：思考时是"正在想"（不是"正在查资料"），等批准时是"在等你批准"。
func TestViewStatusDuringProcess(t *testing.T) {
	a := newApp()
	tt := tester(a)
	runScene(t, a, tt, "想一会儿再回")
	tt.Advance(time.Second)
	if !tt.HasText("晴枝正在想") || tt.HasText("晴枝正在查资料") {
		t.Errorf("思考时状态不对：%v", tt.Texts())
	}
	tt.Advance(5 * time.Second)
	runScene(t, a, tt, "要批准的命令")
	tt.Advance(time.Second)
	if !tt.HasText("晴枝在等你批准") {
		t.Errorf("等批准时状态不对：%v", tt.Texts())
	}
}
