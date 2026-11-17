package tools

// 后台命令：powershell 的 background:true 起，process_output 取新增输出，process_stop 停（连同进程树）。
// 参考：Claude Code 的 Bash run_in_background + BashOutput + KillShell；Codex 的 exec_command/write_stdin 的"等 N 秒或到结束"。
// 规格见 docs/规格-后台命令和多处编辑.md。

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/jsstr"
	"sumika-native/internal/kernel"
)

const (
	// 每个会话（角色 × 对话）同时运行的后台命令上限
	bgMaxRunning = 8
	// 每个后台命令的输出缓冲上限；超出丢最旧的
	bgBufferMax = 1 << 20
	// 已经结束的记录每个会话最多留几条，再多就丢最早结束的
	bgMaxFinished = 16
	// process_output 的 wait 上限（秒）
	bgWaitMaxS = 300
	// 停进程时最多等它真的退出多久
	bgStopGrace = 5 * time.Second
)

// bgOwner 是后台命令归谁：角色 × 对话，从宿主放进 ctx 的身份取，不看模型给的参数。
// 没有身份（测试、工具对拍直接调 Execute）时是空键，也按一个会话算
func bgOwner(ctx context.Context) string {
	s := kernel.InvocationFromContext(ctx).Scope
	return s.CharacterID + "\x00" + s.Conversation
}

type bgProc struct {
	id, owner, command string
	started            time.Time
	cmd                *exec.Cmd
	proc               *shellProcess
	done               chan struct{} // 进程退出、管道读完以后关

	mu       sync.Mutex
	dec      *jsstr.Decoder
	text     string // 还留着的输出（已解码）
	base     int    // text[0] 在全部输出里的字节位置
	read     int    // 已经交给她的位置
	ended    time.Time
	exitCode int
	stopped  bool // 是 process_stop 或退出清理停的
	timedOut bool
	reported bool // 结束的状态已经告诉过她
	startErr error
}

// Write 接命令的 stdout、stderr（同一个写入口，到达先后就是顺序）
func (p *bgProc) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.append(p.dec.Decode(b))
	return len(b), nil
}

func (p *bgProc) append(s string) {
	if s == "" {
		return
	}
	p.text += s
	if over := len(p.text) - bgBufferMax; over > 0 {
		for over < len(p.text) && p.text[over]&0xC0 == 0x80 {
			over++ // 从字的开头切
		}
		p.text = p.text[over:]
		p.base += over
	}
}

