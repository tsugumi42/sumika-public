package userhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sumika-native/internal/taskrun"
	"sumika-native/internal/tools"
)

// 单路输出在内存里最多留多少（头 1/4、尾 3/4）。给模型时再按配置截
const captureBytes = 256 << 10

// 被停下的类别（taskrun 没有这个词：取消本身不是失败原因）
const classCancelled = "cancelled"

// Runner 真正跑命令，测试换成假的
type Runner func(ctx context.Context, c tools.ShellCommand) (int, error)

// capture 只留头尾，几 GB 的输出也不会撑爆后台
type capture struct {
	mu    sync.Mutex
	head  []byte
	tail  []byte
	total int
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += len(p)
	rest := p
	if room := captureBytes/4 - len(c.head); room > 0 {
		n := min(room, len(rest))
		c.head = append(c.head, rest[:n]...)
		rest = rest[n:]
	}
	if len(rest) > 0 {
		c.tail = append(c.tail, rest...)
		if keep := captureBytes * 3 / 4; len(c.tail) > keep {
			c.tail = append(c.tail[:0], c.tail[len(c.tail)-keep:]...)
		}
	}
	return len(p), nil
}

// text 是收到的文字：超了就是头 + 省略说明 + 尾。complete 说的是一个字节都没丢（能当 JSON 解）
func (c *capture) text() (s string, complete bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := len(c.head) + len(c.tail)
	if kept == c.total {
		return string(c.head) + string(c.tail), true
	}
	return string(c.head) + fmt.Sprintf("\n…中间省略 %d 字节…\n", c.total-kept) + string(c.tail), false
}

var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

func cleanOutput(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = ansiEscape.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s)
}

// clipMiddle 截到最多 n 字节，留头 1/4、尾 3/4（构建报错的结论常在末尾），不切断一个字
func clipMiddle(s string, n int) string {
	if len(s) <= n {
		return s
	}
	head, tail := n/4, n-n/4
	for head > 0 && !utf8.RuneStart(s[head]) {
		head--
	}
	cut := len(s) - tail
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[:head] + fmt.Sprintf("\n…中间省略 %d 字节…\n", cut-head) + s[cut:]
}

// outcome 是一条命令跑完的结果
type outcome struct {
	handler Handler
	code    int
	stdout  string
	stderr  string
	json    *hookJSON
	class   string // 空表示成功
	result  string // taskrun 的结局
	failure string // 给运行页和模型的一句话原因（没有就空）
}

