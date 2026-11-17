package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/audience"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/embed"
	"sumika-native/internal/embed/embedtest"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
)

// 本包的测试进程也能当假的 sumika-models.exe 起来（embedtest）：go-models 后端的装配测试用真的 embed.Local 起它
func TestMain(m *testing.M) {
	if embedtest.Wanted() {
		embedtest.Run()
		return
	}
	// 数据目录由 Options 显式给；不让环境变量掩盖配置和数据目录分离的问题（newEnv 以前每条测试 t.Setenv 一次，那样不能并行）
	os.Setenv("SUMIKA_DATA_DIR", "")
	os.Setenv("SUMIKA_CONFIG", "")
	code := m.Run()
	sharedAnalyzer.Close() // 全包共用的常驻 pwsh（见 fastsync_test.go）：没用过也能关
	os.Exit(code)
}

// goModelsCfg 是装配测试里 go-models 的 embed 键；空串的键不写进配置（测"没配"）
type goModelsCfg struct {
	model, root, exe, ort, rerank string
}

type goModelsRig struct {
	e                 *testEnv
	a                 *App
	cfg               goModelsCfg
	dieFile, failFile string
	conv              types.Conversation
	mode              func(types.Conversation) string
}

// openGoModels 装一套 receive 的后台：embed 走 go-models，起的是测试进程自己扮演的假 sumika-models（env 是失败形态的开关）。
// 默认全齐能真发（号、意图、锁都配好），所以发送被钳回试运行只可能是守卫（嵌入）这一条。edit 在写配置之前改 embed 键
func openGoModels(t *testing.T, env map[string]string, edit func(*goModelsCfg)) *goModelsRig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := goModelsCfg{model: "gte-multilingual-base", root: filepath.Join(dir, "models"), exe: exe, ort: filepath.Join(dir, "onnxruntime.dll"), rerank: "bge-x"}
	if err := os.MkdirAll(cfg.root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.ort, []byte("假的 dll，只给 os.Stat 看"), 0o644); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(&cfg)
	}
	// 登记过的模型要先有 onnx / tokenizer.json 才过 Local 的启动检查；假服务不读内容，空文件就够
	if err := embedtest.WriteModelFiles(embed.ModelFiles(cfg.root, cfg.model)...); err != nil {
		t.Fatal(err)
	}
	r := &goModelsRig{cfg: cfg, dieFile: filepath.Join(dir, "die"), failFile: filepath.Join(dir, "embed-fail")}
	t.Setenv(embedtest.EnvOn, "1")
	t.Setenv(embedtest.EnvEmbedModels, cfg.model)
	t.Setenv(embedtest.EnvRerankModels, "bge-x")
	t.Setenv(embedtest.EnvDieFile, r.dieFile)
	t.Setenv(embedtest.EnvEmbedFailFile, r.failFile)
	for k, v := range env {
		t.Setenv(k, v)
	}

	e := newEnv(t)
	e.goSend = true
	if cfg.rerank != "" {
		e.extraConfig = "memory:\n  rerank:\n    model: " + cfg.rerank + "\n    timeoutSeconds: 5\n"
	}
	e.writeConfig(testToken)
	keys := []string{"backend: go-models"}
	for _, kv := range [][2]string{{"model", cfg.model}, {"modelsRoot", cfg.root}, {"modelsExe", cfg.exe}, {"ortLibrary", cfg.ort}} {
		if kv[1] != "" {
			keys = append(keys, fmt.Sprintf("%s: %q", kv[0], kv[1]))
		}
	}
	guardFixtureKeys(t, e, true, strings.Join(keys, "\n  "))
	writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
	writeSenderRecord(t, e.data, testAccount)
	e.peer.onAction = (&sendPeer{}).handle

	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{
		Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `"}`)}, Reply: llmtest.Text(`{"leaked":"no"}`),
	}, &bodies, &mu))
	t.Cleanup(srv.Close)
	opt := e.options()
	opt.LLM = llm.New(llm.Magpie(srv.URL))
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	r.e, r.a = e, a
	r.conv = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	if err := a.set.TraceStore().SetGateMode(r.conv.String(), "speak", e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	outCfg := channel.OutletConfig{Stores: a.set, Roster: a.roster, Adapters: []types.Adapter{a.adapter}, Check: a.checkQQSend, OnPart: a.onQQPart, Logf: a.logf}
	r.mode = a.qqSendMode(channel.GateModes(a.set, channel.ModeDryRun, a.logf), &outCfg)
	startSend(t, e, a) // 协议端登录核验通过：之后 QQ 发送被钳回试运行就只可能是守卫这一条
	return r
}

