package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/embed/embedtest"
	"sumika-native/internal/localmodels/server"
)

// go-models 后端的测试（规格-Go本地模型 GM7）：测试程序扮演假的 sumika-models.exe（embedtest 包），Local 照真的那样起它。
// 各个失败形态的开关是 embedtest 里的环境变量。

type modelsFake struct {
	l                                     *Local
	root, ort                             string // 模型根目录、假的 onnxruntime.dll
	tokens, args, reqs, counter, stdinEOF string // 假服务写的记录文件
	mu                                    sync.Mutex
	statuses                              []Status
}

// startModelsFake 起一个假的 sumika-models（env 是失败形态的开关），edit 在 Start 之前改配置；测试结束自动停
func startModelsFake(t *testing.T, env map[string]string, edit ...func(*LocalConfig)) *modelsFake {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m := &modelsFake{
		root: filepath.Join(dir, "models"), ort: filepath.Join(dir, "onnxruntime.dll"),
		tokens: filepath.Join(dir, "tokens.txt"), args: filepath.Join(dir, "args.jsonl"), reqs: filepath.Join(dir, "requests.jsonl"),
		counter: filepath.Join(dir, "starts.txt"), stdinEOF: filepath.Join(dir, "stdin-eof.txt"),
	}
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.ort, []byte("假的 dll，只给 os.Stat 看"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(embedtest.EnvOn, "1")
	t.Setenv(embedtest.EnvEmbedModels, "m")
	t.Setenv(embedtest.EnvRerankModels, "bge-x")
	t.Setenv(embedtest.EnvTokenFile, m.tokens)
	t.Setenv(embedtest.EnvArgsFile, m.args)
	t.Setenv(embedtest.EnvRequestLog, m.reqs)
	t.Setenv(embedtest.EnvCounterFile, m.counter)
	t.Setenv(embedtest.EnvStdinEOFFile, m.stdinEOF)
	// 把 Python 专用的环境变量在测试进程里设成空串：go-models 不该往子进程里加它们，子进程看到的就还是空串（legacy 会设成真值）
	for _, k := range []string{"PYTHONIOENCODING", "SUMIKA_EMBED_SERVER_DIR", "SUMIKA_EMBED_PORT", "LOCAL_MODELS_ROOT"} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg := LocalConfig{
		Backend: BackendGoModels, Model: "m", Exe: exe, ModelsRoot: m.root, OrtLibrary: m.ort,
		RerankModel: "bge-x", MaxLineTokens: GoModelsMaxLineTokens,
		OnStatus: func(s Status) {
			m.mu.Lock()
			m.statuses = append(m.statuses, s)
			m.mu.Unlock()
		},
	}
	for _, e := range edit {
		e(&cfg)
	}
	m.l = NewLocal(cfg, t.Logf)
	m.l.Start()
	t.Cleanup(m.l.Stop)
	return m
}

func (m *modelsFake) seen() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Status(nil), m.statuses...)
}

