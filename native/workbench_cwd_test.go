package main

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/llm/llmtest"
)

// 工作目录药丸（规格-按会话选工作目录）：显示项目名，菜单里换目录、恢复默认，目录没了标出来，审批卡带目录。

func cwdKey() api.ConversationKey {
	return api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:char00"}
}

func TestWorkbenchCwdPill(t *testing.T) {
	h := wbUI(t, llmtest.Script{}, nil)
	ws, ok := h.a.reader.(api.LocalWorkspaces)
	if !ok {
		t.Fatal("后台应当提供 LocalWorkspaces")
	}
	st := &h.a.workbench.shell.cwd
	h.a.workbench.shell.st.Panes.SideOpen, h.a.workbench.shell.sideTab = true, 1 // 工作区药丸在右栏“上下文”栏顶部（ADR-351）
	h.tt.Frame()
	defaultName := filepath.Base(h.workdir)
	h.until(t, "药丸显示默认目录名", func() bool { return h.tt.HasText(defaultName) })
	if st.note != "" {
		t.Fatalf("读目录出错：%s", st.note)
	}

	project := filepath.Join(t.TempDir(), "编的项目")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetLocalWorkspace(context.Background(), cwdKey(), project); err != nil {
		t.Fatal(err)
	}
	// 点开菜单会重读：药丸换成项目名
	h.click(t, defaultName)
	h.until(t, "药丸换成项目名", func() bool { return st.info.Bound && h.tt.HasText("编的项目") })
	if !h.tt.HasText("恢复默认目录") {
		t.Fatalf("菜单里要有恢复默认：%q", h.tt.Texts())
	}
	// 菜单规则：图标要么都有要么都没有。这个菜单整个不放图标，几项的文字左边对齐
	if a, b := menuTextX(h.tt, "选择文件夹…"), menuTextX(h.tt, "恢复默认目录"); a < 0 || abs32f(a-b) > 0.5 {
		t.Fatalf("菜单项文字该左对齐：选择文件夹 %.1f，恢复默认 %.1f", a, b)
	}
	h.click(t, "恢复默认目录")
	h.until(t, "恢复默认", func() bool { return !st.info.Bound && !st.busy && h.tt.HasText(defaultName) })

	// 目录没了：药丸标出来，不退回默认
	if _, err := ws.SetLocalWorkspace(context.Background(), cwdKey(), project); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(project); err != nil {
		t.Fatal(err)
	}
	st.asked = false
	h.until(t, "目录用不了的提示", func() bool { return st.info.Problem != "" && h.tt.HasText("目录用不了：编的项目") })
}

func TestWorkbenchApprovalCardShowsWorkdir(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"purpose": "写界面测试文件", "path": "wb-cwd.txt", "content": "编造内容"})
	h := wbUI(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("wb-cwd", "write", string(args)), llmtest.Text("完成")}}, nil)
	project := t.TempDir()
	if _, err := h.a.reader.(api.LocalWorkspaces).SetLocalWorkspace(context.Background(), cwdKey(), project); err != nil {
		t.Fatal(err)
	}
	h.wbSend(t, "写这个临时文件")
	h.until(t, "审批卡带目录", func() bool { return h.tt.HasText("允许一次") && h.tt.HasText("目录："+project) })
	h.click(t, "拒绝")
	h.until(t, "收尾", func() bool { return !h.tt.HasText("允许一次") })
}

// 设 CWD_SHOT=目录 时截一张：审批卡带目录、输入框底行的目录药丸（带菜单）；平时跳过。
func TestShotCwd(t *testing.T) {
	dir := os.Getenv("CWD_SHOT")
	if dir == "" {
		t.Skip("设 CWD_SHOT 才出图")
	}
	args, _ := json.Marshal(map[string]string{"purpose": "写界面测试文件", "path": "wb-cwd.txt", "content": "编造内容"})
	h := wbUI(t, llmtest.Script{Replies: []llmtest.Reply{llmtest.Call("wb-shot", "write", string(args)), llmtest.Text("完成")}}, nil)
	project := filepath.Join(t.TempDir(), "编的项目")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := h.a.reader.(api.LocalWorkspaces).SetLocalWorkspace(context.Background(), cwdKey(), project); err != nil {
		t.Fatal(err)
	}
	h.wbSend(t, "写这个临时文件")
	h.until(t, "审批卡带目录", func() bool { return h.tt.HasText("允许一次") && h.tt.HasText("目录：") })
	h.click(t, filepath.Base(h.workdir)) // 点开药丸：重读后换成项目名，菜单带出最近目录
	h.until(t, "药丸换成项目名", func() bool { return h.a.workbench.shell.cwd.info.Bound && h.tt.HasText("恢复默认目录") })
	h.tt.Frame()
	img := h.tt.Image()
	b := img.Bounds()
	r := image.Rect(int(376*1.25), b.Dy()/2, b.Dx(), b.Dy())
	f, _ := os.Create(filepath.Join(dir, "cwd-pill-approval.png"))
	png.Encode(f, img.SubImage(r))
	f.Close()
}

// 会话列表：选了项目目录的会话在预览前写项目名；没选的不写；搜项目名能搜到。
func TestWorkbenchListShowsProject(t *testing.T) {
	h := wbUI(t, llmtest.Script{}, nil)
	project := filepath.Join(t.TempDir(), "列表里的项目")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := h.a.reader.(api.LocalWorkspaces).SetLocalWorkspace(context.Background(), cwdKey(), project); err != nil {
		t.Fatal(err)
	}
	h.until(t, "列表带出项目名", func() bool { return h.tt.HasText("列表里的项目 · ") })
}

// menuTextX 是界面上这行字（文字元素本身，不是整个菜单项）的左边；找不到返回 -1。
func menuTextX(tt *ui.Tester, text string) float32 {
	for _, b := range uiBoxes(tt) {
		if b.Text == text {
			return b.Box.X
		}
	}
	return -1
}
