package selfcheck

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 规格-日志分级 3 节：模块和级别由出口加在行首，消息正文不再自带 `selfcheck:` 前缀
// （带着前缀文件里就是 `WARN selfcheck selfcheck: 自检异常…`）。archcheck 的扫描只认首参是字符串的 logf 形函数，
// 管不到 Runner.log 的 (bool, 格式) 形状，所以在这里直接看写出去的文字。
func TestRunnerLogLinesHaveNoOwnPrefix(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: day1}
	lg := &logs{}
	state := Bad
	c := &fakeCheck{id: "x.y", res: func(*Env) Result { return Result{State: state, Value: 3, Threshold: 1} }}
	r := newTestRunner(t, dir, NewCounters(), clk, lg, c)
	r.Evaluate(context.Background())
	state = Ok
	clk.Add(time.Minute)
	r.Evaluate(context.Background())
	if len(lg.warn) != 1 || len(lg.infos) != 1 {
		t.Fatalf("该有一条异常、一条恢复：%v %v", lg.warn, lg.infos)
	}
	for _, line := range append(append([]string{}, lg.warn...), lg.infos...) {
		if strings.HasPrefix(line, "selfcheck") {
			t.Errorf("正文自带模块前缀：%q", line)
		}
	}
}
