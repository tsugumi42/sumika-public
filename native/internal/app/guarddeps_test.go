package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/embed"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/memory"
)

type guardLocal struct {
	mu                   sync.Mutex // 后台预热、状态回调和测试线程同时读写下面几项
	cfg                  embed.LocalConfig
	starts, stops, calls int
	status               embed.Status
	holdStarting         bool
}

func (e *guardLocal) Start() {
	e.mu.Lock()
	e.starts++
	e.mu.Unlock()
	e.set(embed.StatusStarting)
	if !e.holdStarting {
		e.set(embed.StatusReady)
	}
}
func (e *guardLocal) set(status embed.Status) {
	e.mu.Lock()
	e.status = status
	e.mu.Unlock()
	if e.cfg.OnStatus != nil {
		e.cfg.OnStatus(status)
	}
}
func (e *guardLocal) Stop() {
	e.mu.Lock()
	e.stops++
	e.mu.Unlock()
	e.set(embed.StatusOff)
}
func (e *guardLocal) Status() (embed.Status, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status, ""
}

func (e *guardLocal) MissingModel() *embed.MissingModel { return nil }

// counts 是 Start、Stop、Embed 各被调了几次
func (e *guardLocal) counts() (starts, stops, calls int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.starts, e.stops, e.calls
}
func (e *guardLocal) Model() string { return e.cfg.Model }
func (e *guardLocal) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

func startedOnceAndEmbedded(l *guardLocal) bool {
	starts, _, calls := l.counts()
	return starts == 1 && calls > 0
}

// awaitGuardFull 等守卫能力变成 full：嵌入就绪以后还要等后台把句向量预热完（W1a），不是 Open 返回就有
func awaitGuardFull(t *testing.T, a *App) {
	t.Helper()
	waitFor(t, "句向量预热完成、守卫能力 full", func() bool { return a.guardFull(testChar) })
}

func guardFixture(t *testing.T, e *testEnv, enabled bool) string {
	t.Helper()
	return guardFixtureKeys(t, e, enabled, "model: gte-multilingual-base")
}

