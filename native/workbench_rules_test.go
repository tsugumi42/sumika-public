package main

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// fakeRules 是编造的放行规则后台：列出、撤销（可以让它失败），不碰真实数据。
type fakeRules struct {
	api.Reader
	mu      sync.Mutex
	rows    []api.ApprovalRule
	revoked []string
	added   []string
	fail    error
	// removeThenFail 模拟 Grants.Remove：内存里先撤掉，写文件失败，返回错误。
	removeThenFail error
	scopes         []string // 每次添加收到的 项目|会话
}

func (f *fakeRules) ListApprovalRules(context.Context) ([]api.ApprovalRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.ApprovalRule(nil), f.rows...), nil
}

// AddApprovalRule 模拟后台：写法以"坏"开头的当写错了，其余照写进列表。
func (f *fakeRules) AddApprovalRule(_ context.Context, raw, effect, project, session string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(raw, "坏") {
		return errors.New("不认识的工具名 \"坏\"")
	}
	f.added = append(f.added, effect+" "+raw)
	f.scopes = append(f.scopes, project+"|"+session)
	f.rows = append(f.rows, api.ApprovalRule{Scope: "always", Key: "rule:" + effect + ":" + raw, Label: raw, Tool: "rule", Effect: effect, Pattern: raw, Project: project, Session: session, At: time.Now().UnixMilli()})
	return nil
}

func (f *fakeRules) RevokeApprovalRule(_ context.Context, r api.ApprovalRule) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	err := f.removeThenFail
	f.revoked = append(f.revoked, ruleID(r))
	kept := f.rows[:0:0]
	for _, x := range f.rows {
		if ruleID(x) != ruleID(r) {
			kept = append(kept, x)
		}
	}
	f.rows = kept
	return err
}

// rulesPage 是停在“设置 → 工具与审批”的新界面。后台调用的回投放进队列，由测试在界面线程上执行。
func rulesPage(t *testing.T, f *fakeRules) (*app, *ui.Tester, func(func() bool)) {
	t.Helper()
	a := shellApp()
	a.rail = 4
	a.reader = f
	posted := make(chan func(), 16)
	a.postFn = func(fn func()) { posted <- fn }
	tt := shellTester(a, 2048, 1030)
	click(t, tt, "工具与审批")
	// pump 放行后台回投的结果、画帧，直到条件成立；不按“安静了多久”判断，机器忙时后台协程可能晚很久才回来
	pump := func(cond func() bool) {
		t.Helper()
		remoteWait(t, "后台结果回到界面", func() bool {
			for {
				select {
				case fn := <-posted:
					fn()
					continue
				default:
				}
				break
			}
			tt.Frame()
			tt.Frame()
			return cond()
		})
	}
	pump(func() bool { return !a.rulesUI.loaded.IsZero() && !a.rulesUI.loading })
	return a, tt, pump
}

func ruleFixtures() []api.ApprovalRule {
	now := time.Now().UnixMilli()
	return []api.ApprovalRule{
		{Scope: "always", Key: "mcp:编的服务/编的工具", Label: "「编的服务 的 编的工具」", Tool: "mcp_call", At: now - 3*60*60*1000, LastUsed: now - 5*60*1000},
		{Scope: "always", Key: "task:reader", Label: "「派 reader 子任务」", Tool: "task", At: now - 3*24*60*60*1000},
		{Scope: "conversation", Key: "task:worker", Label: "「派 worker 子任务」", Tool: "task", Conversation: "console:r1", At: now - 60*1000},
	}
}

