package main

// cmd/sumika-go 的测试：参数校验；用假协议端（OneBot 正向 WS）和假网关（llmtest）起一遍完整的 App，
// 取消 ctx 以后干净退出、退出码 0，日志文件里有启动那一行和关机那一行、没有令牌。
// 号、令牌、模型都是编的；数据目录只用 t.TempDir()，不碰真的 data/（规格-Go后台装配 第八节第 5 步）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	backend "sumika-native/internal/app"
	"sumika-native/internal/config"
	"sumika-native/internal/filelog"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
)

// 测试里要连的号、主人、群、令牌、模型、角色都是编的
const (
	testAccount = "100"
	testOwner   = "200"
	testGroup   = "300"
	testToken   = "编的令牌"
	testModel   = "编的模型"
	testChar    = "example"
)

// startLimit 是等后台起来连上协议端的上限，只防卡死：空闲时 4 秒多，低优先级跑在忙机器上量到过 20 秒以上。
const startLimit = 2 * time.Minute

func TestRunWithoutAccountExitsWithUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-data", t.TempDir()}, &stdout, &stderr, refuseLegacy)
	if code != 2 {
		t.Errorf("没给 -account 的退出码是 %d，要 2", code)
	}
	got := strings.TrimSpace(stderr.String())
	if got == "" {
		t.Fatal("没给 -account 时 stderr 里没有用法")
	}
	if strings.Contains(got, "\n") {
		t.Errorf("用法要一行，实际 %q", got)
	}
	if !strings.Contains(got, "-account") {
		t.Errorf("用法里该说要给 -account，实际 %q", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("参数不对时 stdout 该是空的，实际 %q", stdout.String())
	}
}

// -local-only 和 -account 一起给是用法错误；只给 -local-only 不是。
func TestRunLocalOnlyFlags(t *testing.T) {
	t.Run("和账号一起给", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-local-only", "-account", testAccount, "-data", t.TempDir()}, &stdout, &stderr, refuseLegacy)
		if code != 2 {
			t.Fatalf("退出码 %d，要 2；stderr：%s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "只开本地") {
			t.Fatalf("stderr 该说不能和 -account 一起给：%s", stderr.String())
		}
	})
	t.Run("和 media-check 一起给", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-local-only", "-mode", "media-check", "-data", t.TempDir()}, &stdout, &stderr, refuseLegacy)
		if code != 2 {
			t.Fatalf("退出码 %d，要 2；stderr：%s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "media-check") && !strings.Contains(stderr.String(), "媒体核对") {
			t.Fatalf("stderr 该说媒体核对不能用 -local-only：%s", stderr.String())
		}
	})
	t.Run("和 archive 一起给", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-local-only", "-mode", "archive", "-data", t.TempDir()}, &stdout, &stderr, refuseLegacy)
		if code != 2 {
			t.Fatalf("退出码 %d，要 2；stderr：%s", code, stderr.String())
		}
	})
	t.Run("只给 local-only 不是用法错误", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // 参数过了以后 Open 会看到取消，这里是退出码 1，不是 2
		var stdout, stderr bytes.Buffer
		code := run(ctx, []string{"-local-only", "-data", t.TempDir()}, &stdout, &stderr, refuseLegacy)
		if code == 2 {
			t.Fatalf("只给 -local-only 被当成用法错误：%s", stderr.String())
		}
		if strings.Contains(stderr.String(), "只开本地只能") {
			t.Fatalf("不该报参数错：%s", stderr.String())
		}
	})
}

// 真的初始化了数据目录时，日志里要有一行“新建了数据目录 <路径>（写了身份证）”。
func TestRunLogsDataDirInit(t *testing.T) {
	data := filepath.Join(t.TempDir(), "新建")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 只验证初始化那一段：参数过了以后 Open 看到取消，这里退出码 1
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"-local-only", "-data", data}, &stdout, &stderr, refuseLegacy)
	if code == 2 {
		t.Fatalf("初始化日志测试被当成用法错误：%s", stderr.String())
	}
	logFile := filepath.Join(data, "logs", "go-"+time.Now().Format(filelog.DayLayout)+".log")
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("读日志：%v（stderr：%s）", err, stderr.String())
	}
	if !strings.Contains(string(b), "新建了数据目录") {
		t.Fatalf("日志里没有初始化那一行：%s", b)
	}
	if _, err := os.Stat(filepath.Join(data, paths.MetaFile)); err != nil {
		t.Fatalf("数据目录该初始化：%v", err)
	}
}

