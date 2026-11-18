package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"sumika-native/internal/llm"
)

func TestIssue17Lifecycle(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("issue17 进程资源验收仅支持 Windows")
	}
	root, err := findRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "run")
	var out bytes.Buffer
	r, err := runIssue17(context.Background(), issue17Options{Work: dir, Root: root, Model: "fixture/plain", Mode: modeFake, Sample: 2, Rounds: 1}, &out)
	if err != nil {
		t.Fatal(err, out.String())
	}
	round := r.Rounds[0]
	for _, f := range issue17Selected(2) {
		found := false
		for _, it := range round.Items[issue17Character] {
			if strings.Contains(it.Text, f.Value) && it.Conversation == f.Conversation {
				found = true
			}
		}
		if !found {
			t.Fatalf("重启后缺少事件来源对应的事实 %s：%+v", f.ID, round.Items)
		}
	}
	for _, c := range round.Checks {
		if strings.HasPrefix(c.Name, "restart:") && !c.Pass {
			t.Fatal(c)
		}
	}
	// 最终回答必须来自真实 Hub 请求，保留实际拼出的场合、问题与参考信息；没有把预期答案塞给网关。
	for _, f := range issue17Selected(2) {
		found := false
		for _, a := range round.Answers {
			if a.Question.ID == f.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("缺少重启后的最终问答：%s", f.ID)
		}
	}
	for _, a := range round.Answers {
		found := false
		for _, call := range r.Calls {
			if call.Purpose == "answer" && strings.Contains(string(call.Request), a.Question.Text) {
				found = true
			}
		}
		if !found {
			t.Fatalf("没有最终模型请求：%s", a.Question.ID)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "marks.csv"))
	if err != nil {
		t.Fatal(err)
	}
	score, err := parseMarks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if score.Fact.Marked != 0 {
		t.Fatal("未标数据被算成了正确率")
	}
	if r.PaidCalls != 0 || r.Assessment == "通过" {
		t.Fatal("假模型被算成付费质量验收")
	}
}

func TestIssue17AdmissionAndRecovery(t *testing.T) {
	t.Parallel()
	root, err := findRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	checks, err := issue17Mechanisms(context.Background(), t.TempDir(), root, "fixture/plain")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range checks {
		seen[c.Name] = true
		if !c.Pass {
			t.Errorf("%s：%s", c.Name, c.Detail)
		}
	}
	for _, gate := range []string{"write_off", "character_not_listed", "summary_off", "no_background", "place_off"} {
		for _, conv := range []string{"console:w6b-lan", "console:group:w6b-home", "qq:private:101:909", "qq:group:707"} {
			name := gate + ":" + conv
			if !seen[name] {
				t.Errorf("缺少准入判定：%s", name)
			}
		}
		if !seen[gate+":no_write"] {
			t.Errorf("缺少禁写库存判定：%s", gate)
		}
	}
	for _, name := range []string{"before_commit_atomic", "before_commit_restart_replay", "after_commit_index_failure", "after_commit_rebuild", "after_commit_duplicate_consumption"} {
		if !seen[name] {
			t.Errorf("缺少恢复判定：%s", name)
		}
	}
}

func TestIssue17BudgetAndIsolation(t *testing.T) {
	t.Parallel()
	upstreamCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"实际响应\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	g := newIssue17Gateway(modeReal, 1, server.URL+"/v1")
	defer g.server.Close()
	gen := issue17Generator{llm.New(llm.Magpie(g.server.URL + "/v1"))}
	first, err := gen.Call(context.Background(), "fixture/plain", "第一次", "a")
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != "实际响应" {
		t.Fatal(first)
	}
	if _, err = gen.Call(context.Background(), "fixture/plain", "第二次", "b"); err == nil {
		t.Fatal("预算耗尽仍被当成功")
	}
	calls, exhausted := g.snapshot()
	if upstreamCalls != 1 || len(calls) != 1 || !exhausted {
		t.Fatalf("上游=%d calls=%d exhausted=%t", upstreamCalls, len(calls), exhausted)
	}
	// 已有目录不能被 fixture 当成临时根复用，连只读估算也不得遍历它。
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel")
	if err = os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = runIssue17(context.Background(), issue17Options{Work: dir, Root: "unused", Rounds: 1}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("复用了已有数据目录")
	}
	b, err := os.ReadFile(sentinel)
	if err != nil || string(b) != "preserve" {
		t.Fatalf("已有文件被动过：%s %v", b, err)
	}
}
func TestIssue17PercentileRank(t *testing.T) {
	t.Parallel()
	values := make([]float64, 20)
	for i := range values {
		values[i] = float64(i + 1)
	}
	for _, c := range []struct{ p, want float64 }{{0.5, 10}, {0.95, 19}, {1, 20}} {
		if got := issue17Percentile(values, c.p); got != c.want {
			t.Errorf("p=%v got=%v want=%v", c.p, got, c.want)
		}
	}
}
