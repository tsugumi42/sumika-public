package turn

// 工具闭环的契约（规格-Go后台 8.2 A2、A3）：对着写明的预期测，不和 Bun 比。真执行换成假工具（Config.ExecTool），能卡住、能记次数、
// 能定谁先跑完；假网关收到下一次请求时先看库（结果要先存好再请求）

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

const usage = `"usage":{"prompt_tokens":100,"completion_tokens":10}`

// toolMsgs 是第 i 次请求里的工具结果：调用 id → 内容，和它们的先后
func (h *harness) toolMsgs(i int) (map[string]string, []string) {
	byID, order := map[string]string{}, []string{}
	for _, m := range h.messages(i) {
		var x struct {
			Role, Content string
			ID            string `json:"tool_call_id"`
		}
		json.Unmarshal([]byte(m), &x)
		if x.Role == "tool" {
			byID[x.ID] = x.Content
			order = append(order, x.ID)
		}
	}
	return byID, order
}

// endedSteps 是库里这几个调用记了结束的有几个
func (h *harness) endedSteps(ids ...string) int {
	n := 0
	for _, id := range ids {
		var ended *int64
		if err := h.st.Trace.QueryRow(`SELECT ended_at FROM steps WHERE provider_call_id = ?`, id).Scan(&ended); err == nil && ended != nil {
			n++
		}
	}
	return n
}

// A3：并行的一批真的同时跑（三个都开始了才放行，一个一个跑会卡住）；结果按调用的先后接回去，不按谁先跑完；
// 三个都结束、都记进库之前不发下一次请求
func TestParallelToolsRunTogether(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	started, all := 0, make(chan struct{})
	release := map[string]chan struct{}{"d1": make(chan struct{}), "d2": make(chan struct{}), "d3": make(chan struct{})}
	var finished []string
	ended := -1
	var h *harness
	h = newHarnessOpts(t, harnessOpts{
		replies: `[{"toolCalls":[{"id":"a","name":"ls","args":["{\"path\":\"d1\"}"]},{"id":"b","name":"ls","args":["{\"path\":\"d2\"}"]},{"id":"c","name":"ls","args":["{\"path\":\"d3\"}"]}],` + usage + `},
			{"content":["好了"],` + usage + `}]`,
		onRequest: func(n int) {
			if n == 2 {
				ended = h.endedSteps("a", "b", "c")
			}
		},
		mutate: func(c *Config) {
			c.ExecTool = func(ctx context.Context, _, name string, args jsjson.Value) (tools.Result, error) {
				p := args.Obj["path"].Str
				mu.Lock()
				started++
				if started == 3 {
					close(all)
					close(release["d3"]) // 第三个先跑完，再第二个、第一个
				}
				mu.Unlock()
				select {
				case <-all:
				case <-time.After(barrierLimit):
					return tools.Result{Text: "没等到别的工具一起跑"}, nil
				}
				<-release[p]
				mu.Lock()
				finished = append(finished, p)
				switch p {
				case "d3":
					close(release["d2"])
				case "d2":
					close(release["d1"])
				}
				mu.Unlock()
				return tools.Result{Text: "目录 " + p}, nil
			}
		},
	})
	h.send("看看三个目录")
	byID, order := h.toolMsgs(1)
	for _, id := range []string{"a", "b", "c"} {
		if strings.Contains(byID[id], "没等到") {
			t.Fatalf("三个工具没有同时跑（%s 等不到别的）：%v", id, byID)
		}
	}
	if strings.Join(finished, ",") != "d3,d2,d1" {
		t.Fatalf("跑完的先后应该是 d3,d2,d1，实际 %v", finished)
	}
	if strings.Join(order, ",") != "a,b,c" || byID["a"] != "目录 d1" || byID["c"] != "目录 d3" {
		t.Fatalf("结果应该按调用的先后 a,b,c 接回去：%v %v", order, byID)
	}
	if ended != 3 {
		t.Fatalf("发第二次请求时库里记了结束的只有 %d 个（应该 3 个都记好）", ended)
	}
}

// A3 / 7.1 A24：一批里三个一样的调用，防卡死按调用的先后数——提醒落在第三个调用上，不管谁先跑完
func TestParallelGuardCountsByCallOrder(t *testing.T) {
	t.Parallel()
	gate := map[string]chan struct{}{"a": make(chan struct{}), "b": make(chan struct{}), "c": make(chan struct{})}
	var mu sync.Mutex
	n := 0
	// purpose 不算进"一样的调用"（guard.go），假工具靠它认出自己是哪个调用：c 先跑完，再 b、a
	h := newHarnessOpts(t, harnessOpts{
		replies: `[{"toolCalls":[{"id":"a","name":"ls","args":["{\"path\":\"same\",\"purpose\":\"a\"}"]},{"id":"b","name":"ls","args":["{\"path\":\"same\",\"purpose\":\"b\"}"]},{"id":"c","name":"ls","args":["{\"path\":\"same\",\"purpose\":\"c\"}"]}],` + usage + `},
			{"content":["好了"],` + usage + `}]`,
		mutate: func(c *Config) {
			c.ExecTool = func(ctx context.Context, _, _ string, args jsjson.Value) (tools.Result, error) {
				me := args.Obj["purpose"].Str
				mu.Lock()
				n++
				if n == 3 {
					close(gate["c"])
				}
				mu.Unlock()
				<-gate[me]
				mu.Lock()
				switch me {
				case "c":
					close(gate["b"])
				case "b":
					close(gate["a"])
				}
				mu.Unlock()
				return tools.Result{Text: "同样的结果"}, nil
			}
		},
	})
	h.send("看三遍")
	byID, _ := h.toolMsgs(1)
	const nudge = "提醒：你已经连续 3 次"
	if !strings.Contains(byID["c"], nudge) || strings.Contains(byID["a"], nudge) || strings.Contains(byID["b"], nudge) {
		t.Fatalf("提醒应该只落在第三个调用 c 上：%v", byID)
	}
}