func hasTextPart(tt *ui.Tester, want string) bool {
	for _, s := range tt.Texts() {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// 看：记住和这个对话的规则都列出来，写明哪一档、什么时候记的、最近用过没有；没规则时有空态。
func TestRulesPageListsEverything(t *testing.T) {
	_, tt, _ := rulesPage(t, &fakeRules{})
	if !tt.HasText("放行规则") || !hasTextPart(tt, "还没有。审批卡上选") {
		t.Fatalf("空态没出来：%v", tt.Texts())
	}
	_, tt, _ = rulesPage(t, &fakeRules{rows: ruleFixtures()})
	for _, want := range []string{"「编的服务 的 编的工具」", "「派 reader 子任务」", "「派 worker 子任务」", "还没用过", "5 分钟前用过", "3 小时前记下", "3 天前记下", "1 分钟前记下", "这个对话 · "} {
		if !hasTextPart(tt, want) {
			t.Errorf("规则页缺 %q：%v", want, tt.Texts())
		}
	}
}

// 撤销：点一下就从列表拿掉，后台也撤了，别的规则不受影响。这个对话的规则一样能撤。
func TestRulesPageRevoke(t *testing.T) {
	f := &fakeRules{rows: ruleFixtures()}
	a, tt, pump := rulesPage(t, f)
	for _, key := range []string{"rule-conversation|console:r1|task:worker-revoke", "rule-always||task:reader-revoke"} {
		clickKey(t, tt, key)
		pump(func() bool { return len(a.rulesUI.revoking) == 0 })
	}
	f.mu.Lock()
	revoked, left := append([]string(nil), f.revoked...), len(f.rows)
	f.mu.Unlock()
	if len(revoked) != 2 || left != 1 {
		t.Fatalf("后台撤销记录 %v，剩 %d 条", revoked, left)
	}
	if hasTextPart(tt, "派 worker 子任务") || hasTextPart(tt, "派 reader 子任务") || !hasTextPart(tt, "编的服务 的 编的工具") {
		t.Fatalf("撤销后列表不对：%v", tt.Texts())
	}
}

// 撤销失败：照实说，行不消失，重读后台以它为准。
func TestRulesPageRevokeFailureKeepsRow(t *testing.T) {
	f := &fakeRules{rows: ruleFixtures(), fail: errors.New("编的写入失败")}
	a, tt, pump := rulesPage(t, f)
	clickKey(t, tt, "rule-always||task:reader-revoke")
	pump(func() bool { return a.rulesUI.revokeErr != "" })
	if !hasTextPart(tt, "撤销出了问题：编的写入失败") || !hasTextPart(tt, "派 reader 子任务") {
		t.Fatalf("失败要报出来、行要留着：%v", tt.Texts())
	}
}

// 撤销时后台内存里已经撤掉、但写文件失败：重读列表后这一行没了，失败提示不能跟着被清掉（重启后规则会回来）。
func TestRulesPageRevokeWriteFailureSurvivesReload(t *testing.T) {
	f := &fakeRules{rows: ruleFixtures(), removeThenFail: errors.New("编的写不进文件")}
	a, tt, pump := rulesPage(t, f)
	clickKey(t, tt, "rule-always||task:reader-revoke")
	pump(func() bool { return a.rulesUI.revokeErr != "" })
	a.rulesUI.loaded = time.Time{} // 再触发一次重读
	pump(func() bool { return !a.rulesUI.loading && !hasTextPart(tt, "派 reader 子任务") })
	if hasTextPart(tt, "派 reader 子任务") {
		t.Fatalf("前提不成立：重读后这一行应当没了：%v", tt.Texts())
	}
	if !hasTextPart(tt, "撤销出了问题：编的写不进文件") {
		t.Fatalf("写文件失败的提示被重读清掉了：%v", tt.Texts())
	}
}

// 写规则：输入框写一条、点"允许"或"拒绝"，后台收到、列表马上出现并标明"你写的规则 · 允许/拒绝"；写错的原样报出来。
func TestRulesPageAddRule(t *testing.T) {
	f := &fakeRules{rows: ruleFixtures()}
	a, tt, pump := rulesPage(t, f)
	clickKey(t, tt, "rule-add-text")
	tt.Type("Bash(git push:*)")
	tt.Frame()
	clickKey(t, tt, "rule-add-deny")
	pump(func() bool { return hasTextPart(tt, "你写的规则 · 拒绝") }) // 加上以后下一帧重读列表，新规则出现才算完
	f.mu.Lock()
	added := append([]string(nil), f.added...)
	f.mu.Unlock()
	if len(added) != 1 || added[0] != "deny Bash(git push:*)" {
		t.Fatalf("后台收到：%v", added)
	}
	if !hasTextPart(tt, "你写的规则 · 拒绝") || !hasTextPart(tt, "Bash(git push:*)") || a.rulesUI.draft != "" {
		t.Fatalf("列表没出现新规则或输入没清：%v 草稿 %q", tt.Texts(), a.rulesUI.draft)
	}
	clickKey(t, tt, "rule-add-text")
	tt.Type("坏的写法")
	tt.Frame()
	clickKey(t, tt, "rule-add-allow")
	pump(func() bool { return a.rulesUI.addErr != "" })
	if !hasTextPart(tt, "没加上：不认识的工具名") || a.rulesUI.draft != "坏的写法" {
		t.Fatalf("写错要报出来、输入要留着让人改：%v", tt.Texts())
	}
}

// “每次问”按钮和作用范围：项目目录一起交给后台，列表上写出“每次都问”和范围；
// 勾了“只对当前对话”但没有打开的对话，要说清楚、什么都不写。
func TestRulesPageAddAskRuleWithScope(t *testing.T) {
	f := &fakeRules{rows: ruleFixtures()}
	a, tt, pump := rulesPage(t, f)
	clickKey(t, tt, "rule-add-text")
	tt.Type("Write(src/**)")
	tt.Frame()
	clickKey(t, tt, "rule-add-project")
	tt.Type(`D:\Projects\demo`)
	tt.Frame()
	clickKey(t, tt, "rule-add-ask")
	pump(func() bool { return hasTextPart(tt, "你写的规则 · 每次都问") })
	f.mu.Lock()
	added, scopes := append([]string(nil), f.added...), append([]string(nil), f.scopes...)
	f.mu.Unlock()
	if len(added) != 1 || added[0] != "ask Write(src/**)" || scopes[0] != `D:\Projects\demo|` {
		t.Fatalf("后台收到：%v %v", added, scopes)
	}
	if !hasTextPart(tt, "你写的规则 · 每次都问") || !hasTextPart(tt, `只在项目 D:\Projects\demo`) || a.rulesUI.project != "" {
		t.Fatalf("列表没写出每次都问和范围，或范围输入没清：%v", tt.Texts())
	}
	// 打开着的是群，限定当前对话要说清楚、什么都不写
	a.sel = "qq:group:1"
	clickKey(t, tt, "rule-add-text")
	tt.Type("Write(x)")
	tt.Frame()
	clickKey(t, tt, "rule-add-conv")
	clickKey(t, tt, "rule-add-ask")
	pump(func() bool { return hasTextPart(tt, "只有打开着本地私聊") })
	f.mu.Lock()
	n := len(f.added)
	f.mu.Unlock()
	if n != 1 || !hasTextPart(tt, "只有打开着本地私聊") {
		t.Fatalf("不是本地私聊时不该写、要说清楚：%d %v", n, tt.Texts())
	}
	// 本地私聊：会话号交给后台
	a.sel = "local:r1"
	clickKey(t, tt, "rule-add-ask")
	pump(func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.scopes) == 2
	})
	f.mu.Lock()
	scopes = append([]string(nil), f.scopes...)
	f.mu.Unlock()
	if len(scopes) != 2 || scopes[1] != "|local:r1" {
		t.Fatalf("会话范围没交给后台：%v", scopes)
	}
}