// 全新目录用 -local-only：初始化后从资源根复制示例卡，后台不会因为“一个角色都没有”停下。
func TestRunLocalOnlyFreshDirCopiesExampleCard(t *testing.T) {
	data := filepath.Join(t.TempDir(), "全新")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 只验证初始化那一段
	var stdout, stderr bytes.Buffer
	run(ctx, []string{"-local-only", "-data", data}, &stdout, &stderr, refuseLegacy)
	card := filepath.Join(data, "characters", "example", "card", "character.yml")
	b, err := os.ReadFile(card)
	if err != nil {
		t.Fatalf("全新目录该复制示例卡到 %s：%v（stderr：%s）", card, err, stderr.String())
	}
	if !strings.Contains(string(b), "name:") {
		t.Fatalf("示例卡内容不对：%q", b)
	}
}

func refuseLegacy(context.Context, string, string) (net.Conn, error) {
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
}

func TestRunStopsCleanlyOnCancel(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	done := make(chan int, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- run(ctx, []string{"-root", e.root, "-account", testAccount, "-data", e.data}, &stdout, &stderr, refuseLegacy)
	}()
	// 中途红了也要等 run 退出：它开着临时目录里的日志和锁文件，不等就删不掉，报错盖住真正的原因
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(time.Minute):
			t.Error("一分钟内 run 没退出")
		}
	})

	// 等适配器真的连上假协议端，再当 Ctrl+C 按下去
	e.peer.accept(t)
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("取消 ctx 以后退出码是 %d，要 0；stdout：\n%s\nstderr：\n%s", code, stdout.String(), stderr.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("60 秒内 run 没返回")
	}

	logFile := filepath.Join(e.data, "logs", "go-"+time.Now().Format(filelog.DayLayout)+".log")
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("读日志文件 %s：%v", logFile, err)
	}
	logs := string(b)
	wants := []string{
		// 启动那一行：号打码、数据目录、配置文件；令牌下面单独查
		// 启动行里的用户目录和长数字（临时目录名里有）也被打码，预期照同一个函数算
		backend.RedactLog("要连的号 ***00，数据目录 "+e.data+"，配置文件 "+filepath.Join(e.data, "config", "sumika.yml"), nil),
		"关完了", // App 走完关机顺序
	}
	for _, want := range wants {
		if !strings.Contains(logs, want) {
			t.Errorf("日志文件里没有 %q：\n%s", want, logs)
		}
	}
	if strings.Contains(logs, testToken) {
		t.Errorf("日志文件里出现了令牌：\n%s", logs)
	}
	if !strings.Contains(stdout.String(), "关完了") {
		t.Errorf("命令行窗口里没有同样的日志：\n%s", stdout.String())
	}
}

// env 是一套假环境：项目根照 FindRoot 往上找，数据目录用临时目录，配置里指向假协议端和假网关
type env struct {
	t    *testing.T
	root string
	data string
	peer *fakePeer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root, err := config.FindRoot(".")
	if err != nil {
		t.Fatalf("找项目根（在 native/cmd/sumika-go 里跑）：%v", err)
	}
	e := &env{t: t, root: root, data: t.TempDir()}
	if _, err := paths.Init(e.data); err != nil {
		t.Fatalf("初始化临时数据目录：%v", err)
	}
	copyCard(t, root, e.data)

	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}
	var mu sync.Mutex
	var bodies []string
	llmSrv := httptest.NewServer(llmtest.Handler(llmtest.Script{Models: models, Reply: llmtest.Text("嗯，在的。")}, &bodies, &mu))
	t.Cleanup(llmSrv.Close)
	e.peer = newFakePeer(t)
	// 数据目录由 -data 显式给：不让环境变量盖过它
	t.Setenv("SUMIKA_DATA_DIR", "")
	t.Setenv("SUMIKA_CONFIG", "")
	e.writeConfig(llmSrv.URL)
	return e
}

