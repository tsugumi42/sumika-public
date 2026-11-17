package policy

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"sumika-native/internal/jsstr"
	"sumika-native/internal/procreg"
	chain "sumika-native/internal/toolchain"
)

// ───────────── 命令拆开看：常驻 pwsh 解析（daemon/tools/shell.ts 的 ShellAnalyzer） ─────────────
// 用 PowerShell 自带的语法解析器把命令拆开，不执行。常驻一个 pwsh（每次新开要几百毫秒），一行进一行出；出错、超时都返回 nil，调用方当"看不清"处理

// Arg 是命令的一个参数（PowerShell 语法树里的一个节点）
type Arg struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
	// Value 常量（字符串、数字）才有：写死的值
	Value *string `json:"value"`
	// Param 开关（CommandParameterAst）的名字；-Param:值 的值在 Arg 里
	Param *string `json:"param"`
	Arg   *Arg    `json:"arg"`
	// Elements 数组（./a, ./b）的各个元素；JS 里空数组也算"有"，所以用指针分开"没有"和"空的"
	Elements *[]Arg `json:"elements"`
	// Var 变量名
	Var *string `json:"var"`
	// Bind 绑到了命令的哪个参数（静态参数绑定换成的正式名字；只有 Meta 为真的命令才有）
	Bind *string `json:"bind"`
}

// Command 是拆出来的一条命令
type Command struct {
	Name   *string `json:"name"`
	At     *int    `json:"at"`
	Meta   *bool   `json:"meta"`
	Args   []Arg   `json:"args"`
	Canon  *string `json:"canon"`
	Cmdlet *bool   `json:"cmdlet"`
	Piped  *bool   `json:"piped"`
	Pipe   *string `json:"pipe"`
	Idx    *int    `json:"idx"`
}

// Member 是一次方法调用：方法名、是不是静态的、静态方法的类型全名
type Member struct {
	Name   string  `json:"name"`
	Static bool    `json:"static"`
	Type   *string `json:"type"`
}

// Analysis 是一条命令拆开的结果（ShellAnalysis）
type Analysis struct {
	Errors     []string  `json:"errors"`
	Commands   []Command `json:"commands"`
	Methods    []string  `json:"methods"`
	Members    *[]Member `json:"members"`
	Redirects  []string  `json:"redirects"`
	RedirectAt *[]int    `json:"redirectAt"`
}

// AnalyzerStatus 是命令分析器的现状：能不能用、找到的 pwsh 在哪、最近一次为什么起不来。
// 只读状态，不启动进程；环境自检的 env.analyzer 用它。
type AnalyzerStatus struct {
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	LastErr   string `json:"lastErr,omitempty"`
}

// Analyzer 是常驻的 pwsh 解析器，一次只问一条
type Analyzer struct {
	serial    chan struct{} // 排队也算进 Analyze 的时限，预热卡住不能拖住审批
	mu        sync.Mutex    // 进程发布、回收和 Close 的直接中止不依赖排队锁
	closed    chan struct{}
	closeOnce sync.Once
	warmOnce  sync.Once
	warmDone  chan struct{}
	rewarming atomic.Bool    // 超时后的重新预热同一时间只跑一个
	rewarms   sync.WaitGroup // Close 等它退出
	shell     string
	idle      time.Duration
	path      string // 最近一次找到、启动成功的 pwsh；没起过是空
	lastErr   string // 最近一次起不来的原因；启动成功后清空
	proc      *exec.Cmd
	in        io.WriteCloser
	lines     chan string
	exit      chan struct{}
	done      chan struct{}
	timer     *time.Timer
	idleGen   uint64 // 已经排队的旧空闲回调不能回收新近用过的进程
}

// NewAnalyzer 用 PATH 里的 pwsh；闲 10 分钟自己关
func NewAnalyzer() *Analyzer {
	return &Analyzer{shell: "pwsh", idle: 10 * time.Minute, serial: make(chan struct{}, 1), closed: make(chan struct{}), warmDone: make(chan struct{})}
}

