package proactive

import (
	"errors"
	"strings"
	"testing"
)

// 日志分级（主人 11-13“记得改”）：叫醒出错算 WARN，不再混在 INFO 里
func TestWakeErrorLogsWarn(t *testing.T) {
	st := &MemStore{}
	h := newHarness(t, st)
	var info, warn []string
	h.d.opt.Logf = func(f string, a ...any) { info = append(info, f) }
	h.d.opt.Warnf = func(f string, a ...any) { warn = append(warn, f) }
	h.d.opt.Waker = &fakeWaker{err: errors.New("模型不可用")}
	h.tick(at(9, 14, 0))
	h.tick(at(9, 16, 0))
	if len(warn) == 0 {
		t.Fatalf("叫醒出错要记 WARN：info=%v", info)
	}
	if len(info) != 0 {
		t.Fatalf("出错不该进 INFO：%v", info)
	}
}

func TestDirectorLevelHelpersFallBack(t *testing.T) {
	var got []string
	d := &Director{opt: Options{Logf: func(f string, _ ...any) { got = append(got, f) }}}
	d.warnf("w")
	d.errorf("e")
	if strings.Join(got, ",") != "w,e" {
		t.Fatalf("没接 WARN / ERROR 时要落回 Logf：%v", got)
	}
}
