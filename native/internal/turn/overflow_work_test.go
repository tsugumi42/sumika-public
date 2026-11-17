package turn

// 规格-工具历史恢复和压缩 2.3：本地私聊的溢出恢复。一轮内可以多次，不接两遍，不把输出上限压成 1，没进展就停。

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

// overflowAt 让第 steps 个回复请求（摘要请求不算）返回服务商的溢出错误；always 为真时第一步之后一律溢出
func overflowAt(always bool, steps ...int) func(n int, body string, w http.ResponseWriter, r *http.Request) bool {
	var n atomic.Int32
	return func(_ int, body string, w http.ResponseWriter, _ *http.Request) bool {
		if strings.Contains(body, compactMarker) {
			return false
		}
		step := int(n.Add(1))
		hit := always && step > 1
		for _, s := range steps {
			hit = hit || s == step
		}
		if hit {
			http.Error(w, overflowText, 400)
		}
		return hit
	}
}

type overflowOpts struct {
	replies []string
	always  bool
	steps   []int
	exec    func(args jsjson.Value) string
	mutate  func(*Config)
}

// workOverflowHarness：本地私聊开着工作记录，工具结果由 exec 给；overflowAt 管哪几步溢出
func workOverflowHarness(t *testing.T, o overflowOpts) *harness {
	t.Helper()
	h := newHarnessOpts(t, harnessOpts{replies: replyList(o.replies...), intercept: overflowAt(o.always, o.steps...), mutate: func(c *Config) {
		workLimits(c)
		smallKeep(c)
		c.ExecTool = func(_ context.Context, _, name string, args jsjson.Value) (tools.Result, error) {
			if o.exec != nil {
				return tools.Result{Text: o.exec(args)}, nil
			}
			return tools.Result{Text: reuseToolText + name}, nil
		}
		if o.mutate != nil {
			o.mutate(c)
		}
	}})
	approveWork(h)
	return h
}

var maxTokensOne = regexp.MustCompile(`"max_completion_tokens":1[,}]`)

// 溢出恢复不把本轮已入库的工具步骤接两遍（旧代码：工具结果和调用各出现两次）
func TestOverflowWithWorkNoDuplicate(t *testing.T) {
	t.Parallel()
	h := workOverflowHarness(t, overflowOpts{
		replies: []string{callReply("read", `{"path":"src/made_a.go"}`), summaryReply, textReply},
		steps:   []int{2},
	})
	h.seedRounds(8)
	in := rootInput(h, "made-先读再答", "made-root-a")
	o := recv(t, h.hub.Respond(in), "恢复后回复")
	if o.Err != nil || o.Result == nil || o.Result.Reply != "好的" {
		t.Fatalf("没恢复：%+v", o)
	}
	body := lastBody(h)
	if n := strings.Count(body, reuseToolText+"read"); n != 1 {
		t.Fatalf("工具结果出现 %d 次", n)
	}
	if n := strings.Count(body, "made-先读再答"); n != 1 {
		t.Fatalf("本轮那句出现 %d 次", n)
	}
	if n := strings.Count(body, "src/made_a.go"); n != 1 {
		t.Fatalf("工具调用出现 %d 次", n)
	}
	if l := h.liveFor(in); toolPairProblems(l.msgs) {
		t.Fatal("恢复后调用和结果对不上")
	}
}

// 恢复后尾部不再带压之前的用量：输出上限不被压成 1（旧代码：Window - 估算 - 4096 为负）
func TestOverflowClearsStaleUsage(t *testing.T) {
	t.Parallel()
	huge := `{"toolCalls":[{"id":"c1","name":"read","args":["{\"path\":\"src/made_a.go\"}"]}],"usage":{"prompt_tokens":197000,"completion_tokens":10}}`
	h := workOverflowHarness(t, overflowOpts{
		replies: []string{huge, summaryReply, textReply},
		steps:   []int{2},
		mutate: func(c *Config) {
			c.Context.Window, c.Context.Reserve, c.Context.HardRatio = 200000, 1, 1
		},
	})
	h.seedRounds(8)
	in := rootInput(h, "made-先读再答", "made-root-a")
	o := recv(t, h.hub.Respond(in), "恢复后回复")
	if o.Err != nil || o.Result == nil || o.Result.Reply != "好的" {
		t.Fatalf("没恢复：%+v", o)
	}
	if maxTokensOne.MatchString(lastBody(h)) {
		t.Fatal("恢复后的请求输出上限是 1")
	}
}

