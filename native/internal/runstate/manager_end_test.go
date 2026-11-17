package runstate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/kernel"
)

// failEndJournal 让指定运行的活动退出写不进库；allow 打开后写得进。
type failEndJournal struct {
	failRun string
	allow   bool
}

func (j *failEndJournal) ActivityStart(context.Context, string, kernel.Invocation) error { return nil }
func (j *failEndJournal) ActivityEnd(_ context.Context, id string, _ error) error {
	if !j.allow && strings.HasPrefix(id, j.failRun+"/") {
		return errors.New("made-end-failure " + id)
	}
	return nil
}

func TestEndFailureBelongsToItsOwnRun(t *testing.T) {
	j := &failEndJournal{failRun: "run-a"}
	m := New(j)
	endOne := func(run string) {
		_, a, err := m.Begin(context.Background(), kernel.Invocation{RunID: run, Kind: kernel.KindForeground})
		if err != nil {
			t.Fatal(err)
		}
		a.End(nil)
	}
	endOne("run-a")
	endOne("run-b")
	if err := m.WaitRun(context.Background(), "run-b"); err != nil {
		t.Fatalf("别的运行不该带着 run-a 的退出失败：%v", err)
	}
	if err := m.WaitRun(context.Background(), "run-a"); err == nil || !strings.Contains(err.Error(), "made-end-failure") {
		t.Fatalf("run-a 自己该看到退出失败：%v", err)
	}
	if err := m.Drain(context.Background()); err == nil {
		t.Fatal("没写成的退出记录还在，Drain 不该说干净")
	}
	// 库还写不进：重试如实失败，记录留着
	if err := m.RetryEnds(context.Background(), "run-a"); err == nil || m.EndFailures("run-a") != 1 {
		t.Fatalf("重试没成功却被说成成功：err=%v 剩 %d", err, m.EndFailures("run-a"))
	}
	// 写得进了：重试别的运行不动 run-a 的记录；重试 run-a 成功后清掉，Drain 干净
	j.allow = true
	if err := m.RetryEnds(context.Background(), "run-b"); err != nil || m.EndFailures("run-a") != 1 {
		t.Fatalf("重试 run-b 不该动 run-a 的记录：err=%v 剩 %d", err, m.EndFailures("run-a"))
	}
	if err := m.RetryEnds(context.Background(), "run-a"); err != nil {
		t.Fatalf("库恢复后重试该成功：%v", err)
	}
	if err := m.Drain(context.Background()); err != nil {
		t.Fatalf("重试成功后 Drain 该干净：%v", err)
	}
}
