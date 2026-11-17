package modelcall

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/turnbudget"
)

func TestPurposeOf(t *testing.T) {
	t.Parallel()
	for kind, want := range map[string]string{
		"": "", kernel.KindForeground: "", kernel.KindGuard: "sumika_guard", kernel.KindMemory: "sumika_memory",
		kernel.KindCompact: "sumika_compact", kernel.KindLinkTranslate: "sumika_translate", kernel.KindAdvisor: "sumika_advisor",
		kernel.KindGoalJudge: "sumika_goal_judge", kernel.KindChild: "sumika_child", "Weird-Kind!": "sumika_eirdind",
	} {
		if got := PurposeOf(kind); got != want {
			t.Errorf("PurposeOf(%q) = %q，应是 %q", kind, got, want)
		}
	}
	// Magpie 有专门处理的名字一个都不能撞上
	for _, kind := range []string{kernel.KindMemory, kernel.KindCompact, kernel.KindGuard, kernel.KindChild, kernel.KindAdvisor, kernel.KindVerify, kernel.KindGoalJudge, kernel.KindLinkTranslate} {
		switch PurposeOf(kind) {
		case "memory", "memgen", "memory_consolidation", "thread_title", "title", "thread_description", "collab_spawn", "guardian", "compact", "vision", "web_search", "routing", "luna_reserve":
			t.Errorf("%s 撞上了 Magpie 的专用名字", kind)
		}
	}
	if n := len(PurposeOf("x_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")); n > 40 {
		t.Fatalf("用途头超过 40 个字：%d", n)
	}
}

// 后台调用带用途头、前台不带；两种都把发出去的指纹写进 model_runs
func TestStreamSendsPurposeAndRecordsPromptHashes(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenFresh(paths.Paths{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ledger := &turnbudget.Ledger{Store: st}
	var bodies []string
	var mu sync.Mutex
	var headers []string
	h := llmtest.Handler(llmtest.Script{Reply: llmtest.Text("ok")}, &bodies, &mu)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			headers = append(headers, r.Header.Get("x-openai-subagent"))
			mu.Unlock()
		}
		h(w, r)
	}))
	defer srv.Close()
	caller := New(llm.New(llm.Magpie(srv.URL+"/v1")), ledger, st, 1, 0)
	for i, kind := range []string{kernel.KindForeground, kernel.KindGuard} {
		root := "r" + kind
		in := kernel.Invocation{RootID: root, RunID: "run", StepID: "s", AttemptID: "a" + kind, Kind: kind}
		if err := ledger.OpenRoot(ctx, root, in.Scope, kernel.Limits{MaxRequests: 3, MaxTools: 1, MaxTokens: 10000, MaxOutputTokens: 100, Deadline: time.Now().Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if _, err := caller.StreamChat(ctx, in, testChatRequest("fiction", 10), nil); err != nil {
			t.Fatal(err)
		}
		var fp sql.NullString
		if err := st.Trace.QueryRow(`SELECT prompt_hashes_json FROM model_runs WHERE attempt_id = ?`, in.AttemptID).Scan(&fp); err != nil || !fp.Valid || fp.String == "" {
			t.Fatalf("第 %d 次没记指纹：%+v %v", i, fp, err)
		}
	}
	if len(headers) != 2 || headers[0] != "" || headers[1] != "sumika_guard" {
		t.Fatalf("用途头：%q，应是前台不带、守卫带 sumika_guard", headers)
	}
}
