package remind

import (
	"strings"
	"sync"
	"testing"
)

// 日志分级（主人 11-13“记得改”）：没送到、过会儿再试算 WARN，不再混在 INFO 里
func TestDeliveryRetryLogsWarn(t *testing.T) {
	clock := &fakeClock{t: at(2026, 11, 9, 12, 0)}
	rec := &ringRec{}
	rec.res = func(RingRequest) RingResult { return RingResult{Retry: true, Note: "写库出错"} }
	s := newSched(t, &MemoryStore{}, clock, rec)
	var mu sync.Mutex
	var info, warn []string
	s.opt.Logf = func(f string, a ...any) { mu.Lock(); info = append(info, f); mu.Unlock() }
	s.opt.Warnf = func(f string, a ...any) { mu.Lock(); warn = append(warn, f); mu.Unlock() }
	if _, _, err := s.Create(chat, Draft{Text: "开会", At: at(2026, 11, 10, 9, 0)}); err != nil {
		t.Fatal(err)
	}
	clock.Set(at(2026, 11, 10, 9, 0))
	tick(s)
	mu.Lock()
	defer mu.Unlock()
	if len(warn) != 1 || !strings.Contains(warn[0], "没送到") {
		t.Fatalf("没送到要记 WARN：warn=%v info=%v", warn, info)
	}
	for _, l := range info {
		if strings.Contains(l, "没送到") {
			t.Fatalf("没送到不该再进 INFO：%v", info)
		}
	}
}

func TestLevelHelpersFallBackToLogf(t *testing.T) {
	var got []string
	s := &Scheduler{opt: Options{Logf: func(f string, _ ...any) { got = append(got, f) }}}
	s.warnf("w")
	s.errorf("e")
	if strings.Join(got, ",") != "w,e" {
		t.Fatalf("没接 WARN / ERROR 时要落回 Logf：%v", got)
	}
}
