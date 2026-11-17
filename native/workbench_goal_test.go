package main

import (
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/winshell"
)

// 目标行的测试台：新界面的聊天列加外壳令牌（目标行用 Kit 的控件），会话是编造的。
func newGoalRig(t *testing.T, gi *api.GoalInfo, line, state string) *wbRig {
	t.Helper()
	r := newWbRig(t, &fakeInputs{})
	r.a.workbench.shell.kit = winshell.NewKit(chatui.DefaultSettings(), "")
	r.a.localStatus = map[string]api.LocalStatus{r.key.Conversation: {Key: r.key, GoalLine: line, GoalState: state, Goal: gi}}
	r.settle()
	return r
}

const longGoal = "把说明文档里所有过期的路径都改成新目录，然后逐个打开确认能访问，最后写一份改动清单放到工作目录里"

func goalInfoFixture() *api.GoalInfo {
	return &api.GoalInfo{Text: longGoal, State: "active", Rounds: 3, MaxRounds: 30, TokensUsed: 12345, MaxTokens: 0,
		StartedAt: 1000, LastReason: "清单还没写", LastAt: 2000}
}

// 目标行：常驻只有一行；点展开看全文、轮数、预算、裁判理由，再点收起。
func TestGoalRowExpandsAndCollapses(t *testing.T) {
	r := newGoalRig(t, goalInfoFixture(), "目标：把说明文档里所有过期的路径都改成新目录，然后逐个打开确认能访… · 第 4 轮 · 进行中", "active")
	if !hasKey(r.tt, "wb-goal-row") || hasKey(r.tt, "wb-goal-detail") {
		t.Fatalf("刚开始应当只有一行：%q", r.tt.Texts())
	}
	if row := boxOf(t, r.tt, "wb-goal-line"); row.Box.H > 2*r.a.workbench.shell.kit.G.LabelLine {
		t.Fatalf("常驻行不止一行：%+v", row.Box)
	}
	if r.tt.HasText(longGoal) {
		t.Fatal("收起时不该显示目标全文")
	}
	click(t, r.tt, "展开目标详情")
	if !hasKey(r.tt, "wb-goal-detail") {
		t.Fatalf("点了展开没有详情：%q", r.tt.Texts())
	}
	for _, want := range []string{longGoal, "已跑 3 轮，上限 30 轮", "已用 12,345，上限 不限", "没达成", "清单还没写", "进行中"} {
		found := false
		for _, s := range r.tt.Texts() {
			if strings.Contains(s, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("展开后缺“%s”：%q", want, r.tt.Texts())
		}
	}
	// 展开钮换成收起钮；点收起回到一行
	if _, ok := r.tt.Find("展开目标详情"); ok {
		t.Fatal("展开后还有展开钮")
	}
	click(t, r.tt, "收起目标详情")
	if hasKey(r.tt, "wb-goal-detail") {
		t.Fatal("点了收起，详情还在")
	}
}

// 展开状态按会话记：这个会话展开不影响别的；目标没了再来是收起的。暂停时常驻行带继续的写法。
func TestGoalRowPausedAndPerConversation(t *testing.T) {
	gi := goalInfoFixture()
	gi.State, gi.PauseReason, gi.LastAchieved, gi.LastFailed = "paused", "预算到顶", false, true
	r := newGoalRig(t, gi, "目标：整理说明 · 已跑 30 轮 · 已暂停：预算到顶", "paused")
	if !r.tt.HasText("目标：整理说明 · 已跑 30 轮 · 已暂停：预算到顶  /goal resume 继续") {
		t.Fatalf("暂停时常驻行要写继续的办法：%q", r.tt.Texts())
	}
	click(t, r.tt, "展开目标详情")
	got := strings.Join(r.tt.Texts(), "\n")
	if !strings.Contains(got, "已暂停：预算到顶") || !strings.Contains(got, "裁判出错") {
		t.Fatalf("暂停详情不对：%q", got)
	}
	if !r.a.goalOpen[r.key.Conversation] || r.a.goalOpen["console:other"] {
		t.Fatalf("展开状态应只记在这个会话：%v", r.a.goalOpen)
	}
	// 没有目标详情（旧后台）退回一行文字，没有展开钮
	r.a.localStatus[r.key.Conversation] = api.LocalStatus{Key: r.key, GoalLine: "目标：x · 第 1 轮 · 进行中", GoalState: "active"}
	r.settle()
	if _, ok := r.tt.Find("展开目标详情"); ok || !r.tt.HasText("目标：x · 第 1 轮 · 进行中  /goal clear 结束") {
		t.Fatalf("没有详情时该退回一行文字：%q", r.tt.Texts())
	}
}

func TestGoalFactsUnlimitedBudget(t *testing.T) {
	t.Parallel()
	got := goalFacts(api.GoalInfo{Text: "x", State: "active"})
	want := map[string]string{"轮数": "已跑 0 轮，上限 不限", "token": "已用 0，上限 不限", "裁判": "还没判过"}
	for _, kv := range got {
		if w, ok := want[kv.Key]; ok && kv.Value != w {
			t.Errorf("%s：%q，应为 %q", kv.Key, kv.Value, w)
		}
	}
	if thousands(1234567) != "1,234,567" || thousands(999) != "999" || thousands(1000) != "1,000" {
		t.Error("千分位不对")
	}
}