func bigResult(tag string) func(jsjson.Value) string {
	return func(args jsjson.Value) string { return argString(args, "path") + strings.Repeat(tag, 5000) }
}

// 一轮内两次溢出：第一次压前面的轮次，第二次折叠本轮更早的工具步骤，最新一组全文留着
func TestOverflowTwiceFoldsTail(t *testing.T) {
	t.Parallel()
	var replies []string
	for i := range 3 {
		replies = append(replies, callReply("read", fmt.Sprintf(`{"path":"src/made_%d.go"}`, i)))
	}
	replies = append(replies, summaryReply, textReply)
	h := workOverflowHarness(t, overflowOpts{replies: replies, steps: []int{4, 5}, exec: bigResult("甲")})
	h.seedRounds(8)
	in := rootInput(h, "made-连读三个", "made-root-a")
	o := recv(t, h.hub.Respond(in), "两次恢复后回复")
	if o.Err != nil || o.Result == nil || o.Result.Reply != "好的" {
		t.Fatalf("两次恢复没走通：%+v", o)
	}
	body := lastBody(h)
	if !strings.Contains(body, "src/made_2.go"+strings.Repeat("甲", 5000)) {
		t.Fatal("最新一组不是全文")
	}
	if strings.Contains(body, "src/made_0.go"+strings.Repeat("甲", 5000)) || !strings.Contains(body, "较早的工具结果") {
		t.Fatal("更早的结果没有折叠成占位")
	}
	if !h.logged("折叠了这一轮更早的工具步骤") {
		t.Fatal("折叠没有记日志")
	}
	if toolPairProblems(h.liveFor(in).msgs) {
		t.Fatal("折叠后调用和结果对不上")
	}
	if eps := h.epochs(); len(eps) != 1 {
		t.Fatalf("只该压一次前面的轮次：%+v", eps)
	}
}

// 每次请求都溢出又省不下东西：报服务商原错，不无限循环
func TestOverflowNoProgressStops(t *testing.T) {
	t.Parallel()
	h := workOverflowHarness(t, overflowOpts{
		replies: []string{callReply("read", `{"path":"src/made_a.go"}`), summaryReply, textReply, textReply},
		always:  true,
	})
	h.seedRounds(8)
	in := rootInput(h, "made-读一个", "made-root-a")
	o := recv(t, h.hub.Respond(in), "没进展就停")
	if o.Err == nil || !strings.Contains(o.Err.Error(), overflowText) {
		t.Fatalf("应报服务商原错：%v", o.Err)
	}
	if n := h.postCount(); n != 4 {
		t.Fatalf("请求 %d 次，应该是调用、溢出、摘要、溢出，共 4 次", n)
	}
}

// 其他场合一轮只恢复一次；本地私聊最多 maxOverflowRecovery 次
func TestOverflowTriesByScene(t *testing.T) {
	t.Parallel()
	h := workOverflowHarness(t, overflowOpts{replies: []string{textReply}})
	base := rootInput(h, "made-x", "made-root-a")
	for name, in := range scopeVariants(base) {
		want := 1
		if name == "本地私聊" {
			want = maxOverflowRecovery
		}
		if got := maxOverflowTries(h.hub.Cfg, in); got != want {
			t.Errorf("%s：最多恢复 %d 次，应该是 %d", name, got, want)
		}
	}
}