// copyCard 把仓库里那张示例卡拷进临时数据目录（characters/<id>/card/）
func copyCard(t *testing.T, root, data string) {
	t.Helper()
	src := filepath.Join(root, "characters", testChar)
	dst := filepath.Join(data, "characters", testChar, "card")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("建卡的目录：%v", err)
	}
	for _, name := range []string{"character.yml", "character.md"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("读示例卡 %s：%v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			t.Fatalf("写临时卡 %s：%v", name, err)
		}
	}
}

// writeConfig 写临时数据目录里的配置（频率默认 0.3、不抖；令牌写在配置里，不碰密钥文件）
func (e *env) writeConfig(magpieURL string) {
	e.t.Helper()
	cfg := fmt.Sprintf(`# 测试用的配置（号、令牌、模型都是编的）
magpie:
  baseUrl: %s
qq:
  owner: %s
  accounts:
    - id: %s
      character: %s
      url: "%s"
      token: "%s"
  groups:
    - %s
group:
  frequency: 0.3
  jitter: 0
  mode: dry
characters:
  %s:
    model: %s
agent:
  recentMessages: 5
`, magpieURL, testOwner, testAccount, testChar, e.peer.url(), testToken, testGroup, testChar, testModel)
	dir := filepath.Join(e.data, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatalf("建 config 目录：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sumika.yml"), []byte(cfg), 0o644); err != nil {
		e.t.Fatalf("写配置：%v", err)
	}
}

// fakePeer 是假协议端：OneBot 正向 WS 的服务端，校验 Authorization，记下连上来的连接
type fakePeer struct {
	srv      *httptest.Server
	accepted chan struct{}
	wg       sync.WaitGroup

	mu     sync.Mutex
	conns  []*websocket.Conn
	closed bool
}

func newFakePeer(t *testing.T) *fakePeer {
	t.Helper()
	p := &fakePeer{accepted: make(chan struct{}, 4)}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("协议端握手的 Authorization 不是预期的编的令牌")
			http.Error(w, "访问令牌不对", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		p.wg.Add(1)
		defer p.wg.Done()
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			c.CloseNow()
			return
		}
		p.conns = append(p.conns, c)
		p.mu.Unlock()
		select {
		case p.accepted <- struct{}{}:
		default:
		}
		for {
			if _, _, err := c.Read(context.Background()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(func() {
		p.srv.Close()
		p.mu.Lock()
		p.closed = true
		conns := append([]*websocket.Conn(nil), p.conns...)
		p.mu.Unlock()
		for _, c := range conns {
			c.CloseNow()
		}
		p.wg.Wait()
	})
	return p
}

func (p *fakePeer) url() string { return "ws" + strings.TrimPrefix(p.srv.URL, "http") }

// accept 等适配器连上来
func (p *fakePeer) accept(t *testing.T) {
	t.Helper()
	select {
	case <-p.accepted:
	case <-time.After(startLimit):
		t.Fatalf("%v 内没收到适配器的连接", startLimit)
	}
}

// 不给 -root 时照旧从当前目录往上找项目根，不取 exe 所在目录（go run 的 exe 在临时编译目录里）。
func TestCLIRootFindsProjectFromCwd(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sumika.example.yml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "native", "cmd")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	got, err := cliRoot("")
	if err != nil {
		t.Fatal(err)
	}
	a, errA := os.Stat(got)
	b, errB := os.Stat(root)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Fatalf("资源根是 %s，要项目根 %s", got, root)
	}
}

// 只看历史指向不存在的目录：退出码 1，而且不能把目录（连同 logs/）建出来。
func TestArchiveMissingDataDirLeavesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "没有的数据")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-mode", "archive", "-root", t.TempDir(), "-data", dir}, &stdout, &stderr, refuseLegacy)
	if code != 1 {
		t.Errorf("退出码 %d，要 1；stderr：%s", code, stderr.String())
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("只看历史把不存在的数据目录建出来了：%v", err)
	}
}