func (p *bgProc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

type bgRegistry struct {
	mu    sync.Mutex
	procs []*bgProc
	seq   atomic.Int64
}

var bg = &bgRegistry{}

func (r *bgRegistry) forOwner(owner string) (all []*bgProc, running int, finished int) {
	for _, p := range r.procs {
		if p.owner != owner {
			continue
		}
		all = append(all, p)
		if p.running() {
			running++
		} else {
			finished++
		}
	}
	return
}

func describeProcs(ps []*bgProc) string {
	if len(ps) == 0 {
		return "（本会话没有后台进程）"
	}
	var b strings.Builder
	for _, p := range ps {
		state := "运行中"
		if !p.running() {
			state = "已结束"
		}
		cmd := p.command
		if r := []rune(cmd); len(r) > 60 {
			cmd = string(r[:60]) + "…"
		}
		fmt.Fprintf(&b, "\n  %s  %s  %s", p.id, state, strings.ReplaceAll(cmd, "\n", " "))
	}
	return b.String()
}

// startBackground 起一个后台命令，立刻返回编号。命令的生命周期不跟这次工具调用的 ctx：
// 这一轮停下、回合结束它都继续跑，只有 process_stop、命令自己的 timeout 和程序退出会收掉它。
func startBackground(ctx context.Context, cwd, command string, args jsjson.Value) (string, error) {
	if ctx.Err() != nil {
		return "", errors.New("Command aborted")
	}
	if !exists(cwd) {
		return "", fmt.Errorf("Working directory does not exist: %s\nCannot execute PowerShell commands.", cwd)
	}
	owner := bgOwner(ctx)
	// 先占位再起进程：起进程可能被安全软件卡几秒，不能占着锁，也不能让并发的几次都通过上限检查
	p := &bgProc{id: fmt.Sprintf("bg-%d", bg.seq.Add(1)), owner: owner, command: command, started: time.Now(), done: make(chan struct{}), dec: jsstr.NewDecoder()}
	bg.mu.Lock()
	mine, running, finished := bg.forOwner(owner)
	if running >= bgMaxRunning {
		bg.mu.Unlock()
		return "", fmt.Errorf("这个对话里已经有 %d 个后台进程在运行，上限 %d 个，没有再起。先用 process_stop 停掉用不上的，或等它们结束：%s", running, bgMaxRunning, describeProcs(mine))
	}
	if finished >= bgMaxFinished {
		// 丢最早的一条已结束的记录
		for i, q := range bg.procs {
			if q.owner == owner && !q.running() {
				bg.procs = append(bg.procs[:i:i], bg.procs[i+1:]...)
				break
			}
		}
	}
	bg.procs = append(bg.procs, p)
	bg.mu.Unlock()

	full := utf8OutputPrefix + trashPrefix + "\n" + command
	cmd, process, err := startPowerShell(cwd, full, p)
	if err != nil {
		bg.remove(p)
		return "", err
	}
	p.mu.Lock()
	p.cmd, p.proc = cmd, process
	p.mu.Unlock()
	go p.wait()
	if v, ok := argNum(args, "timeout"); ok && v > 0 {
		time.AfterFunc(time.Duration(v*float64(time.Second)), func() {
			if p.running() {
				p.mu.Lock()
				p.timedOut = true
				p.mu.Unlock()
				process.killTree()
			}
		})
	}
	return fmt.Sprintf("后台进程已启动，编号 %s（pid %d）。命令在后台继续跑：process_output 取新增输出（可带 wait 等一会儿），process_stop 停掉它。", p.id, cmd.Process.Pid), nil
}

func (r *bgRegistry) remove(p *bgProc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, q := range r.procs {
		if q == p {
			r.procs = append(r.procs[:i:i], r.procs[i+1:]...)
			return
		}
	}
}

func (p *bgProc) wait() {
	waitErr := p.cmd.Wait()
	p.proc.close() // 作业关掉，命令留下的子进程一起收（和前台命令一样）
	p.mu.Lock()
	p.append(p.dec.Flush())
	p.ended = time.Now()
	switch {
	case p.cmd.ProcessState != nil:
		p.exitCode = p.cmd.ProcessState.ExitCode()
	default:
		p.exitCode = -1
		p.startErr = waitErr
	}
	p.mu.Unlock()
	close(p.done)
}

// take 取走还没给她的输出。取走之后位置前移；丢掉的、因为单次太长没显示的都在返回的说明里写明
func (p *bgProc) take() (text, note string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var notes []string
	if p.read < p.base {
		notes = append(notes, fmt.Sprintf("输出超过 %s 缓冲，最旧的 %s 已经丢掉，没法再取", formatSize(bgBufferMax), formatSize(p.base-p.read)))
		p.read = p.base
	}
	unread := p.text[p.read-p.base:]
	p.read = p.base + len(p.text)
	t := truncateTail(unread)
	text = t.content
	if t.truncated {
		notes = append(notes, fmt.Sprintf("这次新增的输出较长，只显示最后 %d 行 %s，前面的没显示", t.outputLines, formatSize(t.outputBytes)))
	}
	if len(notes) > 0 {
		note = "[" + strings.Join(notes, "；") + "]"
	}
	return
}

func (p *bgProc) status() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := formatSize(p.base + len(p.text))
	if p.ended.IsZero() {
		return fmt.Sprintf("%s 运行中，已运行 %d 秒，累计输出 %s", p.id, int(time.Since(p.started).Seconds()), total)
	}
	why := ""
	switch {
	case p.timedOut:
		why = "，到了 timeout 被停掉"
	case p.stopped:
		why = "，被停掉"
	}
	return fmt.Sprintf("%s 已结束，退出码 %d%s，累计输出 %s", p.id, p.exitCode, why, total)
}

