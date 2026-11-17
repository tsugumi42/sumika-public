package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 起进程的测试：测试程序自己当子进程（环境变量 MODELS_PARITY_HELPER 决定它扮演什么），不需要真的 sumika-models 和 Python。
//
//	ok       像 sumika-models：听 --port，读 stdin，读到 EOF 就退（--exit-on-stdin-eof）
//	stubborn 像卡住的 Python：听 --port，不理 stdin，只能被杀
//	busy     第一次起来就退出码 3（端口被占），在 MODELS_PARITY_MARK 这个文件留个记号，第二次起来当 ok
//	die      起来就以退出码 7 退出
func TestMain(m *testing.M) {
	if mode := os.Getenv("MODELS_PARITY_HELPER"); mode != "" {
		helperMain(mode)
		return
	}
	os.Exit(m.Run())
}

func helperMain(mode string) {
	fs := flag.NewFlagSet("helper", flag.ContinueOnError)
	port := fs.Int("port", 0, "")
	exitEOF := fs.Bool("exit-on-stdin-eof", false, "")
	fs.Parse(os.Args[1:])
	switch mode {
	case "die":
		fmt.Fprintln(os.Stderr, "[sumika-models] 错误：编的错误")
		os.Exit(7)
	case "busy":
		mark := os.Getenv("MODELS_PARITY_MARK")
		if _, err := os.Stat(mark); err != nil {
			os.WriteFile(mark, []byte("x"), 0o600)
			os.Exit(3)
		}
	}
	svc := &fakeService{}
	go http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *port), svc.handler())
	if *exitEOF && mode != "stubborn" {
		buf := make([]byte, 64)
		for {
			if _, err := os.Stdin.Read(buf); err != nil {
				os.Exit(0)
			}
		}
	}
	select {}
}

func helperLaunch(mode string, env ...string) launch {
	return launch{Engine: "go", Exe: os.Args[0], Args: []string{"--port", "{port}", "--exit-on-stdin-eof"},
		Env: append([]string{"MODELS_PARITY_HELPER=" + mode}, env...), Stdin: true}
}

func TestStartAndStopLeavesNoProcess(t *testing.T) {
	p, err := startReady(context.Background(), helperLaunch("ok"), 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pid := p.pid()
	if !processExists(pid) {
		t.Fatal("起来以后 tasklist 里该能查到")
	}
	h, status, err := p.cli.health(context.Background())
	if err != nil || status != 200 || h.RSSBytes == 0 {
		t.Fatalf("health：%+v %d %v", h, status, err)
	}
	p.stop() // 关 stdin：它自己退
	if processExists(pid) {
		t.Fatal("stop 以后进程还在")
	}
	total, residue := stopAll()
	if total < 1 || len(residue) != 0 {
		t.Fatalf("收尾该没有残留：总共 %d，残留 %v", total, residue)
	}
}

// 不理 stdin 的（像卡住的 Python）要被 taskkill 收掉，不能留残留。
func TestStopKillsStubbornProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("反复起真实子进程，放在每日整套；握手和错误分类有单测")
	}
	p, err := startReady(context.Background(), helperLaunch("stubborn"), 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	pid := p.pid()
	old := stdinWait
	stdinWait = 500 * time.Millisecond
	defer func() { stdinWait = old }()
	p.stop()
	if processExists(pid) {
		t.Fatal("卡住的进程该被 taskkill 收掉")
	}
}

func TestPortBusyRetries(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "mark")
	p, err := startReady(context.Background(), helperLaunch("busy", "MODELS_PARITY_MARK="+mark), 20*time.Second)
	if err != nil {
		t.Fatalf("退出码 3 该换口再试一次：%v", err)
	}
	defer p.stop()
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("第一次该真的以退出码 3 退出过一次")
	}
}

func TestEarlyExitIsReportedWithTail(t *testing.T) {
	_, err := startReady(context.Background(), helperLaunch("die"), 20*time.Second)
	if err == nil || !strings.Contains(err.Error(), "退出码 7") || !strings.Contains(err.Error(), "编的错误") {
		t.Fatalf("该报退出码 7 和日志最后几行：%v", err)
	}
}