// guardFixtureKeys 是 guardFixture 的底：embed 一节里 enabled 前面写哪些键由调用方给
func guardFixtureKeys(t *testing.T, e *testEnv, enabled bool, embedKeys string) string {
	t.Helper()
	file := filepath.Join(e.data, "config", "sumika.yml")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b) + "\ndefaults:\n  backgroundModel: " + testModel + "\n"
	if enabled {
		cfg = strings.Replace(cfg, "enabled: false", "enabled: true", 1)
	}
	cfg = strings.Replace(cfg, "enabled: ", embedKeys+"\n  enabled: ", 1)
	if err := os.WriteFile(file, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(e.data, "go", "characters", testChar, "memory.db")
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 故意的裁剪列：只放守卫依赖检查要读的几列，不改成 BaseSchema；不接触真实记忆库。
	for _, q := range []string{
		`CREATE TABLE facts (id TEXT PRIMARY KEY, kind TEXT, status TEXT, text TEXT, subject TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, temporal TEXT, valid_to INTEGER)`,
		`CREATE TABLE episodes (id TEXT PRIMARY KEY, text TEXT, scope TEXT, conversation TEXT, created_at INTEGER, learned_at INTEGER, status TEXT)`,
		`INSERT INTO facts VALUES ('100', 'fact', 'active', '主人把备用电池收在蓝柜底层', 'owner', 'owner', '', 1, 1, NULL, NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return file
}

func TestGuardDependenciesReceive(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	file := guardFixture(t, e, true)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{
		Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `"}`), json.RawMessage(`{"id":"编的角色后台"}`), json.RawMessage(`{"id":"编的新默认后台"}`)}, Reply: llmtest.Text(`{"leaked":"no"}`),
	}, &bodies, &mu))
	t.Cleanup(srv.Close)
	opt := e.options()
	opt.LLM = llm.New(llm.Magpie(srv.URL))
	var local *guardLocal
	opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
		local = &guardLocal{cfg: cfg}
		return local
	}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	awaitGuardFull(t, a)
	m := a.memories[testChar]
	if m == nil || m.GuardLevel() != "full" || local == nil || !startedOnceAndEmbedded(local) {
		t.Fatalf("receive 没有接齐依赖：记忆=%v，嵌入=%+v", m, local)
	}
	if local.cfg.Model != "gte-multilingual-base" || local.cfg.Backend != "go-models" || local.cfg.ModelsRoot != filepath.Join(e.data, "models") || local.cfg.Exe != filepath.Join(e.root, embed.ModelsExeName()) {
		t.Fatalf("本地嵌入没照配置：%+v", local.cfg)
	}
	// guardFor 在记忆外面包了一层路线提醒（guardroute.go）：核对包的是这个角色的记忆
	if rg, ok := a.guardFor(testChar).(*routedGuard); !a.guardFull(testChar) || !ok || rg.core != guardCore(m) || a.guardFor("没有记忆的角色") != nil {
		t.Fatal("发送装配没有按角色读取守卫能力")
	}
	if _, _, err := m.SummaryState(context.Background(), "qq:100:group:300"); !errors.Is(err, memory.ErrNotWritable) {
		t.Fatalf("接后台模型后记忆仍应只读：%v", err)
	}
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	v, err := r.ModelSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{testModel, "编的角色后台", "编的新默认后台"} {
		if model == "编的角色后台" {
			v = modelSave(t, r, v, config.ModelChange{Field: config.CharacterBackground, Character: testChar, String: new(model)})
		} else if model == "编的新默认后台" {
			v = modelSave(t, r, v, config.ModelChange{Field: config.BackgroundModel, String: new(model)})
			v = modelSave(t, r, v, config.ModelChange{Field: config.CharacterBackground, Character: testChar, String: new("")})
		}
		got, err := m.CheckOutbound(kernelGuardContext(t, a), "他留着那几节电池", memory.Audience{Channel: "qq", Scene: "group"})
		if err != nil || len(got) != 0 || m.GuardRoute() != "normal" {
			t.Fatalf("嵌入命中后应调配置的后台模型并按 no 放行：%v %v %s", got, err, m.GuardRoute())
		}
		mu.Lock()
		body := bodies[len(bodies)-1]
		mu.Unlock()
		var request struct {
			Model string `json:"model"`
			Store *bool  `json:"store"`
		}
		if err := json.Unmarshal([]byte(body), &request); err != nil || request.Model != model {
			t.Fatalf("后台模型没现取配置：%+v，%v", request, err)
		}
		if request.Store == nil || *request.Store {
			t.Fatalf("守卫确认带私密记忆，必须显式 store=false：%s", body)
		}
	}
	local.status = embed.StatusDown
	local.cfg.OnStatus(local.status)
	if a.guardFull(testChar) {
		t.Fatal("嵌入退出后仍允许启用")
	}
	if m.GuardLevel() != "literal-pending" || len(a.alerts.Open()) != 1 {
		t.Fatal("嵌入退出后没有降级并提醒")
	}
	a.alerts.Raise(alert.Alert{Key: "guard:literal-pending", Title: "旧的字面检查提醒"})
	local.status = embed.StatusReady
	local.cfg.OnStatus(local.status)
	if m.GuardLevel() != "full" || len(a.alerts.Open()) != 0 {
		t.Fatal("嵌入恢复后没有恢复能力并清除提醒")
	}
	modelSave(t, r, v, config.ModelChange{Field: config.BackgroundModel, String: new("")})
	if m.GuardLevel() != "" {
		t.Fatal("没有选后台模型，不能报告 full")
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("守卫确认写了原记忆库：%v", err)
	}
	d.Close()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, stops, _ := local.counts(); stops != 1 {
		t.Fatalf("关机没有回收嵌入，Stop=%d", stops)
	}
}

