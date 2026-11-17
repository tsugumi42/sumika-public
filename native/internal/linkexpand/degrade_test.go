package linkexpand

// 规格-运行页 第七节、盘点-静默失败 第一类第 5 条：图片、动图、视频退回文字时记一行带类别的运行记录。
// 旧代码整包没有记录，Preparer 不接 Degrade 时这里收不到行会变红。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/taskrun"
)

type memSink struct{ rows []taskrun.Row }

func (s *memSink) AddTaskRun(r taskrun.Row) error { s.rows = append(s.rows, r); return nil }

func TestMediaClassTable(t *testing.T) {
	cases := []struct {
		err            error
		class, outcome string
	}{
		{ErrMediaTools, taskrun.ClassToolsMissing, taskrun.OutcomeFailed},
		{fmt.Errorf("x：%w", ErrContainer), taskrun.ClassContainerInvalid, taskrun.OutcomeFailed},
		{ErrVideoCapability, taskrun.ClassCapability, taskrun.OutcomeSkipped},
		{ErrGIFCapability, taskrun.ClassCapability, taskrun.OutcomeSkipped},
		{errImageCapability, taskrun.ClassCapability, taskrun.OutcomeSkipped},
		{context.DeadlineExceeded, taskrun.ClassTimeout, taskrun.OutcomeFailed},
		{context.Canceled, taskrun.ClassUnknown, taskrun.OutcomeCancelled},
		{ErrVideoDisabled, taskrun.ClassBlocked, taskrun.OutcomeSkipped},
		{linkreader.ErrMediaLimit, taskrun.ClassBlocked, taskrun.OutcomeSkipped},
		{ErrPlayback, taskrun.ClassNotFound, taskrun.OutcomeFailed},
		{errors.New("没见过的错"), taskrun.ClassUnknown, taskrun.OutcomeFailed},
	}
	for _, c := range cases {
		if class, outcome := MediaClass(c.err); class != c.class || outcome != c.outcome {
			t.Errorf("%v：%s/%s，应是 %s/%s", c.err, class, outcome, c.class, c.outcome)
		}
	}
}

// 这个场合发不出图片：整条退成文字，同时记一行 media · capability
func TestPrepareRecordsMediaDegrade(t *testing.T) {
	sink := &memSink{}
	var logs []string
	p := Preparer{Degrade: &taskrun.Degrader{Sink: sink, Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }},
		Check: func(context.Context) error { return nil }, Guard: func(context.Context, string) error { return nil }}
	d := Draft{Parts: []Part{{Kind: types.PartMessage, Body: []Item{{Kind: types.BodyImage}}}}}
	prepared, err := p.Prepare(context.Background(), &linkreader.Result{}, &d)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Release()
	if len(sink.rows) != 1 || sink.rows[0].Module != taskrun.ModuleMedia || sink.rows[0].Class != taskrun.ClassCapability || sink.rows[0].Outcome != taskrun.OutcomeSkipped {
		t.Fatalf("应记一行 media/capability/skipped：%+v", sink.rows)
	}
	if len(logs) != 1 || logs[0] != "media · capability（skipped，图片）" {
		t.Fatalf("日志只写模块、类别、结局和图片这个标签：%q", logs)
	}
	// 没接 Degrade 照常退成文字
	q := Preparer{Check: p.Check, Guard: p.Guard}
	if pr, err := q.Prepare(context.Background(), &linkreader.Result{}, &d); err != nil {
		t.Fatal(err)
	} else {
		pr.Release()
	}
}

// fake-ip 下没有代理可走：运行记录归网络类，日志标签里带底层原因，查的人一眼看到该去配 linkReader.proxy
func TestDegradedKeepsFakeIPReason(t *testing.T) {
	sink := &memSink{}
	var logs []string
	p := Preparer{Degrade: &taskrun.Degrader{Sink: sink, Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}}
	p.degraded("图片", &linkreader.FetchError{Kind: "unsafe_target", Source: "dns", Reason: linkreader.ReasonFakeIPNoProxy, Detail: "解析到 fake-ip 段，fake-ip 下没有代理可走"})
	if len(sink.rows) != 1 || sink.rows[0].Class != taskrun.ClassNetwork {
		t.Fatalf("应记 media/network：%+v", sink.rows)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "fake-ip 下没有代理可走") {
		t.Fatalf("日志里要有底层原因：%q", logs)
	}
}
