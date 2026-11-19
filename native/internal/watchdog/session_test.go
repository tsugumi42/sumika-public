package watchdog

import (
	"testing"
	"time"
)

// 真取当前登录会话的开始时间（只读）：Windows 上该是过去的某个时刻、不会是今天以后；别的系统是零值
func TestSessionStartIsPlausible(t *testing.T) {
	ss := SessionStart()
	if ss.IsZero() {
		t.Skip("这台机器取不到登录时间")
	}
	now := time.Now()
	if ss.After(now.Add(time.Minute)) || ss.Before(now.AddDate(-1, 0, 0)) {
		t.Fatalf("登录时间 %v 不合理（现在 %v）", ss, now)
	}
}