func TestGuardEmbeddingUnavailable(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "配置关闭", true: "找不到本地程序"}[enabled], func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			guardFixture(t, e, enabled)
			opt := e.options()
			if !enabled {
				opt.NewEmbed = func(embed.LocalConfig, func(string, ...any)) LocalEmbedding {
					t.Fatal("配置关闭仍启动嵌入")
					return nil
				}
			}
			a, err := Open(context.Background(), opt)
			if err != nil {
				t.Fatalf("嵌入不可用不应挡住 receive：%v", err)
			}
			defer a.Close()
			if got := a.memories[testChar].GuardLevel(); got != "literal-pending" {
				t.Fatalf("退回能力=%q", got)
			}
			open := a.alerts.Open()
			want := map[bool]string{false: "guard:literal-pending", true: "guard:embed"}[enabled]
			if len(open) != 1 || open[0].Key != want {
				t.Fatalf("嵌入不可用只应提醒 %s：%+v", want, open)
			}
			if a.guardFull(testChar) || a.guardFull("没有记忆的角色") {
				t.Fatal("只有字面不能启用")
			}
			// W1 契约第 6 条：提醒正文带一句原因（找不到可执行文件 / 模型 id 不在服务里 / 依赖没装）
			if enabled && !strings.Contains(open[0].Body, "原因：找不到可执行文件") {
				t.Fatalf("提醒正文该带原因：%q", open[0].Body)
			}
			// 生产装配出的嵌入器（没注入假的时候是 *embed.Local）要实现 Status（编译期，LocalEmbedding）和 Typed：
			// 守卫用接口断言取 EmbedTyped，缺了就静默退成不带类型的 Embed
			if enabled {
				if _, ok := a.embedding.(embed.Typed); !ok {
					t.Fatalf("生产装配的嵌入器 %T 没实现 embed.Typed", a.embedding)
				}
			}
		})
	}
}

// 配置里还写着旧的 embed.python / embed.serverDir：读配置不失败，启动时只警告一次，两个键都点名
func TestWarnRemovedEmbedKeys(t *testing.T) {
	t.Parallel()
	var logs []string
	a := &App{logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }}
	a.cfg.RemovedKeys = []string{"embed.python", "embed.serverDir"}
	a.warnRemovedConfigKeys()
	if len(logs) != 1 || !strings.Contains(logs[0], "embed.python") || !strings.Contains(logs[0], "embed.serverDir") {
		t.Fatalf("两个旧键要在同一行里点名、只一行：%q", logs)
	}
	a.cfg.RemovedKeys = nil
	a.warnRemovedConfigKeys()
	if len(logs) != 1 {
		t.Fatalf("没有旧键不该写日志：%q", logs)
	}
}

func TestGuardEmbeddingArchive(t *testing.T) {
	t.Parallel()
	opt := archiveFixture(t, "守卫归档")
	if err := os.WriteFile(filepath.Join(opt.DataDir, "config", "sumika.yml"), []byte("embed:\n  enabled: true\n  model: gte-multilingual-base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
		calls++
		cfg.OnStatus = nil
		return &guardLocal{cfg: cfg}
	}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if calls != 0 || a.embedding != nil {
		t.Fatalf("archive 启动了嵌入，构造次数=%d", calls)
	}
}

func TestGuardEmbeddingTransitions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	guardFixture(t, e, true)
	opt := e.options()
	var local *guardLocal
	opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
		local = &guardLocal{cfg: cfg, holdStarting: true}
		return local
	}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if open := a.alerts.Open(); len(open) != 0 {
		t.Fatalf("正常加载不应提醒：%+v", open)
	}
	sub := a.alerts.Subscribe()
	defer sub.Close()
	for _, status := range []embed.Status{
		embed.StatusReady,
		embed.StatusDown, embed.StatusDown, embed.StatusStarting, embed.StatusDown,
		embed.StatusReady,
		embed.StatusOff, embed.StatusOff, embed.StatusReady,
	} {
		local.set(status)
	}
	// 同一个订阅里的标记排在所有状态之后，不靠等一会儿猜有没有重复通知。
	a.alerts.Raise(alert.Alert{Key: "test:barrier", Title: "状态已送完"})
	var kinds []alert.Kind
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
readChanges:
	for {
		select {
		case change := <-sub.C():
			if change.Alert.Key == "test:barrier" {
				break readChanges
			}
			if change.Alert.Key == "guard:embed" {
				kinds = append(kinds, change.Kind)
			}
		case <-deadline.C:
			t.Fatalf("状态通知没送完，已收到 %v", kinds)
		}
	}
	want := []alert.Kind{alert.Raised, alert.Cleared, alert.Raised, alert.Cleared}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("故障只提醒一次、恢复只清一次：得到 %v，要 %v", kinds, want)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	e.logMu.Lock()
	defer e.logMu.Unlock()
	count := 0
	for _, line := range e.logs {
		if strings.Contains(line, "发送前检查的嵌入尚不可用") {
			count++
		}
	}
	// 非连接提醒的恢复日志也沿用旧标题；两次故障、两次恢复共四条。
	if count != len(want) {
		t.Fatalf("故障和恢复应各有一条日志，得到 %d，要 %d", count, len(want))
	}
}

