package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/turn"
)

// scriptedDesktop 同 autoDesktop，但能在 agent 一段里追加配置（缩进两格的行）
func scriptedDesktop(t *testing.T, e *testEnv, agentExtra string, script llmtest.Script) (*App, *Desktop) {
	t.Helper()
	script.Models = []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}
	srv := httptest.NewServer(llmtest.Handler(script, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	e.configure(agentExtra + "tools:\n  workdir: '" + filepath.ToSlash(t.TempDir()) + "'\n")
	a := e.open()
	d := a.Desktop()
	t.Cleanup(func() { d.Close(); a.Close() })
	return a, d
}

// 本地私聊里额度到顶：会话里要有一行说清哪一项到顶、用了多少、怎么接着做（原来结局是 budget_exhausted，界面一声不吭）
func TestLocalBudgetExhaustedSaysSo(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, d := scriptedDesktop(t, e, "  budget:\n    local:\n      maxRequests: 1\n", llmtest.Script{Reply: llmtest.Call("c1", "read", `{"path":"nothing.txt"}`)})
	key := localKey("console:" + testChar)
	// 发送端会把到顶当成错误交回来，这里要看的是会话里有没有那一行
	if err := d.Reader.SendLocal(context.Background(), key, "读一下 nothing.txt", nil); err != nil && !strings.Contains(err.Error(), "budget_exhausted") {
		t.Fatal(err)
	}
	waitFor(t, "额度到顶的提示", func() bool { return hasRow(slashRows(t, a, key.Conversation), "notice/system/system:budget/") })
	rows := slashRows(t, a, key.Conversation)
	var line string
	for _, r := range rows {
		if strings.Contains(r, "system:budget") {
			line = r
		}
	}
	for _, want := range []string{"请求次数", "模型请求 1/1 次", "继续", "agent.budget.local.maxRequests"} {
		if !strings.Contains(line, want) {
			t.Fatalf("提示里缺 %q：%s", want, line)
		}
	}
}

// 最后一条回复被单次输出上限截断：告诉主人，并说明怎么让她接着写
func TestLocalTruncatedReplySaysSo(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r := llmtest.Text("这是一个很长的文件，写到一半")
	r.Finish = "length"
	a, d := scriptedDesktop(t, e, "", llmtest.Script{Reply: r})
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(context.Background(), key, "写个长文件", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "截断的提示", func() bool { return hasRow(slashRows(t, a, key.Conversation), "notice/system/system:budget/") })
	for _, r := range slashRows(t, a, key.Conversation) {
		if strings.Contains(r, "system:budget") && (!strings.Contains(r, "被截断") || !strings.Contains(r, "模型单次输出的上限") || strings.Contains(r, "requestOutputTokens") || !strings.Contains(r, "继续")) {
			t.Fatalf("截断提示不对：%s", r)
		}
	}
}

// 不是主人在本地私聊（QQ、本地群、非主人）的回合，不在这里说话：QQ 和群聊走原有出口
func TestBudgetNoticeIgnoresOtherScenes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, _ := autoDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	owner := kernel.Scope{CharacterID: testChar, Platform: "local", Channel: "console", Scene: "private", ActorKind: "owner", ActorID: "owner", Conversation: "console:" + testChar}
	qq := owner
	qq.Platform, qq.Channel = "qq", "qq"
	grp := owner
	grp.Scene = "group"
	for name, scope := range map[string]kernel.Scope{"QQ": qq, "本地群": grp} {
		a.budgetSettled(context.Background(), turn.Settled{Identity: kernel.Invocation{Scope: scope, RootID: "x"}, Conversation: "console:" + testChar, Outcome: string(kernel.OutcomeBudgetExhausted), Truncated: true})
		if hasRow(slashRows(t, a, "console:"+testChar), "system:budget") {
			t.Fatalf("%s：不该说话", name)
		}
	}
}

func TestBudgetExhaustedTextPicksTightestLimit(t *testing.T) {
	t.Parallel()
	now := time.Now()
	snap := kernel.BudgetSnapshot{Requests: 10, Tools: 119, Tokens: 1_000_000, OutputTokens: 5000,
		Limits: kernel.Limits{MaxRequests: 120, MaxTools: 120, MaxTokens: 16_000_000, MaxOutputTokens: 500_000, Deadline: now.Add(time.Hour)}}
	got := budgetExhaustedText(snap, now, false)
	if !strings.Contains(got, "「工具调用」") || !strings.Contains(got, "maxTools") || !strings.Contains(got, "工具调用 119/120 次") {
		t.Fatalf("应该是工具调用到顶：%s", got)
	}
	got = budgetExhaustedText(snap, now.Add(2*time.Hour), false)
	if !strings.Contains(got, "「时限」") || !strings.Contains(got, "agent.budget.local.maxDurationMs") || !strings.Contains(got, "继续") {
		t.Fatalf("过了期限应该说时限、点名键、说怎么接着做：%s", got)
	}
	if got := budgetExhaustedText(snap, now, false); !strings.Contains(got, "重启 Sumika") {
		t.Fatalf("调高的提示要说明改完重启才生效：%s", got)
	}
}

