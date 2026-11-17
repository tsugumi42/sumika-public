package policy

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	chain "sumika-native/internal/toolchain"
)

// 用本测试进程代替 pwsh，真正走 stdin/stdout 和进程回收；控制连接决定何时回复，不靠睡眠。
func TestAnalyzerProcess(t *testing.T) {
	addr := os.Getenv("SUMIKA_ANALYZER_TEST_ADDR")
	if addr == "" {
		return
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		os.Exit(2)
	}
	defer conn.Close()
	in, control := bufio.NewScanner(os.Stdin), bufio.NewScanner(conn)
	if os.Getenv("SUMIKA_ANALYZER_TEST_BLOCK_STDIN") == "1" {
		fmt.Fprintln(conn, "不读 stdin")
		control.Scan() // 只等控制连接关闭，永远不读 stdin
		os.Exit(0)
	}
	for in.Scan() {
		b, err := base64.StdEncoding.DecodeString(in.Text())
		if err != nil {
			os.Exit(3)
		}
		fmt.Fprintln(conn, string(b))
		if !control.Scan() {
			os.Exit(4)
		}
		fmt.Fprintln(os.Stdout, control.Text())
	}
	os.Exit(0)
}

type analyzerProcess struct {
	conn     net.Conn
	requests *bufio.Scanner
}

func controlledAnalyzer(t *testing.T) (*Analyzer, <-chan *analyzerProcess) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	t.Setenv("SUMIKA_ANALYZER_TEST_ADDR", listener.Addr().String())
	old := analyzerArgs
	analyzerArgs = []string{"-test.run=^TestAnalyzerProcess$", "--"}
	t.Cleanup(func() { analyzerArgs = old })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// 登记表里 pwsh 指向测试进程本身：控制连接决定何时回复，真正走 stdin/stdout。
	chain.Refresh(chain.Env{
		LookPath: func(name string) (string, error) {
			if name == "pwsh" || name == "pwsh.exe" {
				return exe, nil
			}
			return "", exec.ErrNotFound
		},
		Exists: func(string) bool { return false },
	})
	t.Cleanup(func() { chain.Refresh(chain.Env{}) })
	a := NewAnalyzer()
	connected := make(chan *analyzerProcess, 8) // 超时杀掉以后重新预热的进程也从这里来
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.SetDeadline(time.Now().Add(2 * time.Minute))
			connected <- &analyzerProcess{conn: conn, requests: bufio.NewScanner(conn)}
		}
	}()
	t.Cleanup(a.Close)
	return a, connected
}

// 只防真卡住：比预热的一分钟时限短，排队不守自己时限的改法照样红；子进程是测试程序本身，机器忙时光起来就要好几秒。
const analyzerRecvLimit = 30 * time.Second

func analyzerRecv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(analyzerRecvLimit):
		t.Fatalf("等%s超过 %v", what, analyzerRecvLimit)
		var zero T
		return zero
	}
}

func (p *analyzerProcess) request(t *testing.T, want string) {
	t.Helper()
	if !p.requests.Scan() || p.requests.Text() != want {
		t.Fatalf("解析进程应该收到 %q，得到 %q，错误 %v", want, p.requests.Text(), p.requests.Err())
	}
}

func (a *Analyzer) processSnapshot() (*exec.Cmd, <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.proc, a.done
}