// 启动失败的原文会带上配置里的东西（这里是角色名里夹着令牌和长号码）：写进日志文件和命令行窗口前必须打码（#20）。
func TestRunStartupFailureLogIsRedacted(t *testing.T) {
	e := newEnv(t)
	const longNumber = "98765432101234"
	badChar := testToken + "-" + longNumber
	cfgFile := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg := strings.Replace(string(b), "character: "+testChar, "character: "+badChar, 1)
	if cfg == string(b) {
		t.Fatal("没改到配置里的角色名")
	}
	if err := os.WriteFile(cfgFile, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-root", e.root, "-account", testAccount, "-data", e.data}, &stdout, &stderr, refuseLegacy)
	if code == 0 {
		t.Fatalf("坏角色名应该启动失败；stdout：\n%s", stdout.String())
	}
	logFile := filepath.Join(e.data, "logs", "go-"+time.Now().Format(filelog.DayLayout)+".log")
	raw, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("读日志文件：%v", err)
	}
	if !strings.Contains(string(raw), "启动失败：") {
		t.Fatalf("日志里没有启动失败那一行：\n%s", raw)
	}
	for name, text := range map[string]string{"日志文件": string(raw), "命令行窗口": stdout.String()} {
		if strings.Contains(text, testToken) || strings.Contains(text, longNumber) {
			t.Errorf("%s里出现了令牌或完整长号码：\n%s", name, text)
		}
	}
}

// 规格-日志分级 4.2：slog 和标准库 log 的行（misc）从入口接进日志文件，令牌和长号码要打码；
// run 返回以后 slog 和 log 的默认值还原，再写不能进已经关掉的出口（输出缓冲的字节数不变）。
// 不能并行：slog.SetDefault、log.SetOutput 是进程全局的。
func TestRunRoutesMiscLogsRedactedAndRestores(t *testing.T) {
	e := newEnv(t)
	const longNumber = "98765432101234"
	b, err := os.ReadFile(filepath.Join(e.data, "config", "sumika.yml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := strings.Replace(string(b), "character: "+testChar, "character: 坏角色名", 1) // 让 Open 马上失败，不用起整个后台
	if err := os.WriteFile(filepath.Join(e.data, "config", "sumika.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	oldSlog, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	logsRouted = func() {
		slog.Warn("检查点快照失败 "+testToken, "cwd", `C:\Users\Someone\`+longNumber)
		log.Printf("标准库来的行 %s", testToken)
	}
	t.Cleanup(func() { logsRouted = func() {} })
	var stdout, stderr bytes.Buffer
	run(context.Background(), []string{"-root", e.root, "-account", testAccount, "-data", e.data}, &stdout, &stderr, refuseLegacy)
	if slog.Default() != oldSlog || log.Writer() != oldWriter || log.Flags() != oldFlags {
		t.Fatalf("run 返回后 slog 或 log 的默认值没还原")
	}
	before := stdout.Len()
	slog.Warn("run 之后的行 " + testToken)
	log.Printf("run 之后的标准库行")
	if stdout.Len() != before {
		t.Errorf("run 返回后 slog 或 log 还在写进已关掉的出口：多了 %d 字节", stdout.Len()-before)
	}
	raw, err := os.ReadFile(filepath.Join(e.data, "logs", "go-"+time.Now().Format(filelog.DayLayout)+".log"))
	if err != nil {
		t.Fatalf("读日志文件：%v", err)
	}
	got := string(raw)
	for _, want := range []string{"WARN misc 检查点快照失败 ***", "INFO misc 标准库来的行 ***"} {
		if !strings.Contains(got, want) {
			t.Errorf("日志文件里没有 %q：\n%s", want, got)
		}
	}
	for name, text := range map[string]string{"日志文件": got, "命令行窗口": stdout.String()} {
		if strings.Contains(text, testToken) || strings.Contains(text, longNumber) || strings.Contains(text, "Someone") {
			t.Errorf("%s里出现了令牌、完整长号码或用户名：\n%s", name, text)
		}
	}
	if strings.Contains(got, "run 之后") {
		t.Errorf("run 之后的行进了文件：\n%s", got)
	}
}
