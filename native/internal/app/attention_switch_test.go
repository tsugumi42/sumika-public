package app

import (
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/config"
	"sumika-native/internal/llm/llmtest"
)

// 设置页“通知”里按类的开关：关掉的那一类不再往宿主说，别的类照常；改了配置不用重启就生效；
// 种类带编号的提醒（reminder:12）也认；配置文件坏了就当全开，不漏通知。
func TestAttentionPerKindSwitch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, _, log := attentionDesktop(t, e, llmtest.Script{})
	file := filepath.Join(e.data, "config", "sumika.yml")
	orig, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	set := func(block string) {
		t.Helper()
		if err := os.WriteFile(file, append(append([]byte(nil), orig...), []byte("\nnotifications:\n"+block)...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	conv := "console:" + testChar
	fire := func(kinds ...string) []string {
		a.attnMu.Lock()
		a.attnLast = nil // 不测 20 秒合并，只测开关
		a.attnMu.Unlock()
		before := len(log.all())
		for _, k := range kinds {
			a.attend(k, conv, "标题", "正文")
		}
		var out []string
		for _, ev := range log.all()[before:] {
			out = append(out, ev.Kind)
		}
		return out
	}
	all := []string{AttentionGoalDone, AttentionGoalPaused, AttentionLoop, AttentionApproval, AttentionQuestion, AttentionReminder + ":12", AttentionProactive}

	if got := fire(all...); len(got) != len(all) {
		t.Fatalf("没写 notifications 时全开：%v", got)
	}
	set("  goal: false\n  reminder: false\n")
	got := fire(all...)
	want := []string{AttentionLoop, AttentionApproval, AttentionQuestion, AttentionProactive}
	if len(got) != len(want) {
		t.Fatalf("关了目标和提醒，应只剩 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("应只剩 %v，实际 %v", want, got)
		}
	}
	set("  loop: false\n  approval: false\n  question: false\n")
	if got := fire(all...); len(got) != 4 || got[0] != AttentionGoalDone || got[3] != AttentionProactive {
		t.Fatalf("关了定期任务、审批、提问：%v", got)
	}
	set("  goal: false\n")
	set("  goal: true\n") // 改回来马上又弹
	if got := fire(AttentionGoalDone); len(got) != 1 {
		t.Fatalf("改回开着要马上生效：%v", got)
	}
	if err := os.WriteFile(file, []byte("notifications: [坏的\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := fire(AttentionGoalDone, AttentionApproval); len(got) != 2 {
		t.Fatalf("配置读不了时当全开：%v", got)
	}
}

func TestDefaultNotificationsAllOn(t *testing.T) {
	t.Parallel()
	n := config.DefaultNotifications()
	if !n.Goal || !n.Loop || !n.Approval || !n.Question || !n.Reminder {
		t.Fatalf("默认要全开：%+v", n)
	}
	if got := config.Defaults().Notifications; got != n {
		t.Fatalf("Defaults 没带上：%+v", got)
	}
}