// 可选截图（SUMIKA_APPROVAL_SHOT=<目录>）：设置 → 工具与审批的放行规则，浅色和夜间。普通测试不写图。
func TestRulesPageShot(t *testing.T) {
	dir := os.Getenv("SUMIKA_APPROVAL_SHOT")
	if dir == "" {
		t.Skip("没设 SUMIKA_APPROVAL_SHOT，不截图")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		_, tt, _ := rulesPage(t, &fakeRules{rows: append(ruleFixtures(), shotUserRules()...)})
		tt.SetDark(dark)
		tt.Frame()
		tt.Frame()
		name := "放行规则页-light.png"
		if dark {
			name = "放行规则页-night.png"
		}
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, tt.Image().SubImage(image.Rect(340, 0, 2560, 900)).(*image.RGBA))
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}

// shotUserRules 只给截图用：主人自己写的两条规则（一条允许、一条拒绝）和一条项目说明的记住。
func shotUserRules() []api.ApprovalRule {
	now := time.Now().UnixMilli()
	return []api.ApprovalRule{
		{Scope: "always", Key: "rule:allow:Bash(git status:*)", Label: "Bash(git status:*)", Tool: "rule", Effect: "allow", Pattern: "Bash(git status:*)", At: now - 2*60*60*1000, LastUsed: now - 90*1000},
		{Scope: "always", Key: "rule:deny:Bash(git push:*)", Label: "Bash(git push:*)", Tool: "rule", Effect: "deny", Pattern: "Bash(git push:*)", At: now - 26*60*60*1000},
		{Scope: "always", Key: "project:d:/编的项目", Label: "「D:/编的项目 的项目说明」", Tool: "project_trust", At: now - 5*60*1000},
	}
}