// A2：参数解析不了、没有这个工具的，一个都不执行、给对得上号的错误结果；能修的参数（不合法的反斜杠）修好再执行，只执行一次；
// 结果都先记进库，再发下一次请求
func TestBadToolCallsNotExecuted(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var ran []string
	var h *harness
	ended := -1
	h = newHarnessOpts(t, harnessOpts{
		replies: `[{"toolCalls":[{"id":"x","name":"ls","args":["{\"path\": "]},{"id":"y","name":"nope","args":["{}"]},{"id":"z","name":"ls","args":["{\"path\":\"sub\\dir\"}"]}],` + usage + `},
			{"content":["好了"],` + usage + `}]`,
		onRequest: func(n int) {
			if n == 2 {
				ended = h.endedSteps("x", "y", "z")
			}
		},
		mutate: func(c *Config) {
			c.ExecTool = func(_ context.Context, _, name string, args jsjson.Value) (tools.Result, error) {
				mu.Lock()
				ran = append(ran, name+":"+args.Obj["path"].Str)
				mu.Unlock()
				return tools.Result{Text: "列好了"}, nil
			}
		},
	})
	h.send("列一下")
	byID, _ := h.toolMsgs(1)
	if !strings.HasPrefix(byID["x"], `Invalid arguments for tool "ls": `) || !strings.Contains(byID["x"], "Received arguments:\n{\"path\": ") {
		t.Errorf("解析不了的参数应该报出原文：%q", byID["x"])
	}
	if byID["y"] != "Tool nope not found" {
		t.Errorf("没有这个工具：%q", byID["y"])
	}
	if strings.Join(ran, ";") != `ls:sub\dir` {
		t.Fatalf("应该只执行修好参数的那一个、一次：%v", ran)
	}
	if ended != 3 {
		t.Fatalf("发第二次请求时库里记了结束的只有 %d 个", ended)
	}
}

// A2：回到长度上限（finish_reason length）的那一步，参数可能被截断，一个都不执行
func TestTruncatedToolCallsNotExecuted(t *testing.T) {
	t.Parallel()
	ran := 0
	h := newHarnessOpts(t, harnessOpts{
		replies: `[{"toolCalls":[{"id":"t1","name":"ls","args":["{\"path\":\".\"}"]}],"finish":"length",` + usage + `},
			{"content":["好了"],` + usage + `}]`,
		mutate: func(c *Config) {
			c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				ran++
				return tools.Result{Text: "x"}, nil
			}
		},
	})
	h.send("列一下")
	byID, _ := h.toolMsgs(1)
	if ran != 0 || !strings.Contains(byID["t1"], "was not executed: the response hit the output token limit") {
		t.Fatalf("截断的那一步不该执行（执行了 %d 次）：%q", ran, byID["t1"])
	}
}

// 7.2 T9：修参数照 json-parse.js repairJson
func TestRepairJSON(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{`{"a":"x\y"}`, `{"a":"x\\y"}`},              // 不合法的转义：反斜杠变两个
		{"{\"a\":\"一\n二\"}", `{"a":"一\n二"}`},         // 字符串里的裸换行
		{"{\"a\":\"\x01\"}", `{"a":"\u0001"}`},       // 别的控制字符
		{"{\"a\":\"\t\b\f\r\"}", `{"a":"\t\b\f\r"}`}, // 有短写法的几个
		{`{"a":"é \u00zz"}`, `{"a":"é \\u00zz"}`},
		{`{"a":"\"\\\/\n"}`, `{"a":"\"\\\/\n"}`}, // 合法的转义不动
		{`{"a":"x\`, `{"a":"x\\`},                // 结尾落单的反斜杠
		{"{\n\"a\" : 1\n}", "{\n\"a\" : 1\n}"},   // 字符串外面的不动
		{`{"a":"\u12"}`, `{"a":"\\u12"}`},        // \u 后面不够四位
	}
	for _, c := range cases {
		if got := repairJSON(c.in); got != c.want {
			t.Errorf("repairJSON(%q) = %q，应该是 %q", c.in, got, c.want)
		}
	}
	// \b 是合法的转义（退格）不动，\c 不合法变成 \\c
	if v, e := parseArgs(`{"path":"a\b\c"}`); e != "" || v.Obj["path"].Str != "a\b\\c" {
		t.Errorf("修过以后应该解析得出来：%q %q", v.Obj["path"].Str, e)
	}
	if _, e := parseArgs(`{"path": `); !strings.Contains(e, "Received arguments:\n{\"path\": ") {
		t.Errorf("修不好的照报原文：%q", e)
	}
}