func TestAnalyzerWarmupReusesProcess(t *testing.T) {
	a, connected := controlledAnalyzer(t)
	ready := a.Warmup()
	p := analyzerRecv(t, connected, "预热进程连接")
	defer p.conn.Close()
	p.request(t, "Get-Date")
	fmt.Fprintln(p.conn, `{"commands":[{"name":"Get-Date"}]}`)
	analyzerRecv(t, ready, "预热结束")
	proc, _ := a.processSnapshot()
	done := make(chan *Analysis, 1)
	go func() { done <- a.Analyze("Get-ChildItem", time.Minute) }()
	p.request(t, "Get-ChildItem")
	fmt.Fprintln(p.conn, `{"commands":[{"name":"Get-ChildItem"}]}`)
	got := analyzerRecv(t, done, "首条分析结果")
	current, _ := a.processSnapshot()
	if got == nil || len(got.Commands) != 1 || got.Commands[0].Name == nil || *got.Commands[0].Name != "Get-ChildItem" || current != proc {
		t.Fatalf("首条 Analyze 应复用预热进程并收到自己的结果：%+v", got)
	}
	a.Close()
	if proc.ProcessState == nil {
		t.Fatalf("Close 返回时进程还没回收：%v", proc.ProcessState)
	}
	if a.Analyze("Get-Date", time.Second) != nil {
		t.Fatal("关掉以后不能再起进程")
	}
}

func TestAnalyzerWarmupKeepsDeadlineAndCloses(t *testing.T) {
	a, connected := controlledAnalyzer(t)
	ready := a.Warmup()
	p := analyzerRecv(t, connected, "卡住的预热进程")
	defer p.conn.Close()
	p.request(t, "Get-Date") // 不给回复，预热一直占着解析器
	proc, exited := a.processSnapshot()
	done := make(chan *Analysis, 1)
	// 预热占着解析器、永远不回：不守自己时限的改法要等预热的一分钟，analyzerRecv 先红。
	go func() { done <- a.Analyze("Remove-Item a.txt", 30*time.Millisecond) }()
	if got := analyzerRecv(t, done, "排队分析按自己的时限返回"); got != nil {
		t.Fatalf("预热没好应当按拆不开返回：%+v", got)
	}
	closed := make(chan struct{})
	go func() { a.Close(); close(closed) }()
	analyzerRecv(t, closed, "关闭卡住的预热")
	analyzerRecv(t, ready, "预热协程退出")
	analyzerRecv(t, exited, "读管道协程及子进程退出")
	if proc.ProcessState == nil {
		t.Fatal("Close 没等进程回收")
	}
}

