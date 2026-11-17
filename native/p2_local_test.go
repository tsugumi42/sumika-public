package main

// P2-1 第 2 步：宿主从“本地可用（QQ 没开）”出发的启动、开始接收和自动接收检查。
// 号、模型、句子都是编的；数据目录只用 t.TempDir()，协议端只连本机立刻关掉的端口。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

const phaseLocalChar = "example"
const phaseLocalModel = "编的模型"

// phaseLocalClosedPort 拿一个已经关掉的本地端口：启动探测和协议端检查都连它，不碰 7720 和真的 QQ。
func phaseLocalClosedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// copyPhaseLocalExampleCard 把仓库里的示例卡放进数据目录（模拟旧安装已经有卡）
func copyPhaseLocalExampleCard(t *testing.T, root, data string) {
	t.Helper()
	dst := filepath.Join(data, "characters", phaseLocalChar, "card")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"character.yml", "character.md"} {
		b, err := os.ReadFile(filepath.Join(root, "characters", phaseLocalChar, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writePhaseLocalConfig 写一份没有 qq.accounts 的配置；accountURL 非空时加一个编的小号（给后半段模拟“配置里有了号”）。
func writePhaseLocalConfig(t *testing.T, data, gateway, accountURL, token string) {
	t.Helper()
	accounts := ""
	if accountURL != "" {
		accounts = fmt.Sprintf("  accounts:\n    - id: \"100\"\n      character: %s\n      url: %q\n      token: %q\n", phaseLocalChar, accountURL, token)
	}
	cfg := fmt.Sprintf(`magpie:
  baseUrl: %s
qq:
  owner: "200"
%s  groups:
    - "300"
  privateAllow: []
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
`, gateway, accounts, phaseLocalChar, phaseLocalModel)
	if err := os.MkdirAll(filepath.Join(data, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "config", "sumika.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// phaseLocalOptions 造一套零小号、可选初始化过的宿主环境；协议端和启动探测都连一个已经关掉的本地端口。
func phaseLocalOptions(t *testing.T, initialized bool) backend.Options {
	t.Helper()
	root, err := config.FindRoot(".")
	if err != nil {
		t.Fatalf("找项目根（在 native/ 里跑）：%v", err)
	}
	data := t.TempDir()
	var bodies []string
	var mu sync.Mutex
	models := []json.RawMessage{json.RawMessage(`{"id":"` + phaseLocalModel + `","context_length":32768}`)}
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Models: models, Reply: llmtest.Text("在的。")}, &bodies, &mu))
	t.Cleanup(srv.Close)
	writePhaseLocalConfig(t, data, srv.URL, "", "")
	if initialized {
		p, err := paths.Init(data)
		if err != nil {
			t.Fatal(err)
		}
		set, err := store.OpenGo(p, "local", "qq")
		if err != nil {
			t.Fatal(err)
		}
		set.Close()
		copyPhaseLocalExampleCard(t, root, data)
	}
	port := phaseLocalClosedPort(t)
	return backend.Options{
		Root: root, DataDir: data, Logf: t.Logf, LLM: llm.New(llm.Magpie(srv.URL)),
		ProbePorts: []int{port}, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		SharedAnalyzer: testAnalyzer(),
	}
}

// 零小号：全新目录和已初始化目录都停在 phaseLocal，本地私聊能写；全新目录复制了示例卡。
func TestHostZeroAccountsStartsLocal(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		name := "全新目录"
		if initialized {
			name = "已初始化目录"
		}
		t.Run(name, func(t *testing.T) {
			opt := phaseLocalOptions(t, initialized)
			var log stateLog
			h := newHost(opt, log.add)
			h.start()
			defer h.shutdown()
			s := log.last()
			if s.Phase != phaseLocal || s.Mode != backend.Receive || s.Reader == nil || s.Memory == nil {
				t.Fatalf("零小号应停在本地可用：%+v", s)
			}
			if s.Account != "" || s.Accounts != 0 || !strings.Contains(s.NoRecv, "配置里没有 QQ 小号") {
				t.Fatalf("没有小号要分开写清：%+v", s)
			}
			key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:" + phaseLocalChar}
			if err := h.desk.Reader.SendLocal(context.Background(), key, "在吗", nil); err != nil {
				t.Fatalf("本地私聊该能写：%v", err)
			}
			if got := strings.Contains(s.Reason, "新建了数据目录"); got != !initialized {
				t.Fatalf("只有这次新建目录才该在原因里说：initialized=%v reason=%q", initialized, s.Reason)
			}
			if !strings.HasPrefix(s.Reason, "本地可用（QQ 没开）") {
				t.Fatalf("本地可用的原因：%q", s.Reason)
			}
			card := filepath.Join(opt.DataDir, "characters", phaseLocalChar, "card", "character.yml")
			if _, err := os.Stat(card); err != nil {
				t.Fatalf("全新目录该复制示例卡到 %s：%v", card, err)
			}
		})
	}
}

// 一个小号、全新目录：先初始化（不靠只看历史初始化），再进只看历史；第二次启动结果一样。
func TestHostOneAccountFreshDirInitializesThenArchive(t *testing.T) {
	t.Parallel()
	opt := phaseLocalOptions(t, false)
	writePhaseLocalConfig(t, opt.DataDir, "http://127.0.0.1:1/v1", "", "")
	if err := os.MkdirAll(filepath.Join(opt.DataDir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "qq:\n  accounts:\n    - id: \"100\"\n      character: " + phaseLocalChar + "\n"
	if err := os.WriteFile(filepath.Join(opt.DataDir, "config", "sumika.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		var log stateLog
		h := newHost(opt, log.add)
		h.start()
		s := log.last()
		if s.Phase != phaseArchive || s.Mode != backend.Archive || s.Reader == nil {
			t.Fatalf("第 %d 次启动：一个小号该停在只看历史：%+v", i+1, s)
		}
		if _, err := os.Stat(filepath.Join(opt.DataDir, paths.MetaFile)); err != nil {
			t.Fatalf("第 %d 次启动该初始化身份证：%v", i+1, err)
		}
		if _, err := os.Stat(filepath.Join(opt.DataDir, "characters", phaseLocalChar, "card", "character.yml")); !os.IsNotExist(err) {
			t.Fatalf("第 %d 次启动：有小号的全新目录不该复制示例卡：%v", i+1, err)
		}
		if err := h.shutdown(); err != nil {
			t.Fatal(err)
		}
	}
}

// 从本地可用手动开始接收：配置里已有号时能进 receiving；失败回到 local（不是 blocked）。
func TestHostLocalManualReceive(t *testing.T) {
	t.Parallel()
	t.Run("成功进接收中", func(t *testing.T) {
		opt := phaseLocalOptions(t, true)
		var log stateLog
		h := newHost(opt, log.add)
		h.start()
		defer h.shutdown()
		if log.last().Phase != phaseLocal {
			t.Fatalf("%+v", log.last())
		}
		writePhaseLocalConfig(t, opt.DataDir, "http://127.0.0.1:1/v1", "ws://127.0.0.1:1/", "编的令牌")
		h.account, h.qqCount, h.noRecv = "100", 1, ""
		if r := h.startReceive(); r.Outcome != receiveStarted {
			t.Fatalf("从本地开始接收：%+v", r)
		}
		if s := log.last(); s.Phase != phaseReceiving || s.Mode != backend.Receive || s.Reader == nil {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("失败回本地", func(t *testing.T) {
		opt := phaseLocalOptions(t, true)
		var log stateLog
		h := newHost(opt, log.add)
		h.start()
		defer h.shutdown()
		writePhaseLocalConfig(t, opt.DataDir, "http://127.0.0.1:1/v1", "ws://127.0.0.1:1/", "编的令牌")
		// 把 URL 抹掉：完整接收开不了；只开本地不看 URL，还能开回来
		file := filepath.Join(opt.DataDir, "config", "sumika.yml")
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Replace(string(b), `url: "ws://127.0.0.1:1/"`, `url: ""`, 1)
		if text == string(b) || os.WriteFile(file, []byte(text), 0o644) != nil {
			t.Fatal("没改到 URL")
		}
		h.account, h.qqCount, h.noRecv = "100", 1, ""
		if r := h.startReceive(); r.Outcome != receiveOpenFailed || !r.Retryable() {
			t.Fatalf("完整接收开不了应回可重试的失败：%+v", r)
		}
		if s := log.last(); s.Phase != phaseLocal || s.Mode != backend.Receive || s.Reader == nil || !strings.Contains(s.Reason, "开始接收失败") {
			t.Fatalf("失败该回本地可用：%+v", s)
		}
		key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:" + phaseLocalChar}
		if err := h.desk.Reader.SendLocal(context.Background(), key, "还在吗", nil); err != nil {
			t.Fatalf("回本地后该还能写：%v", err)
		}
	})
}

// holdResponse 第一次写响应体之前等着：本地回合一直在跑，测“拒绝开始接收且不切断回合”
type holdResponse struct {
	http.ResponseWriter
	release <-chan struct{}
	once    sync.Once
}

func (w *holdResponse) Write(b []byte) (int, error) {
	w.once.Do(func() { <-w.release })
	return w.ResponseWriter.Write(b)
}
func (w *holdResponse) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

// 本地有回合在跑时手动开始接收：被拒，回合照常完成，阶段留在本地可用。
func TestHostLocalManualReceiveRejectsRunningTurn(t *testing.T) {
	t.Parallel()
	opt := phaseLocalOptions(t, true)
	release := make(chan struct{})
	inner := llmtest.Handler(llmtest.Script{
		Models: []json.RawMessage{json.RawMessage(`{"id":"` + phaseLocalModel + `","context_length":32768}`)},
		Reply:  llmtest.Text("慢慢说完了。"),
	}, new([]string), new(sync.Mutex))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "chat/completions") {
			inner.ServeHTTP(&holdResponse{ResponseWriter: w, release: release}, r)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	opt.LLM = llm.New(llm.Magpie(srv.URL))
	writePhaseLocalConfig(t, opt.DataDir, srv.URL, "", "")
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	defer h.shutdown()
	if log.last().Phase != phaseLocal {
		t.Fatalf("%+v", log.last())
	}
	writePhaseLocalConfig(t, opt.DataDir, srv.URL, "ws://127.0.0.1:1/", "编的令牌")
	h.account, h.qqCount, h.noRecv = "100", 1, ""
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:" + phaseLocalChar}
	sent := make(chan error, 1)
	go func() { sent <- h.desk.Reader.SendLocal(context.Background(), key, "在吗", nil) }()
	deadline := time.Now().Add(10 * time.Second)
	for h.runningTurns() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.runningTurns() == 0 {
		close(release)
		t.Fatal("本地回合没开始跑")
	}
	if r := h.startReceive(); r.Outcome != receiveLocalBusy {
		close(release)
		t.Fatalf("本地有回合时应拒绝：%+v", r)
	}
	if s := log.last(); s.Phase != phaseLocal || !strings.Contains(s.Reason, "本地有回合在跑") {
		close(release)
		t.Fatalf("拒绝时要写明原因、阶段不动：%+v", s)
	}
	close(release)
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("回合被打断了：%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("回合没结束")
	}
	if s := log.last(); s.Phase != phaseLocal {
		t.Fatalf("回合结束后阶段该还在本地可用：%+v", s)
	}
}

// 自动接收：协议端连不上时留在本地可用，不拆后台，本地回合不受影响。
func TestHostAutoReceiveStaysLocalWhenPeerDown(t *testing.T) {
	t.Parallel()
	old := autoReceiveDelays
	autoReceiveDelays = []time.Duration{time.Millisecond}
	t.Cleanup(func() { autoReceiveDelays = old })
	opt := phaseLocalOptions(t, true)
	sh, log := autoShell(t, opt)
	sh.host.start()
	defer sh.host.shutdown()
	if log.last().Phase != phaseLocal {
		t.Fatalf("%+v", log.last())
	}
	writePhaseLocalConfig(t, opt.DataDir, "http://127.0.0.1:1/v1", fmt.Sprintf("ws://127.0.0.1:%d/", opt.ProbePorts[0]), "编的令牌")
	sh.host.account, sh.host.qqCount, sh.host.noRecv = "100", 1, ""
	sh.autoRecv.Store(true)
	sh.maybeAutoReceive()
	if sawPhase(log, phaseSwitching) {
		t.Fatal("协议端连不上时不该拆本地后台")
	}
	s := log.last()
	if s.Phase != phaseLocal || !strings.Contains(s.Reason, "等条件齐了") {
		t.Fatalf("该留在本地可用并写原因：%+v", s)
	}
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:" + phaseLocalChar}
	if err := sh.host.desk.Reader.SendLocal(context.Background(), key, "在吗", nil); err != nil {
		t.Fatalf("检查没过以后本地也该还能写：%v", err)
	}
}

// 本地可用阶段停止接收：不拆后台、阶段不变，在跑的本地回合照常完成，之后本地照常能写。
func TestHostStopReceiveInLocalPhaseKeepsBackend(t *testing.T) {
	t.Parallel()
	opt := phaseLocalOptions(t, true)
	release := make(chan struct{})
	inner := llmtest.Handler(llmtest.Script{
		Models: []json.RawMessage{json.RawMessage(`{"id":"` + phaseLocalModel + `","context_length":32768}`)},
		Reply:  llmtest.Text("说完了。"),
	}, new([]string), new(sync.Mutex))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "chat/completions") {
			inner.ServeHTTP(&holdResponse{ResponseWriter: w, release: release}, r)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	opt.LLM = llm.New(llm.Magpie(srv.URL))
	writePhaseLocalConfig(t, opt.DataDir, srv.URL, "", "")
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	defer h.shutdown()
	if log.last().Phase != phaseLocal {
		t.Fatalf("%+v", log.last())
	}
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:" + phaseLocalChar}
	sent := make(chan error, 1)
	go func() { sent <- h.desk.Reader.SendLocal(context.Background(), key, "在吗", nil) }()
	deadline := time.Now().Add(10 * time.Second)
	for h.runningTurns() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.runningTurns() == 0 {
		close(release)
		t.Fatal("本地回合没开始跑")
	}
	if note := h.stopReceive(); note != localStopNote {
		close(release)
		t.Fatalf("本地可用时停止接收该回那一句：%q", note)
	}
	if s := log.last(); s.Phase != phaseLocal || !strings.Contains(s.Reason, "本地可用（QQ 没开）") {
		close(release)
		t.Fatalf("停止接收不该动本地后台：%+v", s)
	}
	if h.cur == nil || !h.localOnly {
		close(release)
		t.Fatal("停止接收不该拆掉只开本地的后台")
	}
	close(release)
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("回合被打断了：%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("回合没结束")
	}
	if err := h.desk.Reader.SendLocal(context.Background(), key, "还在吗", nil); err != nil {
		t.Fatalf("停止接收以后本地该还能写：%v", err)
	}
}

// 身份证写了、建库失败：宿主进 blocked，原因带上“初始化到一半、删掉身份证再来”。
func TestHostHalfInitializedBlocksWithHint(t *testing.T) {
	t.Parallel()
	root, err := config.FindRoot(".")
	if err != nil {
		t.Fatalf("找项目根（在 native/ 里跑）：%v", err)
	}
	data := filepath.Join(t.TempDir(), "shared", "新目录")
	writePhaseLocalConfig(t, data, "http://127.0.0.1:1/v1", "", "")
	opt := backend.Options{Root: root, DataDir: data, Logf: t.Logf, DialContext: refuseDial, SharedAnalyzer: testAnalyzer()}
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	defer h.shutdown()
	s := log.last()
	if s.Phase != phaseBlocked {
		t.Fatalf("建库失败该进 blocked：%+v", s)
	}
	for _, want := range []string{"数据目录初始化到一半", "确认里面没有要留的东西后，删掉 sumika-data.json 再打开"} {
		if !strings.Contains(s.Reason, want) {
			t.Fatalf("受阻原因里没有 %q：%q", want, s.Reason)
		}
	}
	if _, err := os.Stat(filepath.Join(data, paths.MetaFile)); err != nil {
		t.Fatalf("半初始化的身份证该留着：%v", err)
	}
}

// 进 blocked 时清掉“只开本地”的标记：后台已经不在了，不能凭旧标记回本地。
func TestHostBlockClearsLocalOnly(t *testing.T) {
	t.Parallel()
	opt := phaseLocalOptions(t, true)
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	defer h.shutdown()
	if !h.localOnly {
		t.Fatalf("夹具没进本地可用：%+v", log.last())
	}
	h.block("编的受阻原因")
	if h.localOnly {
		t.Fatal("block 以后 localOnly 该清掉")
	}
	if s := log.last(); s.Phase != phaseBlocked {
		t.Fatalf("%+v", s)
	}
}

// 显式给的号不在配置里：有意的收紧，不返回它，开始接收因此不可用。
func TestChooseReceiveAccountRejectsUnconfiguredExplicitAccount(t *testing.T) {
	t.Parallel()
	opt := phaseLocalOptions(t, true)
	writePhaseLocalConfig(t, opt.DataDir, "http://127.0.0.1:1/v1", "ws://127.0.0.1:1/", "编的令牌")
	opt.Account = "999"
	c := chooseReceiveAccount(opt)
	if c.account != "" || c.count != 1 || !strings.Contains(c.reason, "配置里没有 ***99 这个小号") {
		t.Fatalf("显式给的号不在配置里该让开始接收不可用：%+v", c)
	}
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	defer h.shutdown()
	if h.account != "" {
		t.Fatalf("宿主不该拿没配置的号去开始接收：%q", h.account)
	}
	if r := h.startReceive(); r.Outcome != receiveNoAccount {
		t.Fatalf("开始接收该被挡：%+v", r)
	}
	if s := log.last(); !strings.Contains(s.Reason, "配置里没有 ***99 这个小号") {
		t.Fatalf("原因要写清：%+v", s)
	}
}
