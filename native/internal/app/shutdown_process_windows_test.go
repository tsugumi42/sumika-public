//go:build windows

package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"sumika-native/internal/approval"
	"sumika-native/internal/embed"
)

// 只在测试子进程里执行；保留 stdin，直到宿主按句柄回收。
func TestShutdownEmbeddingChild(t *testing.T) {
	t.Parallel()
	if os.Getenv("SUMIKA_SHUTDOWN_CHILD") != "1" {
		return
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

type shutdownEmbedding struct {
	*guardLocal
	t       *testing.T
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	once    sync.Once
	onStart func()
}

func (e *shutdownEmbedding) Start() {
	e.t.Helper()
	exe, err := os.Executable()
	if err != nil {
		e.t.Fatal(err)
	}
	e.cmd = exec.Command(exe, "-test.run=^TestShutdownEmbeddingChild$")
	e.cmd.Env = append(os.Environ(), "SUMIKA_SHUTDOWN_CHILD=1")
	e.stdin, err = e.cmd.StdinPipe()
	if err != nil {
		e.t.Fatal(err)
	}
	out, err := e.cmd.StdoutPipe()
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(e.Stop)
	ready := make(chan bool, 1)
	go func() { s := bufio.NewScanner(out); ready <- s.Scan() && s.Text() == "ready" }()
	if !shutdownRecv(e.t, ready, "嵌入替身子进程启动") {
		e.t.Fatal("嵌入替身子进程未就绪")
	}
	e.guardLocal.Start()
	e.onStart()
}

func (e *shutdownEmbedding) Stop() {
	e.once.Do(func() {
		if e.cmd != nil {
			_ = e.cmd.Process.Kill()
			_ = e.stdin.Close()
			_ = e.cmd.Wait()
		}
		e.guardLocal.Stop()
	})
}

// 只持有这个 App 自己的解析器和嵌入替身进程的句柄；两个都按进程号认，不按名字、不比快照：
// 同包别的并行测试和共用解析器也在起 pwsh，"新出现的 pwsh"会认到别人身上。不按名字结束任何进程。
func shutdownProcessHandles(t *testing.T, analyzerPID, embeddingPID uint32) map[string]windows.Handle {
	t.Helper()
	handles := map[string]windows.Handle{}
	for kind, pid := range map[string]uint32{"解析器": analyzerPID, "嵌入": embeddingPID} {
		if pid == 0 {
			t.Fatalf("没拿到%s的进程号", kind)
		}
		h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, pid)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			// 改坏检查失败时也按本次持有的句柄收好子进程，不影响别的进程。
			if state, _ := windows.WaitForSingleObject(h, 0); state == uint32(windows.WAIT_TIMEOUT) {
				_ = windows.TerminateProcess(h, 1)
				_, _ = windows.WaitForSingleObject(h, 5000)
			}
			windows.CloseHandle(h)
		})
		handles[kind] = h
	}
	for kind, h := range handles {
		if state, err := windows.WaitForSingleObject(h, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
			t.Fatalf("%s 没有在清理前保持运行：%d %v", kind, state, err)
		}
	}
	return handles
}

func TestShutdownReapsProcesses(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("反复起真实子进程，放在每日整套；握手和错误分类有单测")
	}
	for _, path := range []string{"正常关机", "Open失败", "整体超时"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			memoryFile := guardFixture(t, e, true)
			if path == "Open失败" {
				if err := os.WriteFile(memoryFile, []byte("编的坏数据库"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var handles map[string]windows.Handle
			var gate *approval.Gate
			opt := e.options()
			opt.SharedAnalyzer = nil // 要查的就是这个 App 自己起的解析器进程，不能共用
			opt.OnGate = func(g *approval.Gate) { gate = g }
			opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
				local := &shutdownEmbedding{guardLocal: &guardLocal{cfg: cfg}, t: t}
				local.onStart = func() {
					// 审批门自己的解析器默认启动 3 秒后才预热：这里主动起好，再按它的进程号拿句柄
					select {
					case <-gate.Analyzer.Warmup():
					case <-time.After(30 * time.Second):
						t.Fatal("这个 App 自己的解析器 30 秒没预热好")
					}
					handles = shutdownProcessHandles(t, uint32(gate.Analyzer.PID()), uint32(local.cmd.Process.Pid))
				}
				return local
			}
			a, err := Open(context.Background(), opt)
			if path == "Open失败" {
				if a != nil {
					a.Close()
					t.Fatal("坏记忆库仍然启动成功")
				}
				if err == nil || !strings.Contains(err.Error(), "记忆") {
					t.Fatalf("没有走记忆装配失败：%v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { a.Close() })
				if path == "正常关机" {
					if err := a.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					a.shutdownWait = 30 * time.Millisecond
					entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
					var once sync.Once
					unblock := func() { once.Do(func() { close(release) }) }
					defer unblock()
					// 卡在第一步，证明超时出口不依赖走到最后的正常进程清理。
					a.logf = func(format string, args ...any) {
						text := fmt.Sprintf(format, args...)
						e.logf("%s", text)
						if strings.Contains(text, "1 停适配器 开始") {
							close(entered)
							<-release
						}
						if strings.HasPrefix(text, "关完了") {
							close(finished)
						}
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					done := make(chan error, 1)
					go func() { done <- a.Run(ctx) }()
					e.peer.accept(t)
					cancel()
					shutdownRecv(t, entered, "关机第一步屏障")
					err = shutdownRecv(t, done, "整体超时出口返回")
					// 断言前先确保失败也能放开并收好后台关机协程。
					defer func() { unblock(); shutdownRecv(t, finished, "后台关机完成") }()
					if err == nil || !strings.Contains(err.Error(), "关机超过") {
						t.Errorf("没有走整体关机超时：%v", err)
					}
				}
			}
			for kind, h := range handles {
				if state, err := windows.WaitForSingleObject(h, 0); err != nil || state != windows.WAIT_OBJECT_0 {
					t.Errorf("%s 返回后仍活着：state=%d err=%v", kind, state, err)
				}
			}
			if len(handles) != 2 {
				t.Fatalf("没有观测到两个真实子进程：%v", handles)
			}
			t.Logf("%s：已检查解析器与嵌入共 %d 个真实子进程的退出状态", path, len(handles))
		})
	}
}
