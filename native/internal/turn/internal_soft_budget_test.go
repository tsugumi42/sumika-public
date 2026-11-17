package turn

import (
	"context"
	"errors"
	"testing"

	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/turnbudget"
)

type recordingCaller struct{ asked []int64 }

func (r *recordingCaller) StreamChat(_ context.Context, _ kernel.Invocation, req chat.Request, _ func(llm.Event)) (llm.Result, error) {
	r.asked = append(r.asked, req.MaxOutputTokens)
	return llm.Result{UsageSeen: true, Usage: llm.Usage{Input: 10, Output: 5}}, nil
}

func budgetRequest(max int64) chat.Request {
	return chat.Request{Model: "m", MaxOutputTokens: max, Style: chat.StyleBackground,
		Messages: []chat.Message{{Role: chat.RoleUser, Blocks: []chat.Block{chat.Text("x")}}}}
}

func softBudgetFixture(soft int64) (*internalLimits, Config, *recordingCaller, *[]string) {
	caller := &recordingCaller{}
	var nudges []string
	l := &internalLimits{limits: kernel.Limits{MaxRequests: kernel.NoLimit, MaxTools: kernel.NoLimit, MaxTokens: kernel.NoLimit, MaxOutputTokens: kernel.NoLimit}, soft: soft,
		nudge: func(s string) { nudges = append(nudges, s) }}
	cfg := Config{ModelCaller: caller, InternalAuthorize: func(context.Context, kernel.Invocation, string) error { return nil }}
	return l, cfg, caller, &nudges
}

// 子代理照 omp：到软预算那一次请求之后注入一句收尾提示（只一次），到 1.5 倍强停
func TestInternalSoftRequestBudgetNudgesThenStops(t *testing.T) {
	t.Parallel()
	l, cfg, caller, nudges := softBudgetFixture(4)
	ctx := context.Background()
	for i := 1; i <= 6; i++ {
		_, err := l.stream(ctx, cfg, kernel.Invocation{}, budgetRequest(100), nil)
		if err != nil {
			t.Fatalf("第 %d 次不该被停（强停在 6 次之后）：%v", i, err)
		}
		if want := 0; i < 4 && len(*nudges) != want {
			t.Fatalf("第 %d 次就提示了：%v", i, *nudges)
		}
		if i >= 4 && len(*nudges) != 1 {
			t.Fatalf("第 %d 次后应该恰好提示一次：%v", i, *nudges)
		}
	}
	if (*nudges)[0] != SoftBudgetNudge {
		t.Fatalf("提示内容：%q", (*nudges)[0])
	}
	if _, err := l.stream(ctx, cfg, kernel.Invocation{}, budgetRequest(100), nil); !errors.Is(err, turnbudget.ErrExhausted) {
		t.Fatalf("第 7 次（软预算 4 的 1.5 倍是 6）应该强停：%v", err)
	}
	if len(caller.asked) != 6 {
		t.Fatalf("强停后不该再发请求：%d", len(caller.asked))
	}
}

// 软预算写 0 就是不设：不提示也不停
func TestInternalSoftRequestBudgetZeroIsOff(t *testing.T) {
	t.Parallel()
	l, cfg, _, nudges := softBudgetFixture(0)
	for range 400 {
		if _, err := l.stream(context.Background(), cfg, kernel.Invocation{}, budgetRequest(100), nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(*nudges) != 0 {
		t.Fatalf("不设时不该提示：%v", *nudges)
	}
}

// 单次输出不再被固定值压住：子代理按模型给的值发；父根没余额时才往下压
func TestInternalOutputFollowsModelAndParentRemaining(t *testing.T) {
	t.Parallel()
	l, cfg, caller, _ := softBudgetFixture(0)
	if _, err := l.stream(context.Background(), cfg, kernel.Invocation{}, budgetRequest(128000), nil); err != nil {
		t.Fatal(err)
	}
	if caller.asked[0] != 128000 {
		t.Fatalf("子代理单次输出被压住了：%d", caller.asked[0])
	}
	l.limits.MaxOutputTokens = 1000
	if _, err := l.stream(context.Background(), cfg, kernel.Invocation{}, budgetRequest(128000), nil); err != nil {
		t.Fatal(err)
	}
	if want := int64(1000 - 5); caller.asked[1] != want {
		t.Fatalf("应该压到父根剩余输出额 %d：%d", want, caller.asked[1])
	}
}