func TestAnalyzerFailuresReturnUnclear(t *testing.T) {
	t.Run("起不来", func(t *testing.T) {
		a := NewAnalyzer()
		a.shell = filepath.Join(t.TempDir(), "不存在的解析器")
		defer a.Close()
		analyzerRecv(t, a.Warmup(), "失败的预热结束")
		if a.Analyze("Remove-Item a.txt", time.Second) != nil {
			t.Fatal("分析器起不来只能返回拆不开")
		}
	})
	// 超时和取消走同一个 ctx：这里用取消，确定进程收到命令之后才放弃，不赌起进程要多久。
	t.Run("分析超时", func(t *testing.T) {
		a, connected := controlledAnalyzer(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan *Analysis, 1)
		go func() { done <- a.AnalyzeContext(ctx, "Remove-Item a.txt", time.Minute) }()
		p := analyzerRecv(t, connected, "分析进程")
		defer p.conn.Close()
		p.request(t, "Remove-Item a.txt")
		exitedProc, exited := a.processSnapshot()
		cancel()
		if got := analyzerRecv(t, done, "超时分析"); got != nil {
			t.Fatalf("分析超时只能返回拆不开：%+v", got)
		}
		analyzerRecv(t, exited, "超时进程回收")
		current, _ := a.processSnapshot()
		if current == exitedProc || exitedProc.ProcessState == nil {
			t.Fatal("超时后不能拿旧进程的迟到回复给下一条命令")
		}
		// 马上在后台重新预热：机器忙时不能让下一条命令再从冷启动算起
		warm := analyzerRecv(t, connected, "重新预热的进程")
		defer warm.conn.Close()
		warm.request(t, "Get-Date")
		fmt.Fprintln(warm.conn, `{"commands":[{"name":"Get-Date"}]}`)
		go func() { done <- a.Analyze("Get-ChildItem", time.Minute) }()
		warm.request(t, "Get-ChildItem")
		fmt.Fprintln(warm.conn, `{"commands":[{"name":"Get-ChildItem"}]}`)
		got := analyzerRecv(t, done, "下一条命令")
		if got == nil || len(got.Commands) != 1 || got.Commands[0].Name == nil || *got.Commands[0].Name != "Get-ChildItem" {
			t.Fatalf("下一条命令应该用预热好的进程、收到自己的结果：%+v", got)
		}
		select {
		case extra := <-connected:
			extra.conn.Close()
			t.Fatal("预热好了还多起了一个进程")
		default:
		}
	})
	t.Run("已经取消的不起进程", func(t *testing.T) {
		a, connected := controlledAnalyzer(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if got := a.AnalyzeContext(ctx, "Remove-Item a.txt", time.Minute); got != nil {
			t.Fatalf("取消了只能返回拆不开：%+v", got)
		}
		if proc, _ := a.processSnapshot(); proc != nil {
			t.Fatal("取消了不该起解析进程")
		}
		select {
		case <-connected:
			t.Fatal("取消了不该起解析进程")
		default:
		}
	})
}

func TestAnalyzerBlockedInput(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(fmt.Sprint("关闭=", closing), func(t *testing.T) {
			t.Setenv("SUMIKA_ANALYZER_TEST_BLOCK_STDIN", "1")
			a, connected := controlledAnalyzer(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan *Analysis, 1)
			go func() { done <- a.AnalyzeContext(ctx, strings.Repeat("x", 4<<20), time.Minute) }()
			p := analyzerRecv(t, connected, "不读 stdin 的子进程")
			defer p.conn.Close() // 改坏时也能让子进程退出，测试不能挂在清理上
			p.request(t, "不读 stdin")
			proc, exited := a.processSnapshot()
			closed := make(chan struct{})
			// 子进程不读 stdin，写管道永远写不完：放弃等写入的改法在这里就卡到 analyzerRecv 的时限，不用量多快
			if closing {
				go func() { a.Close(); close(closed) }()
			} else {
				cancel()
			}
			if got := analyzerRecv(t, done, "阻塞写入退成拆不开"); got != nil {
				t.Fatalf("阻塞写入不能得到分析结果：%+v", got)
			}
			if closing {
				analyzerRecv(t, closed, "并发 Close 返回")
			}
			analyzerRecv(t, exited, "阻塞写入的读管道及子进程回收")
			if proc.ProcessState == nil {
				t.Fatal("Analyze 返回时子进程还没有回收")
			}
		})
	}
}

func TestAnalyzerOldIdleCallback(t *testing.T) {
	a, connected := controlledAnalyzer(t)
	analyzed := make(chan *Analysis, 1)
	go func() { analyzed <- a.Analyze("first", time.Minute) }()
	p := analyzerRecv(t, connected, "常驻解析器")
	defer p.conn.Close()
	p.request(t, "first")
	fmt.Fprintln(p.conn, `{"commands":[]}`)
	analyzerRecv(t, analyzed, "第一次分析")
	a.serial <- struct{}{}
	proc, generation := a.proc, a.idleGen
	<-a.serial
	go func() { analyzed <- a.Analyze("second", time.Minute) }()
	p.request(t, "second") // 新分析已经占着 serial，旧回调只能排在它后面
	entered, expired := make(chan struct{}), make(chan struct{})
	go func() {
		close(entered)
		a.expire(proc, generation)
		close(expired)
	}()
	analyzerRecv(t, entered, "旧空闲回调开始")
	fmt.Fprintln(p.conn, `{"commands":[]}`)
	analyzerRecv(t, analyzed, "新分析结束")
	analyzerRecv(t, expired, "旧空闲回调结束")
	current, _ := a.processSnapshot()
	if current != proc {
		t.Fatal("旧空闲回调回收了刚用过的进程")
	}
	go func() { analyzed <- a.Analyze("third", time.Minute) }()
	p.request(t, "third")
	fmt.Fprintln(p.conn, `{"commands":[]}`)
	if analyzerRecv(t, analyzed, "复用后的分析") == nil {
		t.Fatal("旧回调之后仍应当复用进程得到分析结果")
	}
}
