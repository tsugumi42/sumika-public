package app

import (
	"testing"

	"sumika-native/internal/approval"
	"sumika-native/internal/kernel"
)

// 子任务的审批卡放回派它的会话；认不出的、不是子任务的、查不到派它的人的，原样不动（不会跑到别的窗口）。
func TestCardConversationOf(t *testing.T) {
	t.Parallel()
	owners := map[string]string{"task-1": "console:suzu", "task-2": "console:suzu:topic:t1", "task-3": ""}
	owner := func(id string) (kernel.Scope, bool) {
		c, ok := owners[id]
		return kernel.Scope{Conversation: c}, ok
	}
	for _, c := range []struct{ in, want string }{
		{"internal:task-1", "console:suzu"},
		{"internal:task-2", "console:suzu:topic:t1"},
		{"internal:task-3", "internal:task-3"}, // 派它的会话是空的：不搬
		{"internal:ghost", "internal:ghost"},   // 没有这个任务（已结束、编造）：不搬
		{"internal:", "internal:"},
		{"console:suzu", "console:suzu"},
		{"console:group:g", "console:group:g"},
		{"qq:group:1", "qq:group:1"},
		{"", ""},
	} {
		if got := cardConversationOf(owner, approval.Request{Conversation: c.in}); got != c.want {
			t.Errorf("%q 搬到了 %q，应为 %q", c.in, got, c.want)
		}
	}
}