func (r *goModelsRig) status() (embed.Status, string) { return r.a.embedding.Status() }

func (r *goModelsRig) hasLog(sub string) bool {
	r.e.logMu.Lock()
	defer r.e.logMu.Unlock()
	for _, l := range r.e.logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// guardAlerts 是现在开着的、键以 guard: 开头的提醒的键
func (r *goModelsRig) guardAlerts() []string {
	var keys []string
	for _, x := range r.a.alerts.Open() {
		if strings.HasPrefix(x.Key, "guard:") {
			keys = append(keys, x.Key)
		}
	}
	return keys
}

// GM7 验收 3：go-models 下嵌入每种起不来的样子，都走 Down / Off → 守卫 literal-pending → QQ 发送钳回试运行 → 只提醒一次（guard:embed，
// 第一行带原因）；没有"换成别的引擎顶上"——状态就停在那，不自动换模型。恢复那一格用真进程：服务退出码 4 退出再起来，
// 提醒清一次、守卫回 full、发送放开
func TestGoModelsDownChain(t *testing.T) {
	if testing.Short() {
		t.Skip("反复起真实子进程，放在每日整套；握手和错误分类有单测")
	}
	missingExe := filepath.Join(t.TempDir(), "没有的-sumika-models.exe")
	for _, tc := range []struct {
		name   string
		env    map[string]string
		edit   func(*goModelsCfg)
		status embed.Status
		want   string
	}{
		{"没配 modelsRoot：默认到数据目录的 models，那里没有模型文件", nil, func(c *goModelsCfg) { c.root = "" }, embed.StatusDown, "找不到模型文件"},
		{"modelsExe 找不到", nil, func(c *goModelsCfg) { c.exe = missingExe }, embed.StatusDown, "找不到可执行文件"},
		{"ortLibrary 配了但不在", nil, func(c *goModelsCfg) { c.ort = filepath.Join(filepath.Dir(missingExe), "没有.dll") }, embed.StatusDown, "找不到 onnxruntime 动态库"},
		{"服务用退出码 2 拒绝启动", map[string]string{embedtest.EnvStderr: "[sumika-models] 错误：dll 的哈希不在已知表里（编的）", embedtest.EnvExitCode: "2"}, nil, embed.StatusDown, "dll 的哈希不在已知表里"},
		{"服务里没有配置的模型 id", map[string]string{embedtest.EnvEmbedModels: "别的模型"}, nil, embed.StatusDown, "模型 id 不在服务里"},
		{"服务里的 kind 不是 embed", map[string]string{embedtest.EnvKind: "rerank"}, nil, embed.StatusDown, "kind"},
		{"预热嵌入被服务拒绝", map[string]string{embedtest.EnvEmbedStatus: "500"}, nil, embed.StatusDown, "预热嵌入被服务拒绝"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := openGoModels(t, tc.env, tc.edit)
			var why string
			waitFor(t, "嵌入变成 "+string(tc.status)+" 且原因里有 "+tc.want, func() bool {
				var s embed.Status
				s, why = r.status()
				return s == tc.status && strings.Contains(why, tc.want)
			})
			m := r.a.memories[testChar]
			if got := m.GuardLevel(); got != "literal-pending" || r.a.guardFull(testChar) {
				t.Fatalf("起不来时守卫该是 literal-pending：%q", got)
			}
			if got := r.mode(r.conv); got != channel.ModeDryRun {
				t.Fatalf("起不来时 QQ 发送该钳回试运行：%s", got)
			}
			waitFor(t, "发送钳制的原因是守卫没有 full 能力", func() bool { return r.hasLog("QQ 发送钳制保持（group）：守卫没有 full 能力") })
			// 只提醒一次，而且是 guard:embed，第一行带原因
			waitFor(t, "guard:embed 提醒", func() bool { _, ok := openAlert(r.a, "guard:embed"); return ok })
			if keys := r.guardAlerts(); !reflect.DeepEqual(keys, []string{"guard:embed"}) {
				t.Fatalf("只该有 guard:embed 一条提醒：%v", keys)
			}
			al, _ := openAlert(r.a, "guard:embed")
			first, _, _ := strings.Cut(why, "\n")
			if !strings.Contains(al.Body, "原因：") || !strings.Contains(al.Body, tc.want) || strings.Contains(al.Body, testToken) {
				t.Fatalf("提醒正文该带第一行原因（%q），不带令牌：%q", first, al.Body)
			}
			if tc.status == embed.StatusDown && strings.Contains(tc.name, "服务") && strings.Contains(tc.name, "退出码 2") {
				time.Sleep(1500 * time.Millisecond)
				if s, _ := r.status(); s != embed.StatusDown {
					t.Fatalf("退出码 2 不再重试，状态该一直是 down：%s", s)
				}
			}
		})
	}
	t.Run("恢复", func(t *testing.T) {
		r := openGoModels(t, nil, nil)
		sub := r.a.alerts.Subscribe()
		defer sub.Close()
		awaitGuardFull(t, r.a)
		if got := r.mode(r.conv); got != channel.ModeSpeak {
			t.Fatalf("全齐时 QQ 发送该放开：%s", got)
		}
		if err := os.WriteFile(r.dieFile, nil, 0o644); err != nil { // 假服务见到这个文件就用退出码 4 退出
			t.Fatal(err)
		}
		waitFor(t, "服务退出后状态变 down", func() bool { s, _ := r.status(); return s == embed.StatusDown })
		if r.a.guardFull(testChar) || r.mode(r.conv) != channel.ModeDryRun {
			t.Fatal("服务没了，守卫该降级、发送该钳回试运行")
		}
		awaitGuardFull(t, r.a) // 退避 1 秒后重启，句向量预热已经按模型名记住，不用等
		if got := r.mode(r.conv); got != channel.ModeSpeak {
			t.Fatalf("恢复以后 QQ 发送该放开：%s", got)
		}
		wantKinds(t, "guard:embed 该提醒一次、恢复清一次", routeChanges(t, r.a.alerts, sub, "guard:embed"), alert.Raised, alert.Cleared)
	})
	t.Run("运行中的故障（照常发，只提醒一次）", func(t *testing.T) {
		// 2.7 的第二条路径，钉住现状（ADR-186 第 4 条）：服务显示就绪，某次嵌入出错 → 走 embed-down，消息照常判照常发，提醒一次
		r := openGoModels(t, nil, nil)
		sub := r.a.alerts.Subscribe()
		defer sub.Close()
		awaitGuardFull(t, r.a)
		aud := audience.Audience{Channel: "qq", Conversation: "qq:group:" + testGroup, Scene: "group"}
		if err := os.WriteFile(r.failFile, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if refs, err := r.a.guardFor(testChar).CheckOutbound(kernelGuardContext(t, r.a), "他留着那几节电池", aud); err != nil || len(refs) != 0 {
				t.Fatalf("嵌入出错时消息该照常过（只比原文）：%v %v", refs, err)
			}
		}
		if s, _ := r.status(); s != embed.StatusReady || !r.a.guardFull(testChar) {
			t.Fatalf("服务没挂，状态该一直是就绪：%s", s)
		}
		if got := r.a.memories[testChar].GuardRoute(); got != "embed-down" {
			t.Fatalf("路线该是 embed-down：%s", got)
		}
		wantKinds(t, "退路只提醒一次", routeChanges(t, r.a.alerts, sub, guardRouteAlertKey(testChar)), alert.Raised)
		os.Remove(r.failFile)
		if _, err := r.a.guardFor(testChar).CheckOutbound(kernelGuardContext(t, r.a), "他留着那几节电池", aud); err != nil {
			t.Fatal(err)
		}
		wantKinds(t, "恢复只清一次", routeChanges(t, r.a.alerts, sub, guardRouteAlertKey(testChar)), alert.Cleared)
	})
}

