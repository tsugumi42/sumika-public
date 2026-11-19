package main

// 桌面程序日志文件的测试（desklog.go）：不起窗口，起外壳里管日志和宿主的那一层（shell.openLog、host），
// 用假网关（llmtest）和一个连不上的协议端地址跑一遍 只看历史 → 接收 → 退出，读 logs/desk-<日期>.log。
// 号、令牌、模型都是编的；数据目录只用 t.TempDir()，不碰真的 data/。

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	backend "sumika-native/internal/app"
	"sumika-native/internal/config"
	"sumika-native/internal/filelog"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
	"sumika-native/internal/testutil/leftover"
)

const (
	logTestAccount = "100"
	logTestModel   = "编的模型"
	logTestChar    = "example"
	// 令牌特意取成角色 id：后台打"角色 example 没有记忆库（……）"这一行时，令牌会被换成 ***。
	// 借此证明桌面的日志走的是后台的统一出口（app.newLogf），不是绕过它直接写文件。
	logTestToken = logTestChar
)

// logEnv 是一套假环境：数据目录里有身份证和示例卡，配置指向假网关和一个没人听的协议端地址
func logEnv(t *testing.T) backend.Options {
	t.Helper()
	root, err := config.FindRoot(".")
	if err != nil {
		t.Fatalf("找项目根（在 native/ 里跑）：%v", err)
	}
	data := leftover.Dir(t)
	p, err := paths.Init(data)
	if err != nil {
		t.Fatalf("初始化临时数据目录：%v", err)
	}
	set, err := store.OpenGo(p, "local", "qq") // 用真实迁移建 Go 库：只看历史不建库，要有现成的
	if err != nil {
		t.Fatal(err)
	}
	set.Close()
	card := filepath.Join(data, "characters", logTestChar, "card")
	if err := os.MkdirAll(card, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"character.yml", "character.md"} {
		b, err := os.ReadFile(filepath.Join(root, "characters", logTestChar, name))
		if err != nil {
			t.Fatalf("读示例卡 %s：%v", name, err)
		}
		if err := os.WriteFile(filepath.Join(card, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var bodies []string
	models := []json.RawMessage{json.RawMessage(`{"id":"` + logTestModel + `","context_length":32768}`)}
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Models: models, Reply: llmtest.Text("嗯，在的。")}, &bodies, &mu))
	t.Cleanup(srv.Close)
	cfg := fmt.Sprintf(`# 测试用的配置（号、令牌、模型都是编的）
magpie:
  baseUrl: %s
qq:
  owner: "200"
  accounts:
    - id: %s
      character: %s
      url: "ws://127.0.0.1:1"
      token: "%s"
  groups:
    - "300"
group:
  frequency: 0.3
  jitter: 0
  mode: dry
characters:
  %s:
    model: %s
embed:
  enabled: false
agent:
  recentMessages: 5
`, srv.URL, logTestAccount, logTestChar, logTestToken, logTestChar, logTestModel)
	if err := os.MkdirAll(filepath.Join(data, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "config", "sumika.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return backend.Options{Root: root, DataDir: data, DialContext: refuseDial, SharedAnalyzer: testAnalyzer()}
}

// deskLogLines 读 logs/ 下唯一的 desk-*.log
func deskLogLines(t *testing.T, data string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(data, "logs", deskLogPrefix+"-*.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("logs/ 下该有且只有一个 desk-*.log：%v %v", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// 桌面没有控制台：外壳开日志文件、交给宿主；只看历史、接收、退出各有一行，后台自己的行也在里面，令牌被打码
func TestDeskLogFileKeepsHostAndBackendLines(t *testing.T) {
	t.Parallel()
	opt := logEnv(t)
	sh := newShell(&app{}, opt, false)
	sh.host = newHost(sh.opt, func(hostState) {})
	sh.openLog()
	if sh.lg.Load() == nil {
		t.Fatal("外壳没开成日志文件")
	}
	sh.host.start()
	sh.host.startReceive()
	if sh.host.state.Phase != phaseReceiving {
		t.Fatalf("接收没开起来：%+v", sh.host.state)
	}
	if err := sh.host.shutdown(); err != nil {
		t.Fatal(err)
	}
	sh.logf("外壳自己的一行：%d", 7)
	sh.closeLog()
	sh.logf("关了以后的一行") // 只剩 log.Printf，不能再进文件

	lines := deskLogLines(t, opt.DataDir)
	all := strings.Join(lines, "\n")
	for _, want := range []string{
		// 启动行也过打码：用户目录和长数字（临时目录名里有）不留原文
		backend.RedactLog("桌面程序启动，数据目录 "+opt.DataDir+"，配置文件 "+filepath.Join(opt.DataDir, "config", "sumika.yml"), nil),
		"托盘可以开始接收的小号 ***00",
		"后台状态：正在检查后台",
		"后台状态：只看历史，模式 archive",
		"后台状态：正在切换：正在开始接收 QQ",
		"后台状态：接收中，模式 receive",
		"后台状态：正在退出",
		"后台状态：已停",
		"外壳自己的一行：7",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("日志文件里没有 %q：\n%s", want, all)
		}
	}
	if strings.Contains(all, "关了以后的一行") {
		t.Error("closeLog 以后的行还进了文件")
	}
	// 每一行都带本地时间
	for _, l := range lines {
		if _, err := time.ParseInLocation(filelog.StampLayout, l[:len(filelog.StampLayout)], time.Local); err != nil {
			t.Errorf("这一行开头不是本地时间：%q", l)
		}
	}
	// 后台自己的行经过 app.newLogf：令牌（这里和角色 id 同名）不会原样出现
	found := false
	for _, l := range lines {
		if strings.Contains(l, "没有记忆库") {
			found = true
			if !strings.Contains(l, "***") {
				t.Errorf("后台的这一行没被打码：%q", l)
			}
		}
	}
	if !found {
		t.Errorf("后台的日志没进文件（没有'没有记忆库'那一行；装配处改了这行话就同步改这里）：\n%s", all)
	}
	if strings.Contains(all, logTestToken) {
		t.Errorf("日志文件里出现了令牌：\n%s", all)
	}
}

// 有数据、只是没有身份证的目录：宿主不初始化它，状态写明原因，日志也不能建 logs/ 或身份证。
// （全新目录现在会在启动时初始化，这是 P2-1 的规则；这里守的是“看起来像数据目录的不许盖格式 1”。）
func TestDeskLogUninitializedLookalikeLeavesNothing(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "有数据没身份证")
	if err := os.MkdirAll(filepath.Join(dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	opt := backend.Options{Root: t.TempDir(), DataDir: dir, DialContext: refuseDial}
	sh := newShell(&app{}, opt, false)
	sh.host = newHost(sh.opt, func(hostState) {})
	sh.openLog()
	sh.host.start()
	if sh.host.state.Phase != phaseBlocked {
		t.Fatalf("有数据没身份证的目录该开不成：%+v", sh.host.state)
	}
	if err := sh.host.shutdown(); err != nil {
		t.Fatal(err)
	}
	sh.closeLog()
	if _, err := os.Stat(filepath.Join(dir, paths.MetaFile)); !os.IsNotExist(err) {
		t.Fatalf("不该给有数据的目录写身份证：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs")); !os.IsNotExist(err) {
		t.Fatalf("日志把不该初始化的目录建出了 logs/：%v", err)
	}
}

// 只开日志、不启动：数据目录不存在时原样不动，不建目录、身份证、logs/。
func TestDeskLogMissingDirLeavesNothing(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "不存在")
	opt := backend.Options{Root: t.TempDir(), DataDir: dir, DialContext: refuseDial}
	sh := newShell(&app{}, opt, false)
	sh.host = newHost(sh.opt, func(hostState) {})
	sh.openLog()
	if sh.lg.Load() == nil {
		t.Fatal("日志没开成")
	}
	sh.logf("只开日志、不启动：这一行该留在早期缓冲里")
	sh.closeLog()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("只开日志不该把数据目录建出来：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, paths.MetaFile)); !os.IsNotExist(err) {
		t.Fatalf("只开日志不该写身份证：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs")); !os.IsNotExist(err) {
		t.Fatalf("只开日志不该建 logs/：%v", err)
	}
}

// 演示数据不碰数据目录，也就不开日志
func TestDeskLogDemoOpensNothing(t *testing.T) {
	t.Parallel()
	opt := logEnv(t)
	sh := newShell(&app{}, opt, true)
	sh.openLog()
	if sh.lg.Load() != nil {
		t.Fatal("演示数据开了日志文件")
	}
	if _, err := os.Stat(filepath.Join(opt.DataDir, "logs")); !os.IsNotExist(err) {
		t.Fatalf("演示数据建了 logs/：%v", err)
	}
}
