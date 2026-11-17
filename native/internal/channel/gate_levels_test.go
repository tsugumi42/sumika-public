package channel

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// 规格-日志分级 5.1：闸门读库出错、“这一批不放行”是出错但会自己接上，记 WARN；以前这些行是 INFO，
// 同文件里同类的几行已经是 WARN，用 --level warn 看时漏掉一半。
func TestGateReadFailuresAreWarnNotInfo(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var info, warn []string
	h := newGH(t, nil, func(c *GateConfig) {
		c.Logf = func(f string, a ...any) { mu.Lock(); info = append(info, fmt.Sprintf(f, a...)); mu.Unlock() }
		c.Warnf = func(f string, a ...any) { mu.Lock(); warn = append(warn, fmt.Sprintf(f, a...)); mu.Unlock() }
	})
	g := group("5001")
	h.st.Trace.Close()
	h.say(g, friend, "在吗", 0, atHer())
	mu.Lock()
	defer mu.Unlock()
	if len(warn) == 0 || !strings.Contains(strings.Join(warn, "\n"), "不放行") {
		t.Errorf("读不出状态不放行，该记 WARN：warn=%v info=%v", warn, info)
	}
	for _, l := range info {
		if strings.Contains(l, "出错") || strings.Contains(l, "不放行") {
			t.Errorf("出错的行还在 INFO：%q", l)
		}
	}
}