// GM7 验收 3b：生产装配出的嵌入器（没注入假的）实现 embed.Typed；重排在就绪前回 ErrUnavailable，就绪后可用（走的是同一个服务的 /v1/rerank）
func TestAssembledLocalImplementsTypedAndRerank(t *testing.T) {
	// 假服务按"卡住文件"报 ready=false：文件在就一直没就绪，删掉才放行，"没就绪"和"就绪"两个场景都是确定的，不靠抢时间
	hold := filepath.Join(t.TempDir(), "not-ready")
	if err := os.WriteFile(hold, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := openGoModels(t, map[string]string{embedtest.EnvNotReadyFile: hold}, nil)
	if _, ok := r.a.embedding.(embed.Typed); !ok {
		t.Fatalf("生产装配的嵌入器 %T 没实现 embed.Typed", r.a.embedding)
	}
	rr := r.a.memoryReranker()
	if rr == nil {
		t.Fatal("配了 memory.rerank.model，该有重排")
	}
	if s, _ := r.status(); s == embed.StatusReady {
		t.Fatalf("卡住文件在，嵌入不该就绪：%s", s)
	}
	if _, _, err := rr.Rerank(context.Background(), "查询", []string{"甲", "乙"}); !errors.Is(err, embed.ErrUnavailable) {
		t.Fatalf("没就绪该回 ErrUnavailable：%v", err)
	}
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	awaitGuardFull(t, r.a)
	order, _, err := rr.Rerank(context.Background(), "查询", []string{"甲", "乙", "丙"})
	if err != nil || !reflect.DeepEqual(order, []int{2, 1, 0}) {
		t.Fatalf("就绪后该排得出（假服务把最后一条排第一）：%v %v", order, err)
	}
}

// 嵌入模型没和守卫门槛对过（不是 gte-multilingual-base）：嵌入就绪也只报 literal-pending、发送钳回试运行，
// 并且有一条提醒说清为什么（不是 guard:embed——嵌入没坏）；gte 没有这条提醒
func TestUncalibratedEmbeddingModelNotice(t *testing.T) {
	r := openGoModels(t, nil, func(c *goModelsCfg) { c.model = "qwen3-embedding-0.6b-local" })
	waitFor(t, "嵌入就绪", func() bool { s, _ := r.status(); return s == embed.StatusReady })
	waitFor(t, "没标定过的提醒", func() bool { _, ok := openAlert(r.a, "guard:uncalibrated"); return ok })
	time.Sleep(1500 * time.Millisecond) // 给句向量预热留出时间：它做完也不该变 full
	m := r.a.memories[testChar]
	if got := m.GuardLevel(); got != "literal-pending" || r.a.guardFull(testChar) {
		t.Fatalf("嵌入就绪但模型没标定过，该是 literal-pending：%q", got)
	}
	if got := r.mode(r.conv); got != channel.ModeDryRun {
		t.Fatalf("没标定过的模型不该放开 QQ 发送：%s", got)
	}
	if keys := r.guardAlerts(); !reflect.DeepEqual(keys, []string{"guard:uncalibrated"}) {
		t.Fatalf("只该有 guard:uncalibrated（嵌入没坏，不该有 guard:embed）：%v", keys)
	}
	al, _ := openAlert(r.a, "guard:uncalibrated")
	if !strings.Contains(al.Body, "qwen3-embedding-0.6b-local") || !strings.Contains(al.Body, "gte-multilingual-base") {
		t.Fatalf("提醒该说清是哪个模型、换成哪个：%q", al.Body)
	}

	g := openGoModels(t, nil, nil)
	awaitGuardFull(t, g.a)
	if keys := g.guardAlerts(); len(keys) != 0 {
		t.Fatalf("gte 不该有守卫提醒：%v", keys)
	}
}

// 装配把 sumika.yml 的 embed 一节变成 LocalConfig：modelsExe 空取资源根里的 sumika-models（只认这一处），
// 配里同时写着已删的旧键 python / serverDir 也不被采用；重排模型取 memory.rerank.model
func TestOpenWiresGoModelsConfig(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.extraConfig = "memory:\n  rerank:\n    model: 编的重排\n"
	e.writeConfig(testToken)
	guardFixtureKeys(t, e, true, fmt.Sprintf("backend: go-models\n  python: %q\n  serverDir: %q\n  model: gte-multilingual-base\n  modelsRoot: %q\n  ortLibrary: %q",
		filepath.Join(e.data, "旧-python"), filepath.Join(e.data, "旧-server"), filepath.Join(e.data, "models"), filepath.Join(e.data, "onnxruntime.dll")))
	var got embed.LocalConfig
	opt := e.options()
	opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
		got = cfg
		return &guardLocal{cfg: cfg}
	}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	got.OnStatus = nil
	want := embed.LocalConfig{
		Model: "gte-multilingual-base", Backend: "go-models", Exe: filepath.Join(e.root, embed.ModelsExeName()),
		ModelsRoot: filepath.Join(e.data, "models"), OrtLibrary: filepath.Join(e.data, "onnxruntime.dll"), RerankModel: "编的重排", MaxLineTokens: embed.GoModelsMaxLineTokens,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("装配出的配置不对：\n得到 %+v\n要   %+v", got, want)
	}
}