// hookJSON 是钩子回的 JSON（Claude Code 的字段）
type hookJSON struct {
	Continue      *bool  `json:"continue"`
	StopReason    string `json:"stopReason"`
	SystemMessage string `json:"systemMessage"`
	Decision      string `json:"decision"`
	Reason        string `json:"reason"`
	Specific      struct {
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// parseOutput：完整收到、以 { 开头、能解开的才算 JSON；别的都是普通输出
func parseOutput(stdout string, complete bool) *hookJSON {
	t := strings.TrimSpace(stdout)
	if !complete || !strings.HasPrefix(t, "{") {
		return nil
	}
	var v hookJSON
	if err := json.Unmarshal([]byte(t), &v); err != nil {
		return nil
	}
	return &v
}

// runOne 跑一条命令。ctx 是这一轮的，取消时杀进程树
func (p *Plugin) runOne(ctx context.Context, event string, h Handler, dir string, env []string, stdin []byte) outcome {
	out := outcome{handler: h, code: -1, result: taskrun.OutcomeOK}
	ctx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()
	var so, se capture
	code, err := p.run(ctx, tools.ShellCommand{Shell: h.Shell, Dir: dir, Command: h.Command, Env: env, Stdin: stdin, Stdout: &so, Stderr: &se})
	stdout, complete := so.text()
	stderr, _ := se.text()
	out.stdout, out.stderr, out.code = cleanOutput(stdout), cleanOutput(stderr), code
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		out.result, out.class = taskrun.OutcomeTimeout, taskrun.ClassTimeout
		out.failure = fmt.Sprintf("超时了（%d 秒），已结束，这次没有结果", int(h.Timeout.Seconds()))
	case ctx.Err() != nil:
		out.result, out.class = taskrun.OutcomeCancelled, classCancelled
	case err != nil:
		out.result, out.class = taskrun.OutcomeFailed, taskrun.ClassSpawnFailed
		out.failure = "没跑起来：" + err.Error()
	case code == 0:
		out.json = parseOutput(stdout, complete)
		out.classifyJSON(event)
	case code == 2:
		out.result, out.class = taskrun.OutcomeFailed, taskrun.ClassCheckFailed
	default:
		out.result, out.class = taskrun.OutcomeFailed, taskrun.ClassExitNonzero
	}
	return out
}

// classifyJSON 把 JSON 里说的事记进结局：decision:block 算检查没过；用不了的写法单独记，让主人看得到
func (o *outcome) classifyJSON(event string) {
	j := o.json
	if j == nil {
		return
	}
	switch {
	case j.Decision == "block" && strings.TrimSpace(j.Reason) == "":
		o.result, o.class = taskrun.OutcomeFailed, taskrun.ClassInvalidOutput
		o.failure = "decision 是 block 但没给 reason"
	case j.Decision == "block":
		o.result, o.class = taskrun.OutcomeFailed, taskrun.ClassCheckFailed
	case event == EventPostToolUse && j.Continue != nil && !*j.Continue:
		o.result, o.class = taskrun.OutcomeDegraded, taskrun.ClassInvalidOutput
		o.failure = "continue:false 在 PostToolUse 上不会结束这一轮"
	}
}

// label 是这条钩子在给模型的尾注和运行页里的名字
func (h Handler) label() string {
	if h.Label != "" {
		return clipRunes(oneLine(h.Label), 80)
	}
	return clipRunes(oneLine(h.Command), 80)
}

// program 是命令的程序名，给时间线用：不带路径和参数
func (h Handler) program() string {
	f := strings.Fields(oneLine(h.Command))
	if len(f) == 0 {
		return ""
	}
	name := strings.Trim(f[0], `"'&`)
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	return clipRunes(name, 40)
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	return s
}

func clipRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

// firstLine 是输出的第一行，给运行页的详情
func firstLine(parts ...string) string {
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			if i := strings.IndexByte(p, '\n'); i >= 0 {
				p = p[:i]
			}
			return clipRunes(p, 120)
		}
	}
	return ""
}

// runAll 同时跑这几条（全局最多 maxParallel 条，等名额时也看取消），结果按配置顺序。进度和耗时只在真跑时留。
func (p *Plugin) runAll(ctx context.Context, event, tool string, handlers []Handler, dir string, env []string, stdin []byte, rootID string, span func(string, string) func(bool), process func(step processStep)) []outcome {
	outs := make([]outcome, len(handlers))
	var wg sync.WaitGroup
	for i, h := range handlers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			record := p.opts.Degrade.Start(taskrun.ModuleHooks)
			record.RootID = rootID
			id := fmt.Sprintf("hook:%d", p.seq.Add(1))
			if process != nil {
				process(processStep{id: id, event: event, handler: h, state: "running", started: started})
			}
			var end func(bool)
			if span != nil {
				end = span(spanKind(event), h.program())
			}
			select {
			case p.slots <- struct{}{}:
				outs[i] = p.runOne(ctx, event, h, dir, env, stdin)
				<-p.slots
			case <-ctx.Done():
				outs[i] = outcome{handler: h, code: -1, result: taskrun.OutcomeCancelled, class: classCancelled}
			}
			o := outs[i]
			if end != nil {
				end(o.result != taskrun.OutcomeOK)
			}
			if process != nil {
				process(processStep{id: id, event: event, handler: h, state: stepState(o), started: started, output: firstLine(o.failure, o.stderr, o.stdout)})
			}
			var detail error
			if o.result != taskrun.OutcomeOK {
				detail = errors.New(detailOf(event, tool, o))
			}
			record.End(o.result, o.class, detail)
		}()
	}
	wg.Wait()
	return outs
}

func spanKind(event string) string {
	if event == EventStop {
		return "hook.stop"
	}
	return "hook.post_tool"
}

func stepState(o outcome) string {
	switch o.result {
	case taskrun.OutcomeOK:
		return "done"
	case taskrun.OutcomeCancelled:
		return "stopped"
	}
	return "failed"
}

func detailOf(event, tool string, o outcome) string {
	where := event
	if tool != "" {
		where += " · " + tool
	}
	line := firstLine(o.failure, o.stderr, o.stdout)
	switch {
	case o.code > 0:
		return fmt.Sprintf("%s · %s · 退出码 %d · %s", where, o.handler.label(), o.code, line)
	case line != "":
		return where + " · " + o.handler.label() + " · " + line
	}
	return where + " · " + o.handler.label()
}
