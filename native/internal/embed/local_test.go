package embed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/embed/embedtest"
	"sumika-native/internal/llm"
	"sumika-native/internal/localmodels/engine"
)

// 编译期：Local 实现 Typed（守卫用接口断言取它）和重排入口
var _ Typed = (*Local)(nil)
var _ interface {
	Rerank(context.Context, string, []string) ([]int, []float64, error)
} = (*Local)(nil)

// waitFor 每 50 毫秒看一次条件，最多等 d
func waitFor(d time.Duration, cond func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

func readLines(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// 后端写了不认识的值：Start 不起进程，状态 down，原因说清只有 go-models（legacy 已移除）
func TestLocalUnknownBackend(t *testing.T) {
	l := NewLocal(LocalConfig{Model: "m", Backend: "go_models"}, nil)
	l.Start()
	if s, why := l.Status(); s != StatusDown || !strings.Contains(why, "go-models") || !strings.Contains(why, "legacy") {
		t.Fatalf("不认识的后端该 down 并说清：%s %q", s, why)
	}
}

// legacy 已删：旧配置读得进来，嵌入不启动，状态 down，原因说清改成 go-models、模型放哪
func TestLocalLegacyBackendRemoved(t *testing.T) {
	var logs []string
	l := NewLocal(LocalConfig{Model: "m", Backend: "legacy"}, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	l.Start()
	s, why := l.Status()
	if s != StatusDown || !strings.Contains(why, "legacy 已移除") || !strings.Contains(why, "go-models") || !strings.Contains(why, "modelsRoot") {
		t.Fatalf("legacy 该 down 并说清怎么改：%s %q", s, why)
	}
	if l.Wait(100 * time.Millisecond) {
		t.Fatal("legacy 不该等到就绪")
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "legacy 已移除") {
		t.Fatalf("旧配置该只记一行日志：%q", logs)
	}
}

// 登记过的模型缺文件：不起子进程，原因里写它在找的完整路径（自检和设置页用它告诉人放哪）
func TestLocalMissingModelFileShowsFullPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sumika-models.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "models")
	l := NewLocal(LocalConfig{Model: "gte-multilingual-base", Backend: BackendGoModels, Exe: exe, ModelsRoot: root}, nil)
	l.Start()
	s, why := l.Status()
	want := filepath.Join(root, "_待分类", "gte-multilingual-base", "model_quantized.onnx")
	if s != StatusDown || !strings.Contains(why, "找不到模型文件") || !strings.Contains(why, want) {
		t.Fatalf("该报缺模型和完整路径 %s：%s %q", want, s, why)
	}
	m := l.MissingModel()
	if m == nil || m.Model != "gte-multilingual-base" || m.Path != want {
		t.Fatalf("缺模型的结构化状态不对：%+v", m)
	}
	var registered string
	for _, spec := range engine.BuiltinSpecs() {
		if spec.ID == m.Model {
			registered = spec.DownloadURL
		}
	}
	if m.DownloadURL != registered || registered == "" {
		t.Fatalf("下载页该按登记表 %q：%+v", registered, m)
	}
	// 文件补齐以后再查一遍：结构化状态清空，文字原因也不再是缺模型。
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{want: "x", filepath.Join(filepath.Dir(want), "tokenizer.json"): "{}"} {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if why := l.checkFiles(); why != "" {
		t.Fatalf("文件补齐了还报：%q", why)
	}
	if m := l.MissingModel(); m != nil {
		t.Fatalf("文件补齐后还记着缺模型：%+v", m)
	}
}

// 验收 1：全新数据目录、不配 embed 的路径，模型根就是 <数据目录>/models；假 sumika-models 照这个根起来、能就绪
func TestEmbedStartsWithDefaultModelRoot(t *testing.T) {
	data := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args.jsonl")
	t.Setenv(embedtest.EnvOn, "1")
	t.Setenv(embedtest.EnvEmbedModels, "m")
	t.Setenv(embedtest.EnvArgsFile, argsFile)
	cfg := LocalConfigFrom(config.Embed{Enabled: true, Model: "m"}, "", "", data)
	cfg.Exe, _ = os.Executable()
	l := NewLocal(cfg, t.Logf)
	l.Start()
	t.Cleanup(l.Stop)
	if !l.Wait(60 * time.Second) {
		s, why := l.Status()
		t.Fatalf("没就绪：%s %s", s, why)
	}
	if l.cfg.ModelsRoot != filepath.Join(data, "models") {
		t.Fatalf("模型根该是数据目录的 models：%q", l.cfg.ModelsRoot)
	}
	var rec struct{ Args []string }
	if err := json.Unmarshal([]byte(readLines(t, argsFile)[0]), &rec); err != nil {
		t.Fatal(err)
	}
	rootArg := ""
	for i := 0; i+1 < len(rec.Args); i++ {
		if rec.Args[i] == "--models-root" {
			rootArg = rec.Args[i+1]
		}
	}
	if rootArg != filepath.Join(data, "models") {
		t.Fatalf("子进程没收到 <数据目录>/models：%q（%v）", rootArg, rec.Args)
	}
}

// builtinModelFiles 和 sumika-models 的登记表逐字段一致（embed 不能 import localmodels 的生产包，怕把 ORT 链进常驻程序）
func TestModelFilesMatchRegistry(t *testing.T) {
	specs := engine.BuiltinSpecs()
	if len(specs) != len(builtinModelFiles) {
		t.Fatalf("登记表 %d 条，embed 抄了 %d 条", len(specs), len(builtinModelFiles))
	}
	for _, s := range specs {
		f, ok := builtinModelFiles[s.ID]
		if !ok || f.dir != s.Dir || f.onnx != s.Onnx || f.page != s.DownloadURL {
			t.Fatalf("模型 %s：embed 记的是 %+v，登记表是 dir=%q onnx=%q page=%q", s.ID, f, s.Dir, s.Onnx, s.DownloadURL)
		}
	}
}

// 服务一直不监听端口：到启动时限就杀掉重起，原因里写超时
func TestLocalStartupTimeout(t *testing.T) {
	m := startModelsFake(t, map[string]string{embedtest.EnvHang: "1"})
	m.l.mu.Lock()
	m.l.startup = 1500 * time.Millisecond
	m.l.mu.Unlock()
	ok := waitFor(60*time.Second, func() bool {
		s, why := m.l.Status()
		return s == StatusDown && strings.Contains(why, "启动超时")
	})
	if !ok {
		s, why := m.l.Status()
		t.Fatalf("该写启动超时：%s %q", s, why)
	}
}

func TestTailBuffer(t *testing.T) {
	tb := newTailBuffer(3, 40)
	tb.Write([]byte("一\r\n二\n三\n四\n五"))
	if got := tb.String(); got != "三\n四\n五" { // 最后一行没换行：算半行，也留着，连半行一共 3 行
		t.Fatalf("行数上限 3：%q", got)
	}
	tb.Write([]byte("六\n"))
	if got := tb.String(); got != "三\n四\n五六" {
		t.Fatalf("半行续上以后：%q", got)
	}
	long := newTailBuffer(20, 10)
	long.Write([]byte(strings.Repeat("汉", 20))) // 60 字节，一直不换行：只留末尾 10 字节里完整的汉字
	if got := long.String(); got != "汉汉汉" || len(got) > 10 {
		t.Fatalf("字节上限 10，不切在汉字中间：%q", got)
	}
	long.stop()
	long.Write([]byte("停了以后的\n"))
	if strings.Contains(long.String(), "停了以后") {
		t.Fatalf("stop 以后不再留：%q", long.String())
	}
	if n, err := long.Write([]byte("x")); n != 1 || err != nil {
		t.Fatalf("Write 永远成功：%d %v", n, err)
	}
}

// Local 的 EmbedTyped、Rerank 在没起来的时候：类型照校验，没就绪回 ErrUnavailable；没点名重排模型回原因码
func TestLocalTypedAndRerankBeforeReady(t *testing.T) {
	l := NewLocal(LocalConfig{Model: "m"}, nil)
	ctx := context.Background()
	if _, err := l.EmbedTyped(ctx, TypeQuery, []string{"x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("没起来：%v", err)
	}
	if _, err := l.EmbedTyped(ctx, Type("别的"), []string{"x"}); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("类型不对该先报类型的错：%v", err)
	}
	if _, _, err := l.Rerank(ctx, "q", []string{"a"}); !errors.Is(err, ErrRerankNotConfigured) || err.Error() != "rerank-not-configured" {
		t.Fatalf("没点名重排模型该回带原因码的错：%v", err)
	}
}

// probe 的判定矩阵，不起子进程：什么算"服务答了不对"（wrong，杀了重起），什么算"还没好"（等下一次）
func TestProbeMatrix(t *testing.T) {
	type behave struct {
		health, models, embed int // 状态码，0 = 正常
		modelsBody            string
		embedBody             string
	}
	okModels := `{"object":"list","data":[{"id":"m","object":"model","owned_by":"local"}]}`
	okEmbed := `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,0]}],"model":"m"}`
	for _, tc := range []struct {
		name      string
		b         behave
		ok, wrong bool
		why       string
	}{
		{"全都正常（响应没有 kind 字段）", behave{}, true, false, ""},
		{"/health 401", behave{health: 401}, false, true, "401"},
		{"/health 503 还没好", behave{health: 503}, false, false, "/health 回了 503"},
		{"/v1/models 404", behave{models: 404}, false, true, "404"},
		{"id 不在", behave{modelsBody: `{"data":[{"id":"别的"}]}`}, false, true, "模型 id 不在服务里：m（服务里有：别的）"},
		{"一个 id 都没有", behave{modelsBody: `{"data":[]}`}, false, true, "模型 id 不在服务里：m"},
		{"kind 是 embed", behave{modelsBody: `{"data":[{"id":"m","kind":"embed"}]}`}, true, false, ""},
		{"kind 是 rerank", behave{modelsBody: `{"data":[{"id":"m","kind":"rerank"}]}`}, false, true, `kind 是 "rerank"`},
		{"kind 是空串（字段在，值不对）", behave{modelsBody: `{"data":[{"id":"m","kind":""}]}`}, false, true, "kind"},
		{"models 回的不是 JSON", behave{modelsBody: `<html>`}, false, true, "不是模型表"},
		{"预热 400", behave{embed: 400}, false, true, "HTTP 400"},
		{"预热 401", behave{embed: 401}, false, true, "401"},
		{"预热回空向量", behave{embedBody: `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[]}],"model":"m"}`}, false, true, "空的"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer tok" {
					http.Error(w, "x", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				reply := func(code int, body string) {
					if code != 0 {
						http.Error(w, `{"detail":"编的"}`, code)
						return
					}
					w.Write([]byte(body))
				}
				switch r.URL.Path {
				case "/health":
					reply(tc.b.health, `{"ok":true}`)
				case "/v1/models":
					reply(tc.b.models, firstNonEmpty(tc.b.modelsBody, okModels))
				case "/v1/embeddings":
					reply(tc.b.embed, firstNonEmpty(tc.b.embedBody, okEmbed))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			l := NewLocal(LocalConfig{Model: "m"}, nil)
			c := NewClient(llm.Source{Name: "假的", BaseURL: srv.URL + "/v1/", APIKey: "tok"}, "m")
			got := l.probe(context.Background(), srv.URL, "tok", c)
			if got.ok != tc.ok || got.wrong != tc.wrong || !strings.Contains(got.why, tc.why) {
				t.Fatalf("得到 %+v，要 ok=%v wrong=%v why 含 %q", got, tc.ok, tc.wrong, tc.why)
			}
		})
	}
	// 连不上（端口没人听）：还没好，不是 wrong
	l := NewLocal(LocalConfig{Model: "m"}, nil)
	if got := l.probe(context.Background(), "http://127.0.0.1:1", "tok", NewClient(llm.Source{BaseURL: "http://127.0.0.1:1/v1/"}, "m")); got.ok || got.wrong {
		t.Fatalf("连不上该是还没好：%+v", got)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// 进程杀不掉时 Stop 不能永远卡在等退出上：等 killGrace 以后直接 Process.Kill
// 改坏检查：terminateAndWait 去掉 killGrace 以后的 Process.Kill（只留 <-exited），这一条就卡死
func TestLocalStopKillsAfterGrace(t *testing.T) {
	oldGrace := killGrace
	t.Cleanup(func() { killGrace = oldGrace })
	killGrace = 300 * time.Millisecond

	m := startModelsFake(t, nil)
	m.waitReady(t)
	m.l.mu.Lock()
	cmd := m.l.cmd
	m.l.mu.Unlock()
	done := make(chan struct{})
	go func() { m.l.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("Stop 在 10 秒内没返回")
	}
	if cmd.ProcessState == nil {
		t.Fatal("Stop 返回了，子进程却还活着")
	}
}

// LocalConfigFrom：modelsRoot 空 = <数据目录>/models；三个路径相对时相对数据目录；modelsExe 空取资源根里的 sumika-models；
// 绝对路径照旧；后端空当 go-models；legacy 只把后端带过去、不拼路径
func TestLocalConfigFrom(t *testing.T) {
	data := filepath.Join(`D:\data`, "sumika")
	e := config.Embed{Model: "m", ModelsRoot: "models", ModelsExe: `D:\bin\me.exe`, OrtLibrary: "onnxruntime.dll"}
	if got := LocalConfigFrom(e, "rr", `D:\root`, data); !reflect.DeepEqual(got, LocalConfig{
		Model: "m", Backend: BackendGoModels, Exe: `D:\bin\me.exe`, ModelsRoot: filepath.Join(data, "models"),
		OrtLibrary: filepath.Join(data, "onnxruntime.dll"), RerankModel: "rr", MaxLineTokens: GoModelsMaxLineTokens,
	}) {
		t.Fatalf("相对路径没按数据目录解析：%+v", got)
	}
	e.ModelsRoot, e.ModelsExe, e.OrtLibrary = "", "", ""
	if got := LocalConfigFrom(e, "", `D:\root`, data); got.ModelsRoot != filepath.Join(data, "models") || got.Exe != filepath.Join(`D:\root`, ModelsExeName()) || got.OrtLibrary != "" {
		t.Fatalf("modelsRoot 空该用数据目录的 models，modelsExe 空取资源根的：%+v", got)
	}
	if got := LocalConfigFrom(e, "", "", data); got.Exe != "" {
		t.Fatalf("没有资源根就没有默认的 exe：%+v", got)
	}
	e.Backend = "legacy"
	if got := LocalConfigFrom(e, "rr", `D:\root`, data); got.Backend != "legacy" || got.Exe != "" || got.ModelsRoot != "" || got.RerankModel != "" {
		t.Fatalf("legacy 只把后端带过去：%+v", got)
	}
	e.Backend = ""
	if got := LocalConfigFrom(e, "rr", "", data); got.Backend != BackendGoModels || got.Exe != "" {
		t.Fatalf("后端空当 go-models：%+v", got)
	}
}

// 生产装配里 LocalConfigFrom 用的是 paths.ModelsDir 同一个位置：空了就 <数据目录>/models
func TestLocalConfigDefaultsToModelsDir(t *testing.T) {
	got := LocalConfigFrom(config.Embed{Model: "m"}, "", "", filepath.Join("data", "x"))
	if got.ModelsRoot != filepath.Join("data", "x", "models") {
		t.Fatalf("默认模型根：%q", got.ModelsRoot)
	}
}

// 测试程序当假 exe 起来的开关（embedtest）
func TestMain(m *testing.M) {
	if embedtest.Wanted() {
		embedtest.Run()
		return
	}
	os.Exit(m.Run())
}