// Warmup 在后台把解析器起好；失败照旧交给 Analyze 按看不清处理，不阻塞建审批门。
// 返回的通知只表示预热结束（可能失败），重复调用不会再起进程。
func (a *Analyzer) Warmup() <-chan struct{} {
	a.warmOnce.Do(func() {
		go func() {
			defer close(a.warmDone)
			a.analyze(context.Background(), "Get-Date", time.Minute, false)
		}()
	})
	return a.warmDone
}

// WarmupLater 过一会儿再预热：启动时先让路给要紧的事，很快就关掉的（测试、改配置重建）不白起一个进程。
// 第一条命令来得更早就在那时起，不等。关了就不再起。
func (a *Analyzer) WarmupLater(d time.Duration) {
	time.AfterFunc(d, func() {
		select {
		case <-a.closed:
		default:
			a.Warmup()
		}
	})
}

func (a *Analyzer) start() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	select {
	case <-a.closed:
		return false
	default:
	}
	shellPath, ok := chain.Lookup(a.shell)
	if !ok {
		a.path, a.lastErr = "", "没找到 "+a.shell
		return false
	}
	path := shellPath.Path
	u := utf16.Encode([]rune(analyzerScript))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	args := append(append([]string(nil), analyzerArgs...), "-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd := exec.Command(path, args...)
	hideWindow(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		a.path, a.lastErr = path, "启动命令分析器失败："+err.Error()
		return false
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		a.path, a.lastErr = path, "启动命令分析器失败："+err.Error()
		return false
	}
	if err := cmd.Start(); err != nil {
		in.Close()
		out.Close()
		a.path, a.lastErr = path, "启动命令分析器失败："+err.Error()
		return false // 没装 pwsh 或起不来：当"看不清"
	}
	a.path, a.lastErr = path, ""
	// 登记到运行页的进程表；读循环结束、进程回收以后撤销
	unregister := procreg.Register(procreg.CatAnalyzer, "命令分析", cmd.Process.Pid)
	lines := make(chan string, 4)
	exit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		defer unregister()
		defer cmd.Wait()
		defer close(lines)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 64<<20)
		for sc.Scan() {
			select {
			case lines <- jsstr.Trim(sc.Text()):
			case <-exit:
				return
			}
		}
	}()
	a.proc, a.in, a.lines, a.exit, a.done = cmd, in, lines, exit, done
	return true
}

// Analyze 拆开一条命令；排队、出错、超时（timeout）都返回 nil。
func (a *Analyzer) Analyze(command string, timeout time.Duration) *Analysis {
	return a.AnalyzeContext(context.Background(), command, timeout)
}

// AnalyzeContext 同 Analyze，另外 ctx 一取消就返回 nil：时限放宽以后，点停止不能跟着多等。
//
// 等不到回复（超时或取消）时进程要杀掉：一问一答没有编号，留着它，迟到的回复会被当成下一条命令的结果，
// 拿别的命令的分析放行这一条。杀掉以后马上在后台重新预热：不然机器忙时每条命令都从冷启动算起、再撞一次时限，一直拆不开。
func (a *Analyzer) AnalyzeContext(ctx context.Context, command string, timeout time.Duration) *Analysis {
	return a.analyze(ctx, command, timeout, true)
}