// starts 是假服务被起了几次（它每次启动往计数文件追加一行）
func (m *modelsFake) starts() int {
	b, err := os.ReadFile(m.counter)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

func (m *modelsFake) base() string {
	m.l.mu.Lock()
	defer m.l.mu.Unlock()
	return strings.TrimSuffix(m.l.client.src.BaseURL, "/v1/")
}

func (m *modelsFake) waitReady(t *testing.T) {
	t.Helper()
	if !m.l.Wait(60 * time.Second) {
		s, why := m.l.Status()
		t.Fatalf("没就绪：%s %s", s, why)
	}
}

func (m *modelsFake) waitDown(t *testing.T, contains string) string {
	t.Helper()
	var why string
	if !waitFor(45*time.Second, func() bool {
		var s Status
		s, why = m.l.Status()
		return s == StatusDown && strings.Contains(why, contains)
	}) {
		s, why := m.l.Status()
		t.Fatalf("该是 down 且原因里有 %q：%s %q", contains, s, why)
	}
	return why
}

type argsRecord struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

func (m *modelsFake) argsRecords(t *testing.T) []argsRecord {
	t.Helper()
	var out []argsRecord
	for _, ln := range readLines(t, m.args) {
		var r argsRecord
		if err := json.Unmarshal([]byte(ln), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

type reqRecord struct {
	Path string                     `json:"path"`
	Auth string                     `json:"auth"`
	Body map[string]json.RawMessage `json:"body"`
}

func (m *modelsFake) requests(t *testing.T) []reqRecord {
	t.Helper()
	var out []reqRecord
	for _, ln := range readLines(t, m.reqs) {
		var r reqRecord
		if err := json.Unmarshal([]byte(ln), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// GM7 验收 1：命令行参数、环境变量、Authorization、input_type 的带法；令牌每次启动不同、不在命令行里；stdin 管道一直开着。
// 改坏检查：去掉 cmd.StdinPipe()（子进程的 stdin 成了 NUL，假服务一读到 EOF 就退，这条红）；EmbedTyped 不带 input_type（请求记录那行红）
func TestLocalGoModelsWire(t *testing.T) {
	if testing.Short() {
		t.Skip("反复起真实子进程，放在每日整套；握手和错误分类有单测")
	}
	m := startModelsFake(t, nil)
	m.waitReady(t)
	ctx := context.Background()
	if _, err := m.l.Embed(ctx, []string{"一句"}); err != nil {
		t.Fatalf("Embed：%v", err)
	}
	for _, kind := range []Type{TypeQuery, TypeDocument} {
		if _, err := m.l.EmbedTyped(ctx, kind, []string{"一句"}); err != nil {
			t.Fatalf("EmbedTyped(%s)：%v", kind, err)
		}
	}
	if order, scores, err := m.l.Rerank(ctx, "查询", []string{"甲", "乙", "丙"}); err != nil || !reflect.DeepEqual(order, []int{2, 1, 0}) || len(scores) != 3 {
		t.Fatalf("Rerank：%v %v %v", order, scores, err)
	}

	recs := m.argsRecords(t)
	if len(recs) != 1 {
		t.Fatalf("该只起了一次：%d", len(recs))
	}
	rec := recs[0]
	if len(rec.Args) < 2 || rec.Args[0] != "--port" {
		t.Fatalf("--port 该是第一个参数（显式传，不靠环境变量）：%q", rec.Args)
	}
	if port, err := strconv.Atoi(rec.Args[1]); err != nil || port < 1 || port > 65535 || !strings.HasSuffix(m.base(), ":"+rec.Args[1]) {
		t.Fatalf("--port 的值该是服务实际听的端口：%q，服务在 %s", rec.Args[1], m.base())
	}
	want := []string{"--port", rec.Args[1], "--models", "m", "--lazy-models", "bge-x", "--max-loaded", "2", "--idle-unload", "0", "--threads", "2",
		"--models-root", m.root, "--ort-library", m.ort, "--exit-on-stdin-eof", "--max-line-tokens", "2048"}
	if !reflect.DeepEqual(rec.Args, want) {
		t.Fatalf("命令行不对：\n得到 %q\n要   %q", rec.Args, want)
	}
	for k, v := range rec.Env {
		if v != "" {
			t.Fatalf("go-models 不该往子进程里加 Python 专用的环境变量（或端口、模型根的环境变量）：%s=%q", k, v)
		}
	}
	tokens := readLines(t, m.tokens)
	tok := tokens[0]
	if len(tok) < 16 {
		t.Fatalf("令牌该由环境变量传到子进程：%q", tok)
	}
	for _, a := range rec.Args {
		if strings.Contains(a, tok) {
			t.Fatalf("令牌不能在命令行里：%q", a)
		}
	}

	// 请求：预热 + Embed + 两种 EmbedTyped + 一次重排，全带本次启动的令牌；input_type 只有 Typed 的请求和预热带
	reqs := m.requests(t)
	if len(reqs) != 5 {
		t.Fatalf("请求记录该有 5 条（预热、Embed、两种 EmbedTyped、重排）：%d", len(reqs))
	}
	for i, r := range reqs {
		if r.Auth != "Bearer "+tok {
			t.Fatalf("第 %d 个请求没带这次启动的令牌：%q", i+1, r.Auth)
		}
	}
	inputType := func(r reqRecord) string {
		raw, has := r.Body["input_type"]
		if !has {
			return "(没带)"
		}
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	for i, wantType := range []string{"document", "(没带)", "query", "document"} {
		if reqs[i].Path != "/v1/embeddings" || inputType(reqs[i]) != wantType {
			t.Fatalf("第 %d 个请求（%s）的 input_type 该是 %s：%s", i+1, reqs[i].Path, wantType, inputType(reqs[i]))
		}
	}
	if reqs[4].Path != "/v1/rerank" || string(reqs[4].Body["model"]) != `"bge-x"` {
		t.Fatalf("重排请求该打到 /v1/rerank，模型是点名的 bge-x：%+v", reqs[4])
	}

	// stdin 管道一直开着：等一阵子，服务没读到 EOF，状态还是就绪
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(m.stdinEOF); err == nil {
		t.Fatal("子进程的 stdin 读到了 EOF：stdin 管道该开着不关")
	}
	if s, why := m.l.Status(); s != StatusReady {
		t.Fatalf("stdin 管道开着，服务该一直就绪：%s %s", s, why)
	}

	// 让服务用退出码 4（看门狗）退出再起一次：令牌换新的
	http.Get(m.base() + "/exit?code=4")
	if !waitFor(30*time.Second, func() bool { s, _ := m.l.Status(); return s != StatusReady }) {
		t.Fatal("子进程退出了，状态还是 ready")
	}
	m.waitReady(t)
	all := readLines(t, m.tokens)
	if len(all) != 2 || all[0] == all[1] {
		t.Fatalf("每次启动的令牌该不同：%q", all)
	}
}

// 没点名重排模型、没配 ortLibrary、不限行长时，对应的三个参数不传
func TestLocalGoModelsOptionalFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("反复起真实子进程，放在每日整套；握手和错误分类有单测")
	}
	m := startModelsFake(t, nil, func(c *LocalConfig) { c.RerankModel, c.OrtLibrary, c.MaxLineTokens = "", "", 0 })
	m.waitReady(t)
	args := strings.Join(m.argsRecords(t)[0].Args, " ")
	for _, f := range []string{"--lazy-models", "--ort-library", "--max-line-tokens"} {
		if strings.Contains(args, f) {
			t.Fatalf("没配的不该传 %s：%s", f, args)
		}
	}
	for _, f := range []string{"--models m", "--models-root", "--exit-on-stdin-eof"} {
		if !strings.Contains(args, f) {
			t.Fatalf("该传 %s：%s", f, args)
		}
	}
}

// W1 契约 4 在 go-models 下：服务里没有配置的模型 id、kind 不对、令牌被拒、预热被拒、ready=false，都不就绪，状态 Down，原因里有说法。
// id 不在、kind 不对是配置问题，换一次也一样：不再重试（启动次数停在 1）
func TestLocalGoModelsReadiness(t *testing.T) {
	if testing.Short() {
		t.Skip("反复起真实子进程，放在每日整套；握手和错误分类有单测")
	}
	for _, tc := range []struct {
		name    string
		env     map[string]string
		want    string
		noRetry bool
	}{
		{"模型 id 不在服务里", map[string]string{embedtest.EnvEmbedModels: "别的模型,又一个"}, "模型 id 不在服务里：m", true},
		{"服务里的 kind 是 rerank", map[string]string{embedtest.EnvKind: "rerank"}, `kind 是 "rerank"`, true},
		{"/health 拒绝令牌", map[string]string{embedtest.EnvHealthStatus: "401"}, "令牌", false},
		{"/v1/models 拒绝令牌", map[string]string{embedtest.EnvModelsStatus: "401"}, "令牌", false},
		{"/v1/models 回 500", map[string]string{embedtest.EnvModelsStatus: "500"}, "/v1/models 回了 500", false},
		{"预热嵌入失败", map[string]string{embedtest.EnvEmbedStatus: "500"}, "预热嵌入被服务拒绝", false},
		{"服务说模型 ready=false", map[string]string{embedtest.EnvNotReady: "1"}, "ready=false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := startModelsFake(t, tc.env)
			why := m.waitDown(t, tc.want)
			if _, err := m.l.Embed(context.Background(), []string{"x"}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("没就绪不该能算：%v", err)
			}
			if tc.noRetry {
				if !strings.Contains(why, "不再重试") {
					t.Fatalf("配置问题该说不再重试：%q", why)
				}
				time.Sleep(2500 * time.Millisecond) // 退避的第一次重启在 1 秒以后
				if n := m.starts(); n != 1 {
					t.Fatalf("配置问题不该重试，启动了 %d 次", n)
				}
				if m.l.Wait(5 * time.Second) {
					t.Fatal("放弃以后不该等到就绪")
				}
			} else if m.l.Wait(100 * time.Millisecond) {
				t.Fatal("没就绪不该等到就绪")
			}
		})
	}
}

// W1 契约 6 在 go-models 下：服务写完 stderr 就用退出码 2 退出，最后一行 [sumika-models] 错误： 必须在 lastErr 里（第一行就带它的内容），
// 只留最后 20 行、令牌抹掉；就绪以后写的 stderr 不进 lastErr。
// 改坏检查：把 cmd.Stderr = tail 改回 StderrPipe + 另起 goroutine 读、再另起 goroutine Wait，下面的循环里最后一行会偶发或必现丢掉
func TestLocalGoModelsStderrTail(t *testing.T) {
	t.Run("退出码 2，最后一行必须在", func(t *testing.T) {
		var lines []string
		for i := 1; i <= 30; i++ {
			lines = append(lines, "第"+strconv.Itoa(i)+"行")
		}
		lines = append(lines, "[sumika-models] 错误：编的最后一行 {token}")
		for round := 1; round <= 12; round++ { // 丢行是竞争条件：多起几次
			m := startModelsFake(t, map[string]string{embedtest.EnvStderr: strings.Join(lines, "\n"), embedtest.EnvExitCode: "2"})
			why := m.waitDown(t, "编的最后一行")
			if !strings.Contains(why, "第12行") || strings.Contains(why, "第11行") {
				t.Fatalf("第 %d 次：只该留最后 20 行（第 12 行起）：%q", round, why)
			}
			first, _, _ := strings.Cut(why, "\n")
			if !strings.Contains(first, "编的最后一行") || !strings.Contains(first, "不再重试") {
				t.Fatalf("第 %d 次：第一行（提醒只取这一行）该带错误行的内容和不再重试：%q", round, first)
			}
			if strings.Contains(why, "{token}") || len(why) > stderrTailBytes+600 {
				t.Fatalf("第 %d 次：令牌占位该已替换成真令牌又被抹掉，总长不该超过上限太多：%d %q", round, len(why), why)
			}
			tok := readLines(t, m.tokens)[0]
			if strings.Contains(why, tok) {
				t.Fatalf("第 %d 次：令牌没抹掉：%q", round, why)
			}
			m.l.Stop()
		}
	})
	t.Run("就绪以后的 stderr 不进 lastErr", func(t *testing.T) {
		m := startModelsFake(t, nil)
		m.waitReady(t)
		http.Get(m.base() + "/noise?text=运行期的输入文字")
		http.Get(m.base() + "/exit?code=4")
		if !waitFor(30*time.Second, func() bool { s, _ := m.l.Status(); return s == StatusDown }) {
			t.Fatal("子进程退出了，状态没变 down")
		}
		if _, why := m.l.Status(); strings.Contains(why, "运行期的输入文字") {
			t.Fatalf("就绪以后写的 stderr 不该进 lastErr：%q", why)
		}
	})
}

// 退出码（第三节"退出码"一行）：2 不再重试、状态 Down 带原因；3 换口马上重试（不走退避，所以状态里没有 Down），连着 5 次还不行才走退避；
// 4 和别的码退避重启
func TestLocalExitCodes(t *testing.T) {
	if testing.Short() {
		t.Skip("反复起真实子进程，放在每日整套；握手和错误分类有单测")
	}
	t.Run("退出码 2：不再重试，Wait 不傻等，Stop 能走", func(t *testing.T) {
		m := startModelsFake(t, map[string]string{embedtest.EnvStderr: "[sumika-models] 错误：编的配置不对", embedtest.EnvExitCode: "2"})
		why := m.waitDown(t, "编的配置不对")
		if !strings.Contains(why, "不再重试") {
			t.Fatalf("该说不再重试：%q", why)
		}
		t0 := time.Now()
		if m.l.Wait(30 * time.Second) {
			t.Fatal("放弃以后不该等到就绪")
		}
		if d := time.Since(t0); d > 3*time.Second {
			t.Fatalf("放弃以后 Wait 该马上返回，等了 %v", d)
		}
		time.Sleep(2500 * time.Millisecond) // 退避的第一次重启在 1 秒以后
		if n := m.starts(); n != 1 {
			t.Fatalf("退出码 2 不该重试，启动了 %d 次", n)
		}
		if got := m.seen(); !reflect.DeepEqual(got, []Status{StatusStarting, StatusDown}) {
			t.Fatalf("状态变化该是 starting → down 各一次：%v", got)
		}
		t0 = time.Now()
		m.l.Stop()
		if d := time.Since(t0); d > 3*time.Second {
			t.Fatalf("Stop 等了 %v", d)
		}
	})
	t.Run("退出码 3：换口马上重试，不走退避", func(t *testing.T) {
		m := startModelsFake(t, map[string]string{embedtest.EnvExit3Times: "2"})
		m.waitReady(t)
		if n := m.starts(); n != 3 {
			t.Fatalf("前两次端口被占、第三次成：启动该是 3 次，实际 %d", n)
		}
		for _, s := range m.seen() {
			if s == StatusDown {
				t.Fatalf("端口被占是换口马上重试，不该出现 down（退避才有）：%v", m.seen())
			}
		}
	})
	t.Run("退出码 3 连着被占：最多换 5 次，再走退避", func(t *testing.T) {
		m := startModelsFake(t, map[string]string{embedtest.EnvExit3Times: "100"})
		why := m.waitDown(t, "端口连着被占")
		if !strings.Contains(why, "秒后重启") {
			t.Fatalf("连着被占以后该走退避：%q", why)
		}
		if n := m.starts(); n != maxPortRetries+1 {
			t.Fatalf("该是 1 次加 %d 次换口重试共 %d 次，实际 %d", maxPortRetries, maxPortRetries+1, n)
		}
	})
	t.Run("退出码 4：退避重启", func(t *testing.T) {
		m := startModelsFake(t, nil)
		m.waitReady(t)
		http.Get(m.base() + "/exit?code=4")
		why := m.waitDown(t, "秒后重启")
		_ = why
		m.waitReady(t)
		if n := m.starts(); n != 2 {
			t.Fatalf("退出码 4 该重启一次：启动了 %d 次", n)
		}
	})
	// Go 运行时 panic、fatal error（包括 out of memory）、Windows 上的原生崩溃都以退出码 2 退出，
	// 但没有"[sumika-models] 错误："行：那不是配置问题，不能永久放弃（一次瞬时的内存吃紧就让嵌入再也起不来）
	t.Run("退出码 2 但没有错误行（panic、内存吃紧）：退避重启，不放弃", func(t *testing.T) {
		m := startModelsFake(t, map[string]string{embedtest.EnvStderr: "panic: assignment to entry in nil map\n\ngoroutine 1 [running]:", embedtest.EnvExitCode: "2"})
		why := m.waitDown(t, "秒后重启")
		if strings.Contains(why, "不再重试") || strings.Contains(why, "改好配置") || strings.Contains(why, "拒绝启动") {
			t.Fatalf("没有错误行的退出码 2 不是配置问题，不该说不再重试：%q", why)
		}
		if !strings.Contains(why, "在就绪前退出了") || !strings.Contains(why, "panic: assignment to entry in nil map") {
			t.Fatalf("该是普通的就绪前退出，并带 stderr 末尾：%q", why)
		}
		if !waitFor(10*time.Second, func() bool { return m.starts() >= 2 }) {
			t.Fatalf("退避以后该再起一次：启动了 %d 次", m.starts())
		}
		m.l.mu.Lock()
		gaveUp := m.l.gaveUp
		m.l.mu.Unlock()
		if gaveUp {
			t.Fatal("不该放弃")
		}
	})
	t.Run("别的退出码就绪前退出：退避重启，不放弃", func(t *testing.T) {
		m := startModelsFake(t, map[string]string{embedtest.EnvStderr: "普通的错", embedtest.EnvExitCode: "1"})
		why := m.waitDown(t, "秒后重启")
		if !strings.Contains(why, "在就绪前退出了") || strings.Contains(why, "不再重试") {
			t.Fatalf("退出码 1 该是普通的就绪前退出：%q", why)
		}
		if !waitFor(10*time.Second, func() bool { return m.starts() >= 2 }) {
			t.Fatalf("退避以后该再起一次：启动了 %d 次", m.starts())
		}
	})
}

// 启动前的检查：没配的键、文件不在、路径不是绝对路径，都在起进程之前就说清（状态 off / down，不起进程）
func TestLocalGoModelsStartChecks(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sumika-models.exe")
	os.WriteFile(exe, []byte("x"), 0o644)
	full := LocalConfig{Backend: BackendGoModels, Model: "m", Exe: exe, ModelsRoot: dir}
	for _, tc := range []struct {
		name   string
		edit   func(*LocalConfig)
		status Status
		want   string
	}{
		{"什么都没配", func(c *LocalConfig) { *c = LocalConfig{Backend: BackendGoModels} }, StatusOff, "没配嵌入服务（embed.modelsRoot、embed.model、embed.modelsExe）"},
		{"没配模型根", func(c *LocalConfig) { c.ModelsRoot = "" }, StatusOff, "没配嵌入服务（embed.modelsRoot）"},
		{"没配模型", func(c *LocalConfig) { c.Model = "" }, StatusOff, "没配嵌入服务（embed.model）"},
		{"没有 exe 的路径（评测工具没有资源根）", func(c *LocalConfig) { c.Exe = "" }, StatusOff, "没配嵌入服务（embed.modelsExe）"},
		{"exe 不存在", func(c *LocalConfig) { c.Exe = filepath.Join(dir, "没有.exe") }, StatusDown, "找不到可执行文件：sumika-models（"},
		{"exe 写了相对路径", func(c *LocalConfig) { c.Exe = "sumika-models.exe" }, StatusDown, "embed.modelsExe 要写绝对路径"},
		{"模型根写了相对路径", func(c *LocalConfig) { c.ModelsRoot = "models" }, StatusDown, "embed.modelsRoot 要写绝对路径"},
		{"dll 配了但不存在", func(c *LocalConfig) { c.OrtLibrary = filepath.Join(dir, "没有.dll") }, StatusDown, "找不到 onnxruntime 动态库"},
		{"dll 写了相对路径", func(c *LocalConfig) { c.OrtLibrary = "onnxruntime.dll" }, StatusDown, "embed.ortLibrary 要写绝对路径"},
		{"不认识的后端", func(c *LocalConfig) { c.Backend = "go_models" }, StatusDown, "不认识"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := full
			tc.edit(&cfg)
			l := NewLocal(cfg, nil)
			l.Start()
			defer l.Stop()
			s, why := l.Status()
			if s != tc.status || !strings.Contains(why, tc.want) {
				t.Fatalf("得到 %s %q，要 %s 且含 %q", s, why, tc.status, tc.want)
			}
			if l.Wait(100 * time.Millisecond) {
				t.Fatal("没起来不该等到就绪")
			}
		})
	}
}

// GM7 验收 3b：生产装配出的嵌入器实现 Typed；Rerank 在就绪前回 ErrUnavailable，就绪后可用；没点名重排模型回原因码；
// 重排模型的请求失败（文件坏了之类）不影响嵌入
func TestLocalImplementsTypedAndRerank(t *testing.T) {
	if testing.Short() {
		t.Skip("反复起真实子进程，放在每日整套；握手和错误分类有单测")
	}
	var _ Typed = (*Local)(nil)
	ctx := context.Background()
	t.Run("就绪前后", func(t *testing.T) {
		m := startModelsFake(t, nil)
		if _, _, err := m.l.Rerank(ctx, "q", []string{"a"}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("没就绪该回 ErrUnavailable：%v", err)
		}
		m.waitReady(t)
		order, scores, err := m.l.Rerank(ctx, "q", []string{"甲", "乙", "丙", "丁"})
		if err != nil || !reflect.DeepEqual(order, []int{3, 2, 1, 0}) || scores[0] != 1 {
			t.Fatalf("就绪后该排得出：%v %v %v", order, scores, err)
		}
		if _, err := m.l.EmbedTyped(ctx, Type("别的"), []string{"x"}); err == nil || errors.Is(err, ErrUnavailable) {
			t.Fatalf("类型不对该先报类型的错：%v", err)
		}
	})
	t.Run("没点名重排模型", func(t *testing.T) {
		m := startModelsFake(t, nil, func(c *LocalConfig) { c.RerankModel = "" })
		m.waitReady(t)
		if _, _, err := m.l.Rerank(ctx, "q", []string{"a"}); !errors.Is(err, ErrRerankNotConfigured) || err.Error() != "rerank-not-configured" {
			t.Fatalf("没点名该回带原因码的错：%v", err)
		}
	})
	t.Run("重排模型不在服务里，嵌入照常", func(t *testing.T) {
		m := startModelsFake(t, map[string]string{embedtest.EnvRerankModels: "别的重排"})
		m.waitReady(t)
		if _, _, err := m.l.Rerank(ctx, "q", []string{"a"}); err == nil || errors.Is(err, ErrUnavailable) {
			t.Fatalf("重排模型不可用该回服务的错：%v", err)
		}
		if _, err := m.l.Embed(ctx, []string{"x"}); err != nil {
			t.Fatalf("重排坏了不该影响嵌入：%v", err)
		}
		if s, _ := m.l.Status(); s != StatusReady {
			t.Fatalf("重排模型的问题不该让嵌入变 down：%s", s)
		}
	})
}

// embed 是常驻程序的依赖，不能 import localmodels，所以前缀、退出码、令牌变量名在 local.go 里写了一份；这里对着服务端那一份断言相等
func TestModelsConstantsMatchServer(t *testing.T) {
	if modelsErrPrefix != server.ErrPrefix || exitConfig != server.ExitConfig || exitPortBusy != server.ExitPortBusy || tokenEnv != server.TokenEnv {
		t.Fatalf("和 localmodels/server 对不上：%q %d %d %q", modelsErrPrefix, exitConfig, exitPortBusy, tokenEnv)
	}
	if BackendGoModels != "go-models" || removedBackendLegacy != "legacy" {
		t.Fatalf("后端取值：%q %q", BackendGoModels, removedBackendLegacy)
	}
}

// exe、模型根、dll 的路径里有空格和中文时，命令行参数原样传到子进程（exec.Command 在 Windows 上自己拼带引号的命令行，这里验一遍）
func TestLocalGoModelsPathsWithSpacesAndChinese(t *testing.T) {
	if testing.Short() {
		t.Skip("反复起真实子进程，放在每日整套；握手和错误分类有单测")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "含 空格 与中文 的目录")
	if err := os.MkdirAll(filepath.Join(dir, "模型 根"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "sumika models 副本.exe")
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		t.Fatal(err)
	}
	ort := filepath.Join(dir, "onnx runtime 动态库.dll")
	if err := os.WriteFile(ort, []byte("假的 dll"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "模型 根")
	m := startModelsFake(t, nil, func(c *LocalConfig) { c.Exe, c.ModelsRoot, c.OrtLibrary = exe, root, ort })
	m.waitReady(t)
	args := m.argsRecords(t)[0].Args
	got := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		got[args[i]] = args[i+1]
	}
	if got["--models-root"] != root || got["--ort-library"] != ort {
		t.Fatalf("含空格和中文的路径该原样传到子进程：\n得到 %q\n要   %q 和 %q", args, root, ort)
	}
}

// W1 契约 6 的丢行保护是靠 os/exec 的约定：cmd.Stderr 给 io.Writer，os/exec 自己起拷贝、Wait 等拷完才返回，所以进程写完就退时最后几行不丢。
// 改回 StderrPipe + 另起 goroutine 读、再另起 goroutine Wait，Wait 会在读的一方还没读完时关掉管道，最后一行（正是 [sumika-models] 错误：）可能丢。
// 这是竞争条件，行为上抓不住：直接改回去 12 轮丢不出行，要让读的一方慢 300 毫秒才红，而这个"慢"没有能注入的接缝
// （新写法里根本没有读的 goroutine）。所以这里读源码守住写法本身：local.go 里不许出现 StderrPipe，stderr 必须接在 tailBuffer 上。
// 改坏检查：在 once() 里加一行 cmd.StderrPipe()，这条红。
func TestLocalStderrGoesThroughCmdStderr(t *testing.T) {
	src, err := os.ReadFile("local.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if strings.Contains(text, "StderrPipe") {
		t.Error("local.go 里出现了 StderrPipe：stderr 要用 cmd.Stderr = tail（os/exec 自己拷、Wait 等拷完），不然进程写完就退时最后几行可能丢")
	}
	if !strings.Contains(text, "cmd.Stderr = tail") || !strings.Contains(text, "cmd.WaitDelay") {
		t.Error("local.go 里该有 cmd.Stderr = tail 和 cmd.WaitDelay（孙进程握着 stderr 句柄不放时，Wait 不会被一直拖住）")
	}
}

// memory.rerank.model 写的 id 服务里没有（拼错、写成别的名字）：嵌入照常就绪，只有重排用不了，
// Rerank 回带原因码 rerank-model-unknown 的错、写明是哪个 id；写对了就正常。
// 改坏检查：probe 不查重排 id（checkRerankListed 恒返回空），这一条红
func TestLocalRerankModelUnknown(t *testing.T) {
	t.Run("id 服务里没有", func(t *testing.T) {
		m := startModelsFake(t, map[string]string{embedtest.EnvRerankModels: "other-rerank"})
		m.waitReady(t)
		if _, err := m.l.Embed(context.Background(), []string{"你好"}); err != nil {
			t.Fatalf("重排 id 写错不该影响嵌入：%v", err)
		}
		_, _, err := m.l.Rerank(context.Background(), "q", []string{"a", "b"})
		if !errors.Is(err, ErrRerankModelUnknown) || !strings.Contains(err.Error(), "bge-x") || !strings.Contains(err.Error(), "other-rerank") {
			t.Fatalf("该回 rerank-model-unknown 并写明写的 id 和服务里有的：%v", err)
		}
		for _, r := range m.requests(t) {
			if strings.Contains(r.Path, "rerank") {
				t.Fatalf("id 不对时不该把重排请求发出去：%+v", r)
			}
		}
	})
	t.Run("id 写对了照常重排", func(t *testing.T) {
		m := startModelsFake(t, nil)
		m.waitReady(t)
		if _, _, err := m.l.Rerank(context.Background(), "q", []string{"a", "b"}); err != nil {
			t.Fatalf("id 对的重排：%v", err)
		}
	})
}