func TestGuardEmbeddingNotices(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, model, want string
		enabled           bool
	}{
		{"启动中到就绪", "gte-multilingual-base", "", true},
		{"配置关闭", "gte-multilingual-base", "guard:literal-pending", false},
		{"没配嵌入模型", "", "guard:literal-pending", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			guardFixture(t, e, tc.enabled)
			opt := e.options()
			var local *guardLocal
			opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
				cfg.Model = tc.model
				local = &guardLocal{cfg: cfg, holdStarting: true}
				return local
			}
			a, err := Open(context.Background(), opt)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { a.Close() })
			check := func() {
				t.Helper()
				open := a.alerts.Open()
				if tc.want == "" {
					if len(open) != 0 {
						t.Fatalf("正常加载和就绪都不应提醒：%+v", open)
					}
				} else if len(open) != 1 || open[0].Key != tc.want {
					t.Fatalf("只应有 %s，不应有两条相反的提醒：%+v", tc.want, open)
				}
			}
			check()
			if local != nil {
				if tc.model == "" {
					local.set(embed.StatusOff)
				} else {
					local.set(embed.StatusReady)
					awaitGuardFull(t, a) // 就绪以后后台补句向量，补完才是 full
				}
				check()
			}
		})
	}
}

// gatedEmbedding 的句向量（TypeDocument）要等 gate 关掉才算出来，其余（补 memory_vectors 的 Embed、守卫查询）立刻就有：
// 用来停在"嵌入就绪、句向量预热还没做完"那一段
type gatedEmbedding struct {
	*sendEmbedding
	gate chan struct{}
}

func (e *gatedEmbedding) EmbedTyped(ctx context.Context, kind embed.Type, texts []string) ([][]float32, error) {
	if kind == embed.TypeDocument {
		select {
		case <-e.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return e.Embed(ctx, texts)
}

// W1a 验收 2：嵌入就绪、句向量预热没做完 → 守卫是 literal-pending，qqSendMode 把 QQ 群发送钳在试运行；补完 → full，放开说话。
// 改坏检查：GuardLevel 不看预热，这条在第一个断言红
func TestGuardLevelNeedsWarmup(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	e, a, _ := sendAppWith(t, "全部齐全", func(cfg embed.LocalConfig) LocalEmbedding {
		return &gatedEmbedding{sendEmbedding: &sendEmbedding{&guardLocal{cfg: cfg}}, gate: gate}
	}, false)
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	if err := a.set.TraceStore().SetGateMode(conv.String(), "speak", e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	startSend(t, e, a) // 登录核验也通过了：别的条件都齐，只差预热
	if status, _ := a.embedding.Status(); status != embed.StatusReady {
		t.Fatalf("嵌入该是就绪的：%s", status)
	}
	if a.guardFull(testChar) || a.memories[testChar].GuardLevel() != "literal-pending" {
		t.Fatalf("句向量没补完，守卫不该是 full：%q", a.memories[testChar].GuardLevel())
	}
	if got := a.modeOf(conv); got != channel.ModeDryRun {
		t.Fatalf("预热期间发送该钳在试运行：%s", got)
	}
	close(gate)
	awaitGuardFull(t, a)
	waitFor(t, "补完以后放开说话", func() bool { return a.modeOf(conv) == channel.ModeSpeak })
}
