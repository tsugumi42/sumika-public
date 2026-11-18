package turn

// 规格第七节 T14：每个工具的总超时（照 Sumika 的 declare，daemon/tools/spec.ts）。对着写明的预期测：
// 时限照声明表、到点取消它、给她的那句和 Bun 逐字一样、记进 steps 是出错，这一轮接着往下走

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
)

const (
	timedOutRead   = "超时了（1 秒），没做完。可以缩小范围再试（少读一点、换更具体的条件），或者分几步做。"
	timedOutChange = "超时了（1 秒），没做完。这一步可能已经执行了一部分：先确认现在的状态（读一下文件、看一下结果），再决定要不要重做，别直接再来一遍。"
)

func TestToolTimeout(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, tool, args string
		declared         time.Duration
		want             string
	}{
		{"只读的工具（并行那条路）", "read", `{"path":"a.txt"}`, 30 * time.Second, timedOutRead},
		{"写文件（一个一个跑那条路）", "write", `{"path":"a.txt","content":"x"}`, 60 * time.Second, timedOutChange},
		{"只读的命令", "powershell", `{"command":"Get-ChildItem"}`, 630 * time.Second, timedOutRead},
		{"会改东西的命令", "powershell", `{"command":"Set-Content a.txt x"}`, 630 * time.Second, timedOutChange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// 命令测试要先让常驻 pwsh 和命令元数据就绪；拆不出来会转成等主人批，测不到工具超时。
			if tc.tool == "powershell" && analyzer.Analyze("Get-ChildItem", barrierLimit) == nil {
				t.Fatal("命令分析器没有就绪")
			}
			call, _ := json.Marshal(map[string]any{"toolCalls": []map[string]any{{"id": "t1", "name": tc.tool, "args": []string{tc.args}}}})
			replies := `[` + strings.TrimSuffix(string(call), "}") + `,` + usage + `},` + textReply + `]`
			canceled := make(chan bool, 1)
			var mu sync.Mutex
			var declared time.Duration
			h := newHarnessOpts(t, harnessOpts{replies: replies, mode: "yolo", mutate: func(c *Config) {
				c.ToolTimeout = func(name string, d time.Duration) time.Duration {
					mu.Lock()
					declared = d
					mu.Unlock()
					return time.Second
				}
				c.ExecTool = func(ctx context.Context, _, _ string, _ jsjson.Value) (tools.Result, error) {
					select {
					case <-ctx.Done():
						canceled <- true
						return tools.Result{Text: "Operation aborted", IsError: true}, nil
					case <-time.After(10 * time.Second):
						canceled <- false
						return tools.Result{Text: "没超时"}, nil
					}
				}
			}})
			h.send("做一下")
			mu.Lock()
			if declared != tc.declared {
				t.Fatalf("%s 声明的总超时应该是 %v，实际 %v", tc.tool, tc.declared, declared)
			}
			mu.Unlock()
			select {
			case c := <-canceled:
				if !c {
					t.Fatal("到点以后要取消工具")
				}
			case <-time.After(barrierLimit):
				t.Fatal("到点以后工具一直没收到取消")
			}
			var preview, status string
			var isErr int
			if err := h.st.Trace.QueryRow(`SELECT preview, is_error, status FROM steps WHERE kind = 'tool'`).Scan(&preview, &isErr, &status); err != nil {
				t.Fatal(err)
			}
			if preview != tc.want || isErr != 1 || status != "error" {
				t.Fatalf("steps 里应该是那句超时、记成出错：%q %d %s", preview, isErr, status)
			}
			if !strings.Contains(h.body(1), tc.want) {
				t.Fatalf("下一次请求里她应该看到那句超时：%s", h.body(1))
			}
		})
	}
}

// 审查查出：到点就返回，不等不理取消的工具（read、ls、todo_write 那样的）自己跑完——不然它卡住，整轮跟着卡住
func TestToolTimeoutDoesNotWait(t *testing.T) {
	t.Parallel()
	cfg := Config{ToolTimeout: func(string, time.Duration) time.Duration { return 200 * time.Millisecond }}
	release, exited := make(chan struct{}), make(chan struct{})
	defer func() { close(release); receive(t, exited) }()
	start := time.Now()
	r, err := within(context.Background(), cfg, "read", jsjson.Value{}, func(context.Context) (tools.Result, error) {
		defer close(exited)
		<-release
		return tools.Result{Text: "读完了"}, nil
	})
	if took := time.Since(start); err != nil || !r.IsError || !strings.HasPrefix(r.Text, "超时了") || took > barrierLimit { // 工具一直卡到测试放行：不守时限的改法这里过不去
		t.Fatalf("到点应该马上返回超时（用了 %v）：%v %+v", took, err, r)
	}
}

// 审查查出：时限从过了关开始算，等主人批的时间不算（Bun 的 declare 只包 execute，审批在 beforeToolCall）
func TestToolTimeoutApprovalNotCounted(t *testing.T) {
	t.Parallel()
	ran := 0
	h := newHarnessOpts(t, harnessOpts{
		mode:    "manual",
		replies: `[{"toolCalls":[{"id":"w1","name":"write","args":["{\"path\":\"new.txt\",\"content\":\"x\"}"]}],` + usage + `},` + textReply + `]`,
		mutate: func(c *Config) {
			c.ToolTimeout = func(string, time.Duration) time.Duration { return time.Second }
			c.ExecTool = func(context.Context, string, string, jsjson.Value) (tools.Result, error) {
				ran++
				return tools.Result{Text: "写好了"}, nil
			}
		},
	})
	h.approvals.OnRequest = append(h.approvals.OnRequest, func(req approval.Request) {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			h.approvals.Decide(req.ID, "once", "")
		}()
	})
	h.send("写个文件")
	var preview string
	var isErr int
	if err := h.st.Trace.QueryRow(`SELECT preview, is_error FROM steps WHERE kind = 'tool'`).Scan(&preview, &isErr); err != nil {
		t.Fatal(err)
	}
	if ran != 1 || preview != "写好了" || isErr != 0 {
		t.Fatalf("等审批的 1.5 秒不该算进 1 秒的时限：执行了 %d 次，结果 %q（出错 %d）", ran, preview, isErr)
	}
}

// 没登记的工具照最危险的那一档：60 秒、提醒可能已经执行了一部分
func TestToolTimeoutUndeclared(t *testing.T) {
	t.Parallel()
	var declared time.Duration
	cfg := Config{ToolTimeout: func(_ string, d time.Duration) time.Duration { declared = d; return time.Second }}
	r, err := within(context.Background(), cfg, "mcp_没登记的", jsjson.Value{}, func(ctx context.Context) (tools.Result, error) {
		<-ctx.Done()
		return tools.Result{Text: "Operation aborted", IsError: true}, nil
	})
	if err != nil || declared != 60*time.Second || r.Text != timedOutChange || !r.IsError {
		t.Fatalf("没登记的：%v %v %+v", err, declared, r)
	}
}