func (a *Analyzer) analyze(ctx context.Context, command string, timeout time.Duration, rewarm bool) *Analysis {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case a.serial <- struct{}{}:
		defer func() { <-a.serial }()
	case <-ctx.Done():
		return nil
	case <-a.closed:
		return nil
	}
	select {
	case <-a.closed:
		return nil
	default:
	}
	if ctx.Err() != nil {
		return nil
	}
	if a.timer != nil {
		a.idleGen++
		a.timer.Stop()
	}
	if a.proc == nil && !a.start() {
		return nil
	}
	written := make(chan error, 1)
	in := a.in
	go func() {
		_, err := io.WriteString(in, base64.StdEncoding.EncodeToString([]byte(command))+"\n")
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			a.dispose()
			return nil
		}
	case <-ctx.Done():
		a.dispose()
		<-written
		if rewarm {
			a.rewarmLater()
		}
		return nil
	case <-a.closed:
		a.dispose()
		<-written
		return nil
	}
	var line string
	select {
	case l, ok := <-a.lines:
		if !ok {
			a.dispose() // 进程没了：下次重开
			return nil
		}
		line = l
	case <-ctx.Done():
		a.dispose()
		if rewarm {
			a.rewarmLater()
		}
		return nil
	case <-a.closed:
		a.dispose()
		return nil
	}
	proc, generation := a.proc, a.idleGen
	a.timer = time.AfterFunc(a.idle, func() { a.expire(proc, generation) })
	if line == "" || line == "null" {
		return nil
	}
	var r Analysis
	if json.Unmarshal([]byte(line), &r) != nil {
		return nil
	}
	return &r
}

// rewarmLater 在后台把杀掉的解析器起回来；只在持有 serial 时调。预热自己超时不再接着预热，真坏的 pwsh 不会被反复拉起。
func (a *Analyzer) rewarmLater() {
	if !a.rewarming.CompareAndSwap(false, true) {
		return
	}
	a.rewarms.Add(1)
	go func() {
		defer a.rewarms.Done()
		defer a.rewarming.Store(false)
		a.analyze(context.Background(), "Get-Date", time.Minute, false)
	}()
}

func (a *Analyzer) expire(proc *exec.Cmd, generation uint64) {
	select {
	case a.serial <- struct{}{}:
		defer func() { <-a.serial }()
		if a.proc == proc && a.idleGen == generation {
			a.dispose()
		}
	case <-a.closed:
	}
}

// PID 是常驻进程的进程号；还没起、已关掉返回 0。测试按它认"这一个解析器"的进程，不靠名字猜。
func (a *Analyzer) PID() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.proc == nil || a.proc.Process == nil {
		return 0
	}
	return a.proc.Process.Pid
}

// Status 读命令分析器现在的状态：不启动进程，只读已有状态；还没起过时查一次 PATH 里的 pwsh。
// Available 为假时 LastErr 一定有原因（没装、起不来或被关掉了）。
func (a *Analyzer) Status() AnalyzerStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := AnalyzerStatus{Path: a.path, LastErr: a.lastErr}
	select {
	case <-a.closed:
		st.Available = false
		if st.LastErr == "" {
			st.LastErr = "命令分析器已经关了"
		}
		return st
	default:
	}
	if st.Path == "" {
		if r, ok := chain.Lookup(a.shell); ok {
			st.Path = r.Path
		} else if st.LastErr == "" {
			st.LastErr = "没找到 " + a.shell
		}
	}
	st.Available = st.Path != "" && st.LastErr == ""
	if !st.Available && st.LastErr == "" {
		st.LastErr = "命令分析器起不来"
	}
	return st
}

// Close 取消预热和分析，等常驻进程退出；关掉以后不再重启。
func (a *Analyzer) Close() {
	a.closeOnce.Do(func() { close(a.closed) })
	// 写 stdin 的协程可能占着 serial，先直接杀进程解除管道阻塞。
	a.mu.Lock()
	if a.proc != nil {
		a.proc.Process.Kill()
	}
	a.mu.Unlock()
	// 和 Warmup 的启动互斥：Close 先到就不启动，Warmup 先到就等它退出。
	a.warmOnce.Do(func() { close(a.warmDone) })
	a.serial <- struct{}{}
	if a.timer != nil {
		a.timer.Stop()
	}
	a.dispose()
	<-a.serial
	<-a.warmDone
	a.rewarms.Wait() // closed 已关、serial 也轮过一次：不会再有新的重新预热
}

func (a *Analyzer) dispose() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.proc != nil {
		close(a.exit)
		a.proc.Process.Kill()
		a.in.Close()
		<-a.done
	}
	a.proc, a.in, a.lines, a.exit, a.done = nil, nil, nil, nil, nil
}
