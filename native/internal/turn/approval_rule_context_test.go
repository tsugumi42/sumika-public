package turn

// 审批说法不在固定前缀里：输入框切档当场生效，说法跟着变，但只追加在每轮参考信息的末尾，
// 请求前缀不断（LESSONS：每轮变化的内容只追加在末尾；opencode 也是变了才渲染更新）。

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestApprovalRuleSwitchKeepsPrefix(t *testing.T) {
	t.Parallel()
	var mode atomic.Value
	mode.Store("manual")
	h, clock := newReuseHarness(t, replyList(textReply, textReply, textReply, textReply), func(cfg *Config) {
		cfg.ApprovalRuleOf = func(conversation string) string {
			if mode.Load() == "yolo" {
				return "测试：免审批说法。"
			}
			return DefaultApprovalRule
		}
	})
	a := rootInput(h, "第一轮", "")
	b := rootInput(h, "第二轮（档没变）", "")
	c := rootInput(h, "第三轮（切到免审批）", "")
	d := rootInput(h, "第四轮（档没变）", "")
	respondAll(t, h, a)
	first := h.liveFor(a)
	count := func(i int, s string) int { return strings.Count(strings.Join(h.messages(i), "\n"), s) }
	if got := count(0, "审批规则（以这一条为准"); got != 1 {
		t.Fatalf("第一轮要给一次审批规则：%d", got)
	}
	if strings.Contains(strings.Join(h.messages(0)[:1], ""), "每次都会先请所有者在界面上批准") {
		t.Fatal("固定前缀（系统提示）里不该再写审批说法")
	}
	clock.advance(time.Minute)
	respondAll(t, h, b)
	if got := count(1, "审批规则（以这一条为准"); got != 1 {
		t.Fatalf("档没变不该再给：共 %d 条", got)
	}
	mode.Store("yolo")
	clock.advance(time.Minute)
	respondAll(t, h, c)
	if got := count(2, "审批规则（以这一条为准"); got != 2 || count(2, "测试：免审批说法") != 1 {
		t.Fatalf("切档后追加一条新的：规则 %d 条，新说法 %d 条", got, count(2, "测试：免审批说法"))
	}
	clock.advance(time.Minute)
	respondAll(t, h, d)
	if got := count(3, "审批规则（以这一条为准"); got != 2 {
		t.Fatalf("没再切档就不该再追加：%d", got)
	}
	if h.liveFor(d) != first {
		t.Fatal("切档换了实例")
	}
	assertChain(t, h, 0) // 每一次请求都以前一次开头：前缀、历史一个字节都没动
}