// 自动发起的一轮（目标续轮、定期任务）没有主人在场，用顶层额度，不拿本地的宽额度；到顶提示不叫人回发不进去的「继续」，键写顶层的
func TestAutoTurnUsesTopLevelLimitsAndNoticeWithoutContinue(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, _ := scriptedDesktop(t, e, "  budget:\n    maxRequests: 1\n    local:\n      maxRequests: 50\n", llmtest.Script{Reply: llmtest.Call("c1", "read", `{"path":"nothing.txt"}`)})
	conv := "console:" + testChar
	if err := a.goalLaunch(conv, testChar, "自动续轮：读一下 nothing.txt"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "自动轮额度到顶的提示", func() bool { return hasRow(slashRows(t, a, conv), "system:budget") })
	var line string
	for _, r := range slashRows(t, a, conv) {
		if strings.Contains(r, "system:budget") {
			line = r
		}
	}
	for _, want := range []string{"模型请求 1/1 次", "agent.budget.maxRequests", "自动发起"} {
		if !strings.Contains(line, want) {
			t.Fatalf("自动轮提示里缺 %q：%s", want, line)
		}
	}
	if strings.Contains(line, "agent.budget.local") || strings.Contains(line, "回一句「继续」") {
		t.Fatalf("自动轮不该用本地额度的键、不该叫人回继续：%s", line)
	}
}

// 实跑里唯一出现过的"输出被截断"：思考占满单次输出，网关回 502、不是 stop=length。要在会话里说清是额度问题和怎么调
func TestReasoningFilledOutput502SaysSo(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			llmtest.Handler(llmtest.Script{Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}}, &e.bodies, &e.mu)(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"message":"Open Design: an empty reply (the model answered nothing; try again) — cut at its length, the whole output spent on reasoning","type":"api_error"}`))
	}))
	t.Cleanup(gateway.Close)
	e.llm = gateway
	e.configure("tools:\n  workdir: '" + filepath.ToSlash(t.TempDir()) + "'\n")
	a := e.open()
	d := a.Desktop()
	t.Cleanup(func() { d.Close(); a.Close() })
	key := localKey("console:" + testChar)
	_ = d.Reader.SendLocal(context.Background(), key, "写个大文件", nil) // 出错交回来，这里看会话里的那一行
	waitFor(t, "思考占满输出的提示", func() bool { return hasRow(slashRows(t, a, key.Conversation), "system:budget") })
	for _, r := range slashRows(t, a, key.Conversation) {
		if strings.Contains(r, "system:budget") {
			for _, want := range []string{"思考", "继续", "由模型和窗口决定"} {
				if !strings.Contains(r, want) {
					t.Fatalf("提示里缺 %q：%s", want, r)
				}
			}
		}
	}
}

// 配置里设了单次输出封顶时，截断提示点名这个键，并说写 0 就跟模型；没设封顶时不提这个键
func TestOutputLimitCauseNamesTheRealCause(t *testing.T) {
	t.Parallel()
	capped := kernel.BudgetSnapshot{Limits: kernel.Limits{RequestOutputTokens: 4096, MaxOutputTokens: kernel.NoLimit}}
	if got := truncatedText(capped, false); !strings.Contains(got, "agent.budget.local.requestOutputTokens") || !strings.Contains(got, "写 0 就跟模型走") || !strings.Contains(got, "4096") {
		t.Fatalf("封顶造成的截断要点名键：%s", got)
	}
	if got := truncatedText(capped, true); !strings.Contains(got, "agent.budget.requestOutputTokens") || strings.Contains(got, "agent.budget.local") || strings.Contains(got, "回一句") {
		t.Fatalf("自动轮用顶层键、不叫人回继续：%s", got)
	}
	spent := kernel.BudgetSnapshot{OutputTokens: 524288, Limits: kernel.Limits{MaxOutputTokens: 524288}}
	if got := truncatedText(spent, true); !strings.Contains(got, "输出额度快用完") || !strings.Contains(got, "agent.budget.maxOutputTokens") {
		t.Fatalf("输出额度造成的截断要说输出额度：%s", got)
	}
	unlimited := kernel.BudgetSnapshot{Limits: kernel.Limits{MaxOutputTokens: kernel.NoLimit}}
	if got := truncatedText(unlimited, false); strings.Contains(got, "agent.budget") {
		t.Fatalf("不设额度时不该怪配置：%s", got)
	}
	// 不设上限的几项在"到顶"提示里不当成最紧的一项，也不打出天文数字
	snap := kernel.BudgetSnapshot{Requests: 7, Tools: 3, Tokens: 2_000_000, Limits: kernel.Limits{MaxRequests: 7, MaxTools: kernel.NoLimit, MaxTokens: kernel.NoLimit, MaxOutputTokens: kernel.NoLimit, Deadline: time.Now().Add(time.Hour)}}
	got := budgetExhaustedText(snap, time.Now(), false)
	if !strings.Contains(got, "「请求次数」") || strings.Contains(got, "2305843009213693951") || strings.Contains(got, "万亿") {
		t.Fatalf("不设上限的项要写已用量、不打出上限：%s", got)
	}
}