func lookupBackground(ctx context.Context, args jsjson.Value, tool string) (*bgProc, error) {
	owner := bgOwner(ctx)
	var id string
	switch v := args.Obj["id"]; v.Kind {
	case jsjson.String:
		id = strings.TrimSpace(v.Str)
	case jsjson.Number:
		id = "bg-" + jsjson.Number_(v.Num)
	}
	bg.mu.Lock()
	defer bg.mu.Unlock()
	mine, _, _ := bg.forOwner(owner)
	if id == "" {
		return nil, fmt.Errorf("%s 需要参数 id：启动后台命令时返回的编号，形如 bg-1。本对话现有：%s", tool, describeProcs(mine))
	}
	for _, p := range mine {
		if p.id == id {
			return p, nil
		}
	}
	// 别的对话的编号一律当不存在，不透露它在不在
	return nil, fmt.Errorf("没有编号 %s 的后台进程（编号只在启动它的对话里有效，已结束的记录也只留最近 %d 条）。本对话现有：%s", id, bgMaxFinished, describeProcs(mine))
}

func processOutput(ctx context.Context, args jsjson.Value) (string, error) {
	p, err := lookupBackground(ctx, args, "process_output")
	if err != nil {
		return "", err
	}
	if w, ok := argNum(args, "wait"); ok && w > 0 && p.running() {
		timer := time.NewTimer(time.Duration(min(w, bgWaitMaxS) * float64(time.Second)))
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
		case <-ctx.Done():
			return "", errors.New("Operation aborted")
		}
	}
	text, note := p.take()
	if !p.running() {
		p.mu.Lock()
		again := p.reported && text == "" && note == ""
		p.reported = true
		p.mu.Unlock()
		if again {
			return fmt.Sprintf("[%s]\n这个进程已经结束，输出之前都取完了，没有新内容。要重新跑就再用 powershell 起一个。", p.status()), nil
		}
	}
	return joinBgText(text, note, p.status()), nil
}

func joinBgText(text, note, status string) string {
	var parts []string
	if note != "" {
		parts = append(parts, note)
	}
	if text != "" {
		parts = append(parts, text)
	} else {
		parts = append(parts, "(没有新输出)")
	}
	parts = append(parts, "["+status+"]")
	return strings.Join(parts, "\n\n")
}

func processStop(ctx context.Context, args jsjson.Value) (string, error) {
	p, err := lookupBackground(ctx, args, "process_stop")
	if err != nil {
		return "", err
	}
	if !p.running() {
		text, note := p.take()
		p.mu.Lock()
		p.reported = true
		p.mu.Unlock()
		if text == "" && note == "" {
			return fmt.Sprintf("[%s]\n它已经自己结束了，不用再停。", p.status()), nil
		}
		return joinBgText(text, note, p.status()+"，不用再停"), nil
	}
	if err := p.stop(); err != nil {
		return "", err
	}
	text, note := p.take()
	p.mu.Lock()
	p.reported = true
	p.mu.Unlock()
	return joinBgText(text, note, p.status()), nil
}

// stop 杀整棵进程树并等它真的退出。停不掉就报错，不假装停了
func (p *bgProc) stop() error {
	p.mu.Lock()
	p.stopped = true
	proc := p.proc
	p.mu.Unlock()
	if proc == nil {
		return nil // 还在起进程，没有东西可停（只在退出的一瞬间碰得到）
	}
	killErr := proc.killTree()
	select {
	case <-p.done:
		return nil
	case <-time.After(bgStopGrace):
		return errors.Join(fmt.Errorf("发了停止，但 %s 在 %v 内还没退出。稍后再用 process_output 看状态，或再 process_stop 一次", p.id, bgStopGrace), killErr)
	}
}

// StopAllBackground 在程序退出时收掉所有还在跑的后台命令（连进程树），等它们退出，最多等 bgStopGrace
func StopAllBackground() {
	bg.mu.Lock()
	all := append([]*bgProc(nil), bg.procs...)
	bg.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range all {
		if !p.running() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.stop()
		}()
	}
	wg.Wait()
}
