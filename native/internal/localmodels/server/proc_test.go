package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/localmodels/ort"
	"sumika-native/internal/localmodels/pool"
	"sumika-native/internal/rpcwire"
)

// 真进程的测试：把测试程序自己当成 sumika-models 再起一遍（TestMain 里看环境变量），所以有自己的 PID、退出码、
// stderr、stdin 管道，和真的进程一样，只是 dll、分词器、模型是假的，不依赖任何本机文件。
// 真 dll、真模型的真进程测试在 cmd/sumika-models 里，用环境变量门着。

const childEnv = "SUMIKA_MODELS_TEST_CHILD"

// 错误行的稳定前缀写成字面值，不引用 server.ErrPrefix：Local 靠这串字认错误行（W1 契约第 6 条），常量被改了测试要红
const wantErrPrefix = "[sumika-models] 错误："

func TestMain(m *testing.M) {
	if mode := os.Getenv(childEnv); mode != "" {
		os.Exit(runChild(mode))
	}
	os.Exit(m.Run())
}

// ---- 文件版的假登记项：模型文件是固定内容，哈希在测试进程和子进程里都算得出来 ----

var (
	fakeOnnxBytes = []byte("fake onnx bytes for sumika-models tests")
	fakeTokBytes  = []byte(`{"fake":"tokenizer.json"}`)
)

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fileSpecs() (embed, rerank engine.Spec) {
	embed, rerank = fakeSpecs()
	for _, sp := range []*engine.Spec{&embed, &rerank} {
		sp.Onnx = "model.onnx"
		sp.OnnxSHA256 = sha256hex(fakeOnnxBytes)
		sp.TokenizerSHA256 = sha256hex(fakeTokBytes)
	}
	embed.Dir, rerank.Dir = "fake/embed", "fake/rerank"
	return embed, rerank
}

func fileRegistry() (*engine.Registry, error) {
	e, r := fileSpecs()
	return engine.NewRegistry(e, r)
}

// writeModel 把一个登记项的两个文件写进模型根目录（内容可以故意写错）。
func writeModel(t testing.TB, root string, spec engine.Spec, onnx, tok []byte) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(spec.Dir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, spec.Onnx), onnx, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), tok, 0o644); err != nil {
		t.Fatal(err)
	}
}

func goodModelsRoot(t testing.TB, which ...string) string {
	t.Helper()
	root := t.TempDir()
	e, r := fileSpecs()
	for _, sp := range []engine.Spec{e, r} {
		if len(which) == 0 || contains(which, sp.ID) {
			writeModel(t, root, sp, fakeOnnxBytes, fakeTokBytes)
		}
	}
	return root
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- 假运行时和假加载函数（子进程、进程内的 Run 测试都用）----

type fakeRuntime struct {
	hang  bool
	delay time.Duration // 每次前向慢多久
}

func (fakeRuntime) Version() string { return "9.9.9-fake" }
func (fakeRuntime) Close() error    { return nil }
func (f fakeRuntime) NewSession(modelPath string, o ort.Options) (ort.Session, error) {
	s := &fakeSess{dim: 768, rerank: strings.Contains(filepath.ToSlash(modelPath), "/rerank/"), delay: f.delay}
	if f.hang {
		s.block = make(chan struct{}) // 永远不放开：模拟 ORT 卡死
	}
	return s, nil
}

// fakeOpen 代替 ort.Open：不管路径，直接给假运行时。
func fakeOpen(hang bool) func(string) (ort.Runtime, error) {
	return func(string) (ort.Runtime, error) { return fakeRuntime{hang: hang}, nil }
}

// fakeMakeLoad 造假加载函数：用被监视器包过的运行时开会话，再装成引擎（假分词器）。
func fakeMakeLoad(rt ort.Runtime) pool.LoadFunc {
	return func(ctx context.Context, spec engine.Spec) (pool.Loaded, error) {
		sess, err := rt.NewSession(spec.Dir+"/"+spec.Onnx, ort.Options{})
		if err != nil {
			return pool.Loaded{}, err
		}
		tok := &fakeTok{}
		switch spec.Kind {
		case engine.KindEmbed:
			e, err := engine.NewEmbedder(spec, tok, sess, engine.EmbedderOptions{})
			return pool.Loaded{Embedder: e, Close: sess.Close}, err
		default:
			r, err := engine.NewReranker(spec, tok, sess)
			return pool.Loaded{Reranker: r, Close: sess.Close}, err
		}
	}
}

// fakeEnv 是进程内 Run 测试和子进程共用的注入。
func fakeEnv(hang bool) Env {
	reg, err := fileRegistry()
	if err != nil {
		panic(err)
	}
	return Env{Registry: reg, OpenORT: fakeOpen(hang), MakeLoad: fakeMakeLoad}
}

// runChild 是子进程的入口。mode：fake（假 dll 假模型）、hang（前向永远卡住）、realort（真的 ort.Open，只测它的前几道关）、
// parent（辅助进程：起一个 fake 子进程，用 StdinPipe 握着不放，然后卡住，给 TestParentKilledExits 用）。
func runChild(mode string) int {
	switch mode {
	case "fake":
		return Run(os.Args[1:], fakeEnv(false))
	case "hang":
		return Run(os.Args[1:], fakeEnv(true))
	case "realort":
		env := fakeEnv(false)
		env.OpenORT = nil
		return Run(os.Args[1:], env)
	case "parent":
		return runParentHelper()
	}
	fmt.Fprintln(os.Stderr, "不认识的测试子进程模式", mode)
	return 99
}

func runParentHelper() int {
	cmd := exec.Command(os.Args[0], os.Args[1:]...)
	cmd.Env = append(baseEnv(), childEnv+"=fake", "SUMIKA_EMBED_TOKEN="+testToken)
	stdin, err := cmd.StdinPipe() // 握着写端：本进程没了，管道就关，孙进程读到 EOF
	if err != nil {
		return 98
	}
	_ = stdin
	if err := cmd.Start(); err != nil {
		return 97
	}
	fmt.Printf("CHILD_PID=%d\n", cmd.Process.Pid)
	time.Sleep(time.Hour)
	return 0
}

// ---- 子进程的辅助 ----

func baseEnv() []string {
	var env []string
	for _, k := range []string{"SystemRoot", "PATH", "TEMP", "TMP", "windir"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

type procOpts struct {
	mode    string            // 缺省 fake
	noToken bool              // 不设令牌环境变量
	env     map[string]string // 另外的环境变量
	stdin   bool              // 用管道接 stdin（不然是 NUL）
}

type proc struct {
	t     *testing.T
	cmd   *exec.Cmd
	out   *syncBuf
	stdin io.WriteCloser
	done  chan struct{}
	code  int
}

func startProc(t *testing.T, o procOpts, args ...string) *proc {
	t.Helper()
	if o.mode == "" {
		o.mode = "fake"
	}
	p := &proc{t: t, out: &syncBuf{}, done: make(chan struct{})}
	p.cmd = exec.Command(os.Args[0], args...)
	env := append(baseEnv(), childEnv+"="+o.mode)
	if !o.noToken {
		env = append(env, "SUMIKA_EMBED_TOKEN="+testToken)
	}
	for k, v := range o.env {
		env = append(env, k+"="+v)
	}
	p.cmd.Env = env
	p.cmd.Stderr = p.out
	if o.stdin {
		var err error
		if p.stdin, err = p.cmd.StdinPipe(); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := p.cmd.Wait()
		var ee *exec.ExitError
		switch {
		case err == nil:
			p.code = 0
		case errors.As(err, &ee):
			p.code = ee.ExitCode()
		default:
			p.code = -1
		}
		close(p.done)
	}()
	t.Cleanup(p.kill)
	return p
}

// kill 按 PID 结束（不按名字），已经退出就什么也不做。
func (p *proc) kill() {
	select {
	case <-p.done:
	default:
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// wait 等进程退出，返回退出码；超时返回 -100。
func (p *proc) wait(d time.Duration) int {
	select {
	case <-p.done:
		return p.code
	case <-time.After(d):
		return -100
	}
}

func (p *proc) stderrLines() []string {
	s := strings.TrimRight(p.out.String(), "\r\n")
	if s == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

var listenRE = regexp.MustCompile(`listen=(\S+)`)

// addr 等 READY 行出现，返回监听地址。
func (p *proc) addr() string {
	p.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if m := listenRE.FindStringSubmatch(p.out.String()); m != nil {
			return m[1]
		}
		if p.exited() {
			p.t.Fatalf("进程在 READY 之前退出了（退出码 %d）：\n%s", p.code, p.out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.t.Fatalf("15 秒内没等到 READY：\n%s", p.out.String())
	return ""
}

func (p *proc) do(method, path string, body any) (int, []byte) {
	p.t.Helper()
	return httpDo(p.t, p.addr(), method, path, body)
}

func httpDo(t *testing.T, addr, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://"+addr+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s：%v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// ---- 验收 8：退出码 ----

func TestProcessExitCodes(t *testing.T) {
	if testing.Short() {
		t.Skip("反复起真实子进程，放在每日整套；握手和错误分类有单测")
	}
	good := goodModelsRoot(t)
	e, _ := fileSpecs()

	badHash := t.TempDir() // 嵌入模型的 onnx 内容不对
	ee, rr := fileSpecs()
	writeModel(t, badHash, ee, []byte("被换掉的内容"), fakeTokBytes)
	writeModel(t, badHash, rr, fakeOnnxBytes, fakeTokBytes)

	missing := goodModelsRoot(t, rerankID) // 只有重排的文件，嵌入模型的缺失

	notADll := filepath.Join(t.TempDir(), "onnxruntime.dll")
	if err := os.WriteFile(notADll, []byte("这不是 dll"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		opts    procOpts
		args    []string
		code    int
		subs    []string // 第一行该含的
		notSubs []string
	}{
		{"缺令牌", procOpts{noToken: true}, []string{"--models-root", good}, 2, []string{"SUMIKA_EMBED_TOKEN"}, nil},
		{"令牌只有空白", procOpts{env: map[string]string{"SUMIKA_EMBED_TOKEN": "   "}}, []string{"--models-root", good}, 2, []string{"SUMIKA_EMBED_TOKEN"}, nil},
		{"监听 0.0.0.0", procOpts{}, []string{"--models-root", good, "--host", "0.0.0.0"}, 2, []string{"回环地址", "0.0.0.0"}, nil},
		{"监听非回环地址 10.1.2.3", procOpts{}, []string{"--host", "10.1.2.3"}, 2, []string{"回环地址", "10.1.2.3"}, nil},
		{"模型根目录没配", procOpts{}, nil, 2, []string{"LOCAL_MODELS_ROOT", "模型根目录"}, nil},
		{"模型根目录用环境变量也没配", procOpts{env: map[string]string{"LOCAL_MODELS_ROOT": ""}}, nil, 2, []string{"LOCAL_MODELS_ROOT"}, nil},
		{"模型根目录不存在", procOpts{}, []string{"--models-root", filepath.Join(good, "不存在的目录")}, 2, []string{"模型根目录", "不存在的目录"}, nil},
		{"模型根目录是相对路径", procOpts{}, []string{"--models-root", "models"}, 2, []string{"绝对路径"}, nil},
		{"点名的模型哈希对不上", procOpts{}, []string{"--models-root", badHash}, 2, []string{"fake-embed", "SHA-256", "fake/embed/model.onnx"}, nil},
		{"点名的模型文件缺失（两个文件一次列全）", procOpts{}, []string{"--models-root", missing}, 2, []string{"fake-embed", "找不到文件", "fake/embed/model.onnx", "fake/embed/tokenizer.json"}, []string{missing}},
		{"dll 不在已知好的清单里", procOpts{mode: "realort"}, []string{"--models-root", good, "--ort-library", notADll}, 2, []string{"onnxruntime", "SHA-256"}, nil},
		{"dll 是相对路径", procOpts{mode: "realort"}, []string{"--models-root", good, "--ort-library", "onnxruntime.dll"}, 2, []string{"onnxruntime", "绝对路径"}, nil},
		{"dll 不存在", procOpts{mode: "realort"}, []string{"--models-root", good, "--ort-library", filepath.Join(good, "没有.dll")}, 2, []string{"onnxruntime", "找不到文件"}, nil},
		{"没有 --ort-library 时找 exe 旁边的 dll（找不到）", procOpts{mode: "realort"}, []string{"--models-root", good}, 2, []string{"onnxruntime", "找不到文件"}, nil},
		{"不认识的参数", procOpts{}, []string{"--bogus"}, 2, []string{"命令行参数有问题", "bogus"}, nil},
		{"--config 这一版没有", procOpts{}, []string{"--config", "x.toml"}, 2, []string{"--config", "这一版没有"}, nil},
		{"多余的位置参数", procOpts{}, []string{"--models-root", good, "oops"}, 2, []string{"多余的参数"}, nil},
		{"--models 里的 id 不认识", procOpts{}, []string{"--models-root", good, "--models", "qwen3"}, 2, []string{"--models", "qwen3", "fake-embed"}, nil},
		{"同一个模型在两个列表里", procOpts{}, []string{"--models-root", good, "--models", rerankID, "--lazy-models", rerankID}, 2, []string{"同时在 --models 和 --lazy-models"}, nil},
		{"--max-line-tokens 太小", procOpts{}, []string{"--models-root", good, "--max-line-tokens", "3"}, 2, []string{"--max-line-tokens"}, nil},
		{"--port 超范围", procOpts{}, []string{"--models-root", good, "--port", "70000"}, 2, []string{"--port"}, nil},
		{"环境变量里的端口不是整数", procOpts{env: map[string]string{"SUMIKA_EMBED_PORT": "abc"}}, []string{"--models-root", good}, 2, []string{"SUMIKA_EMBED_PORT"}, nil},
	}
	_ = e
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := startProc(t, c.opts, c.args...)
			if code := p.wait(15 * time.Second); code != c.code {
				t.Fatalf("想要退出码 %d，实际 %d：\n%s", c.code, code, p.out.String())
			}
			lines := p.stderrLines()
			if len(lines) == 0 || !strings.HasPrefix(lines[0], wantErrPrefix) {
				t.Fatalf("stderr 第一行该带 %q：\n%s", wantErrPrefix, p.out.String())
			}
			mustContain(t, "第一行", lines[0], c.subs...)
			for _, s := range c.notSubs {
				if strings.Contains(lines[0], s) {
					t.Errorf("第一行不该含 %q：%s", s, lines[0])
				}
			}
			if strings.Contains(p.out.String(), testToken) {
				t.Errorf("stderr 里有令牌：\n%s", p.out.String())
			}
		})
	}

	t.Run("端口被占是退出码 3", func(t *testing.T) {
		busy, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Close()
		port := busy.Addr().(*net.TCPAddr).Port
		p := startProc(t, procOpts{}, "--models-root", good, "--port", strconv.Itoa(port))
		if code := p.wait(15 * time.Second); code != 3 {
			t.Fatalf("想要退出码 3（端口被占），实际 %d：\n%s", code, p.out.String())
		}
		lines := p.stderrLines()
		if len(lines) == 0 || !strings.HasPrefix(lines[0], wantErrPrefix) {
			t.Fatalf("stderr 第一行该带前缀：\n%s", p.out.String())
		}
		mustContain(t, "第一行", lines[0], strconv.Itoa(port), "已被占用")
	})

	t.Run("端口被另一个 sumika-models 占着也是 3", func(t *testing.T) {
		first := startProc(t, procOpts{}, "--models-root", good, "--port", "0")
		addr := first.addr()
		port := addr[strings.LastIndex(addr, ":")+1:]
		second := startProc(t, procOpts{}, "--models-root", good, "--port", port)
		if code := second.wait(15 * time.Second); code != 3 {
			t.Fatalf("想要退出码 3，实际 %d：\n%s", code, second.out.String())
		}
		if code, _ := httpDo(t, addr, http.MethodGet, "/health", nil); code != 200 {
			t.Errorf("第一个进程该照常活着：/health 是 %d", code)
		}
	})
}

// 看门狗：一次前向超过 --run-watchdog-seconds，进程自己退出（退出码 4），stderr 最后一行是 "错误：前向超时"。
func TestWatchdogExitsWithCode4(t *testing.T) {
	good := goodModelsRoot(t)
	p := startProc(t, procOpts{mode: "hang"}, "--models-root", good, "--port", "0", "--run-watchdog-seconds", "1")
	addr := p.addr()
	go func() { // 这个请求永远等不到答案：假会话的 Run 卡住了
		body, _ := json.Marshal(map[string]any{"model": embedID, "input": "卡住的前向"})
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/embeddings", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		if resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	t0 := time.Now()
	// /health 在前向卡着的时候照样答，且 run_elapsed_ms 在涨
	time.Sleep(500 * time.Millisecond)
	if !p.exited() {
		code, out := httpDo(t, addr, http.MethodGet, "/health", nil)
		var h struct {
			RunElapsedMs int64 `json:"run_elapsed_ms"`
		}
		_ = json.Unmarshal(out, &h)
		if code == 200 && h.RunElapsedMs <= 0 {
			t.Errorf("前向卡着的时候 run_elapsed_ms 该大于 0：%s", out)
		}
	}
	if code := p.wait(10 * time.Second); code != 4 {
		t.Fatalf("想要退出码 4（看门狗），实际 %d：\n%s", code, p.out.String())
	}
	if took := time.Since(t0); took > 4*time.Second {
		t.Errorf("--run-watchdog-seconds 1 却过了 %v 才退出", took)
	}
	lines := p.stderrLines()
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, wantErrPrefix+"前向超时") {
		t.Errorf("stderr 最后一行该是 %q 开头：%q", wantErrPrefix+"前向超时", last)
	}
}

// --run-watchdog-seconds 0 是关：前向卡着，进程不退。
func TestWatchdogCanBeDisabled(t *testing.T) {
	good := goodModelsRoot(t)
	p := startProc(t, procOpts{mode: "hang"}, "--models-root", good, "--port", "0", "--run-watchdog-seconds", "0")
	addr := p.addr()
	go func() {
		body, _ := json.Marshal(map[string]any{"model": embedID, "input": "卡住的前向"})
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/embeddings", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		if resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req); err == nil {
			resp.Body.Close()
		}
	}()
	if code := p.wait(2500 * time.Millisecond); code != -100 {
		t.Fatalf("关掉看门狗以后进程不该退出，退出码 %d：\n%s", code, p.out.String())
	}
}

// ---- stdin 与父进程 ----

func TestStdinEOFExits(t *testing.T) {
	good := goodModelsRoot(t)
	t.Run("带 --exit-on-stdin-eof，关掉 stdin 就退", func(t *testing.T) {
		p := startProc(t, procOpts{stdin: true}, "--models-root", good, "--port", "0", "--exit-on-stdin-eof")
		p.addr()
		t0 := time.Now()
		_ = p.stdin.Close()
		if code := p.wait(2 * time.Second); code != 0 {
			t.Fatalf("关掉 stdin 以后 2 秒内该退出（退出码 0），实际 %d：\n%s", code, p.out.String())
		}
		t.Logf("关 stdin 到退出：%v", time.Since(t0))
		mustContain(t, "stderr", p.out.String(), "STOP reason=stdin-eof", "EXIT code=0")
	})
	t.Run("不带这个开关，stdin 是 NUL，进程照常活着", func(t *testing.T) {
		p := startProc(t, procOpts{}, "--models-root", good, "--port", "0")
		if code, _ := p.do(http.MethodGet, "/health", nil); code != 200 {
			t.Fatalf("/health：%d", code)
		}
		if code := p.wait(1500 * time.Millisecond); code != -100 {
			t.Fatalf("stdin 是 NUL（一上来就是 EOF），不带开关不该退出，退出码 %d：\n%s", code, p.out.String())
		}
		if code, _ := p.do(http.MethodGet, "/health", nil); code != 200 {
			t.Errorf("1.5 秒以后 /health：%d", code)
		}
	})
	t.Run("带开关但没人关 stdin，进程不退", func(t *testing.T) {
		p := startProc(t, procOpts{stdin: true}, "--models-root", good, "--port", "0", "--exit-on-stdin-eof")
		p.addr()
		if code := p.wait(1500 * time.Millisecond); code != -100 {
			t.Fatalf("stdin 还开着，不该退出，退出码 %d", code)
		}
	})
}

// 父进程被强杀（任务管理器、崩了）：管道照样会关，子进程 2 秒内退出。"关 stdin 就退"和"父进程被强杀"不是一回事，
// 真正要防的是后者：没有它，Go 后台崩一次就留下一个占着内存和端口的孤儿。
func TestParentKilledExits(t *testing.T) {
	good := goodModelsRoot(t)
	helper := exec.Command(os.Args[0], "--models-root", good, "--port", "0", "--exit-on-stdin-eof")
	helper.Env = append(baseEnv(), childEnv+"=parent")
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	helperPID := helper.Process.Pid
	helperDone := make(chan struct{})
	go func() { _ = helper.Wait(); close(helperDone) }()
	t.Cleanup(func() { _ = helper.Process.Kill() })

	var childPID int
	sc := bufio.NewScanner(stdout)
	lineCh := make(chan string, 1)
	go func() {
		if sc.Scan() {
			lineCh <- sc.Text()
		}
	}()
	select {
	case line := <-lineCh:
		if _, err := fmt.Sscanf(line, "CHILD_PID=%d", &childPID); err != nil {
			t.Fatalf("辅助进程的输出不对：%q", line)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("辅助进程没有报出子进程的 PID")
	}
	// 万一没退，按 PID 结束。不提前拿着孙进程的句柄：Windows 上有人握着句柄，进程退了也还能 OpenProcess 成功，ProcessAlive 就一直说活着
	t.Cleanup(func() {
		if p, err := os.FindProcess(childPID); err == nil {
			_ = p.Kill()
			_ = p.Release()
		}
	})
	time.Sleep(800 * time.Millisecond) // 让孙进程走到 READY 并开始读 stdin
	// 孙进程不是本测试的子进程（是辅助进程的子进程），没法 Wait；两个系统都靠 rpcwire.ProcessAlive 轮询它还在不在
	childGone := make(chan struct{})
	go func() {
		for rpcwire.ProcessAlive(childPID) {
			time.Sleep(20 * time.Millisecond)
		}
		close(childGone)
	}()
	select {
	case <-childGone:
		t.Fatal("孙进程在父进程被杀之前就退出了")
	default:
	}

	// 强杀辅助进程（不是正常退出）
	// Process.Kill 在 Windows 上是 TerminateProcess（和原来的 taskkill /F 一样直接结束），在 Unix 上是 SIGKILL
	if err := helper.Process.Kill(); err != nil {
		t.Fatalf("强杀辅助进程 %d：%v", helperPID, err)
	}
	<-helperDone
	t0 := time.Now()
	select {
	case <-childGone:
		t.Logf("父进程被强杀到子进程退出：%v", time.Since(t0))
		if took := time.Since(t0); took > 2*time.Second {
			t.Errorf("子进程 %v 才退出，该在 2 秒内", took)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("父进程被强杀 2 秒了，子进程还活着（孤儿）")
	}
}

// ---- 验收 8a：只查点名的 ----

func TestOnlyNamedModelsAreRequired(t *testing.T) {
	t.Run("--models 只点名嵌入：删掉重排的目录，嵌入照常，重排说明没点名", func(t *testing.T) {
		root := goodModelsRoot(t, embedID) // 没有重排模型的文件
		p := startProc(t, procOpts{}, "--models-root", root, "--port", "0", "--models", embedID)
		if code, out := p.do(http.MethodPost, "/v1/embeddings", map[string]any{"model": embedID, "input": "你好"}); code != 200 {
			t.Fatalf("嵌入：%d %s", code, out)
		}
		code, out := p.do(http.MethodPost, "/v1/rerank", map[string]any{"model": rerankID, "query": "q", "documents": []string{"d"}})
		if code != 404 {
			t.Fatalf("没点名的重排：想要 404，实际 %d %s", code, out)
		}
		mustContain(t, "detail", detailOf(t, out), "registered but not selected")
	})

	t.Run("缺省（全部登记的）都必须就绪：缺一个就退出码 2", func(t *testing.T) {
		root := goodModelsRoot(t, embedID)
		p := startProc(t, procOpts{}, "--models-root", root, "--port", "0")
		if code := p.wait(15 * time.Second); code != 2 {
			t.Fatalf("缺省要查全部登记的，重排缺了该退出码 2，实际 %d：\n%s", code, p.out.String())
		}
		mustContain(t, "第一行", p.stderrLines()[0], rerankID, "找不到文件")
	})

	// memory.rerank.model 写错 id（拼错、写成别的名字）只该让重排用不了，不该让必须就绪的嵌入也起不来。
	// 原来 --lazy-models 里不认识的 id 在启动检查里就退出码 2，Local 照退出码 2 永久放弃，嵌入和守卫一起停（复核发现）。
	// 改坏检查：selectModels 之前不拿掉不认识的 lazy id，这一条红
	t.Run("--lazy-models 里不认识的 id：忽略并写 WARN，嵌入照常，请求它回 404", func(t *testing.T) {
		root := goodModelsRoot(t, embedID)
		p := startProc(t, procOpts{}, "--models-root", root, "--port", "0", "--models", embedID, "--lazy-models", "bge-reranker-typo")
		if p.exited() {
			t.Fatalf("lazy 里写错 id 不该让进程退出：%s", p.out.String())
		}
		if code, out := p.do(http.MethodPost, "/v1/embeddings", map[string]any{"model": embedID, "input": "你好"}); code != 200 {
			t.Fatalf("嵌入：%d %s", code, out)
		}
		_, out := p.do(http.MethodGet, "/v1/models", nil)
		var ml struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(out, &ml); err != nil || len(ml.Data) != 1 || ml.Data[0]["id"] != embedID {
			t.Fatalf("/v1/models 该只有嵌入一个：%s", out)
		}
		if code, out := p.do(http.MethodPost, "/v1/rerank", map[string]any{"model": "bge-reranker-typo", "query": "q", "documents": []string{"d"}}); code != 404 {
			t.Fatalf("请求写错的 id：想要 404，实际 %d %s", code, out)
		}
		mustContain(t, "stderr", p.out.String(), "WARN", "bge-reranker-typo", "不在登记表里", "已忽略")
	})
	t.Run("--lazy-models 放重排：文件缺了只它自己 ready=false，补上以后不用重启", func(t *testing.T) {
		root := goodModelsRoot(t, embedID)
		p := startProc(t, procOpts{}, "--models-root", root, "--port", "0", "--models", embedID, "--lazy-models", rerankID)
		if code, out := p.do(http.MethodPost, "/v1/embeddings", map[string]any{"model": embedID, "input": "你好"}); code != 200 {
			t.Fatalf("嵌入：%d %s", code, out)
		}
		ready := func() map[string]any {
			_, out := p.do(http.MethodGet, "/v1/models", nil)
			var ml struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal(out, &ml); err != nil || len(ml.Data) != 2 {
				t.Fatalf("/v1/models：%s", out)
			}
			m := map[string]any{}
			for _, row := range ml.Data {
				m[row["id"].(string)] = row["ready"]
			}
			return m
		}
		if got := ready(); got[embedID] != true || got[rerankID] != false {
			t.Fatalf("ready 该是嵌入 true、重排 false：%v", got)
		}
		code, out := p.do(http.MethodPost, "/v1/rerank", map[string]any{"model": rerankID, "query": "q", "documents": []string{"d"}})
		if code != 500 {
			t.Fatalf("文件缺了的重排：想要 500，实际 %d %s", code, out)
		}
		mustContain(t, "detail", detailOf(t, out), "找不到文件", "fake/rerank/model.onnx")
		if strings.Contains(string(out), root) {
			t.Errorf("响应里有模型根目录的绝对路径：%s", out)
		}
		if p.exited() {
			t.Fatalf("lazy 模型的问题不该让进程退出：\n%s", p.out.String())
		}
		// 文件补上了
		_, r := fileSpecs()
		writeModel(t, root, r, fakeOnnxBytes, fakeTokBytes)
		if code, out := p.do(http.MethodPost, "/v1/rerank", map[string]any{"model": rerankID, "query": "q", "documents": []string{"d"}}); code != 200 {
			t.Fatalf("文件补上以后：想要 200，实际 %d %s", code, out)
		}
		if got := ready(); got[rerankID] != true {
			t.Errorf("装好以后 ready 该恢复：%v", got)
		}
	})
}

// ---- 优雅退出（信号）、--verify、参数 ----

func TestRunStopsOnSignal(t *testing.T) {
	good := goodModelsRoot(t)
	logs := &syncBuf{}
	sig := make(chan os.Signal, 1)
	ready := make(chan string, 1)
	env := fakeEnv(false)
	env.Stderr, env.Signals = logs, sig
	env.Getenv = func(k string) string {
		if k == TokenEnv {
			return testToken
		}
		return ""
	}
	env.Ready = func(addr string) { ready <- addr }
	done := make(chan int, 1)
	go func() {
		done <- Run([]string{"--models-root", good, "--port", "0", "--idle-unload", "1", "--max-loaded", "0"}, env)
	}()
	var addr string
	select {
	case addr = <-ready:
	case code := <-done:
		t.Fatalf("Run 在 READY 之前返回了（退出码 %d）：\n%s", code, logs.String())
	case <-time.After(15 * time.Second):
		t.Fatalf("15 秒内没等到 READY：\n%s", logs.String())
	}
	if code, out := httpDo(t, addr, http.MethodPost, "/v1/embeddings", map[string]any{"model": embedID, "input": "你好"}); code != 200 {
		t.Fatalf("嵌入：%d %s", code, out)
	}
	_, out := httpDo(t, addr, http.MethodGet, "/health", nil)
	var h struct {
		MaxLoaded int `json:"max_loaded"`
	}
	if err := json.Unmarshal(out, &h); err != nil || h.MaxLoaded != 1 {
		t.Errorf("--max-loaded 0 该夹成 1（池里的 0 是\"用缺省 2\"），/health 报的是：%s", out)
	}
	sig <- os.Interrupt
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("收到信号以后想要退出码 0，实际 %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("收到信号 10 秒了还没退出")
	}
	mustContain(t, "日志", logs.String(), "STOP reason=signal", "UNLOADED model=fake-embed", "EXIT code=0", "max_loaded=1") // --max-loaded 0 夹成 1
	if _, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		t.Error("退出以后端口还在听")
	}
}

// 收尾顺序：先停 HTTP（等处理中的请求做完），再关池，最后关运行时。处理中的请求不能被收尾弄成失败。
func TestShutdownWaitsForInFlightRequest(t *testing.T) {
	good := goodModelsRoot(t)
	logs := &syncBuf{}
	sig := make(chan os.Signal, 1)
	ready := make(chan string, 1)
	env := fakeEnv(false)
	env.OpenORT = func(string) (ort.Runtime, error) { return fakeRuntime{delay: 700 * time.Millisecond}, nil }
	env.Stderr, env.Signals = logs, sig
	env.Getenv = func(k string) string {
		if k == TokenEnv {
			return testToken
		}
		return ""
	}
	env.Ready = func(addr string) { ready <- addr }
	done := make(chan int, 1)
	go func() { done <- Run([]string{"--models-root", good, "--port", "0"}, env) }()
	addr := <-ready

	result := make(chan int, 1)
	go func() {
		body, _ := json.Marshal(map[string]any{"model": embedID, "input": "收尾时还在算的请求"})
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/embeddings", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			result <- -1
			return
		}
		resp.Body.Close()
		result <- resp.StatusCode
	}()
	time.Sleep(300 * time.Millisecond) // 前向已经开始，还要约 400 毫秒
	sig <- os.Interrupt
	if code := <-result; code != 200 {
		t.Errorf("收尾时处理中的请求该做完（200），实际 %d", code)
	}
	if code := <-done; code != 0 {
		t.Errorf("退出码想要 0，实际 %d\n%s", code, logs.String())
	}
}

func TestRunVerify(t *testing.T) {
	good := goodModelsRoot(t)
	var out, errb bytes.Buffer
	env := fakeEnv(false)
	env.Stdout, env.Stderr = &out, &errb
	if code := Run([]string{"--verify", "--models-root", good}, env); code != 0 {
		t.Fatalf("文件都对：想要退出码 0，实际 %d\n%s", code, errb.String())
	}
	mustContain(t, "stdout", out.String(), "OK   fake-embed", "OK   fake-rerank")

	bad := t.TempDir()
	e, r := fileSpecs()
	writeModel(t, bad, e, fakeOnnxBytes, fakeTokBytes)
	writeModel(t, bad, r, []byte("坏的"), fakeTokBytes)
	out.Reset()
	errb.Reset()
	if code := Run([]string{"--verify", "--models-root", bad}, env); code != 1 {
		t.Fatalf("有一个对不上：想要退出码 1，实际 %d", code)
	}
	mustContain(t, "stdout", out.String(), "OK   fake-embed", "FAIL fake-rerank", "SHA-256")
	if !strings.HasPrefix(errb.String(), wantErrPrefix) {
		t.Errorf("stderr 第一行该带前缀：%q", errb.String())
	}

	// --verify 不要令牌、不开 dll
	env.OpenORT = func(string) (ort.Runtime, error) {
		t.Error("--verify 不该打开 dll")
		return nil, errors.New("不该走到这里")
	}
	env.Getenv = func(k string) string { return "" }
	if code := Run([]string{"--verify", "--models-root", good}, env); code != 0 {
		t.Errorf("--verify 不需要令牌：退出码 %d", code)
	}
	// 根目录没配是 2，不是 1
	if code := Run([]string{"--verify"}, env); code != 2 {
		t.Errorf("--verify 没配根目录：想要退出码 2，实际 %d", code)
	}
}

func TestSelectModels(t *testing.T) {
	reg, err := fileRegistry()
	if err != nil {
		t.Fatal(err)
	}
	ids := func(specs []engine.Spec) string {
		var out []string
		for _, s := range specs {
			out = append(out, s.ID)
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		name, models, lazy  string
		wantNamed, wantLazy string
		wantErr             string
	}{
		{"缺省 = 登记的全部", "", "", "fake-embed,fake-rerank", "", ""},
		{"缺省但 lazy 里有重排", "", rerankID, embedID, rerankID, ""},
		{"点名嵌入", embedID, "", embedID, "", ""},
		{"点名加空格和空项", " fake-embed , ,", "", embedID, "", ""},
		{"点名嵌入 lazy 重排", embedID, rerankID, embedID, rerankID, ""},
		{"全是 lazy", "", embedID + "," + rerankID, "", "fake-embed,fake-rerank", ""},
		{"不认识的 id", "nope", "", "", "", "不在登记表里"},
		{"lazy 里不认识", "", "nope", "", "", "--lazy-models"},
		{"写了两次", embedID + "," + embedID, "", "", "", "写了两次"},
		{"两边都有", embedID, embedID, "", "", "同时在"},
	}
	for _, c := range cases {
		named, lazy, err := selectModels(reg, c.models, c.lazy)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s：想要含 %q 的错误，实际 %v", c.name, c.wantErr, err)
			}
			continue
		}
		if err != nil && c.wantNamed == "" && c.wantLazy != "" {
			t.Errorf("%s：%v", c.name, err)
			continue
		}
		if err != nil {
			t.Errorf("%s：%v", c.name, err)
			continue
		}
		if ids(named) != c.wantNamed || ids(lazy) != c.wantLazy {
			t.Errorf("%s：named=%s lazy=%s，想要 %s / %s", c.name, ids(named), ids(lazy), c.wantNamed, c.wantLazy)
		}
	}
}

func TestPortFromEnvironment(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  string
		want int
		bad  bool
	}{
		{"都没给用 11440", nil, "", 11440, false},
		{"环境变量", nil, "11555", 11555, false},
		{"参数压过环境变量", []string{"--port", "0"}, "11555", 0, false},
		{"环境变量不是整数", nil, "abc", 0, true},
		{"环境变量超范围", nil, "99999", 0, true},
	}
	for _, c := range cases {
		opts, _, err := parseFlags(c.args)
		if err != nil {
			t.Fatal(err)
		}
		env := Env{Getenv: func(k string) string {
			if k == PortEnv {
				return c.env
			}
			return ""
		}}
		got, err := resolvePort(opts, &env)
		if (err != nil) != c.bad || (!c.bad && got != c.want) {
			t.Errorf("%s：得到 %d，%v；想要 %d，出错=%v", c.name, got, err, c.want, c.bad)
		}
	}
}

func TestIsPortProblem(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	_, err = net.Listen("tcp", busy.Addr().String()) // 真的端口占用，不是造出来的错误
	if err == nil {
		t.Fatal("同一个端口监听两次该失败")
	}
	if !isPortProblem(err) {
		t.Errorf("真实的端口占用错误该被认出来：%v（%T）", err, err)
	}
	if isPortProblem(errors.New("listen tcp: lookup nope: no such host")) {
		t.Error("别的错误不该被当成端口问题")
	}
	_, err = net.Listen("tcp", "300.300.300.300:80") // 地址写错：配置错误，不是端口问题
	if err == nil || isPortProblem(err) {
		t.Errorf("地址写错该是配置错误：%v", err)
	}
}

// ---- 看门狗本身（不用真进程）----

func TestWatchdogUnit(t *testing.T) {
	newMon := func() (*Monitor, *time.Time) {
		m := NewMonitor()
		now := time.Unix(1_800_000_000, 0)
		m.now = func() time.Time { return now }
		return m, &now
	}
	run := func(m *Monitor, limit time.Duration, wait time.Duration) (code int, msg string) {
		got := make(chan [2]any, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		w := &Watchdog{Mon: m, Limit: limit, Interval: 5 * time.Millisecond, Fail: func(c int, s string) { got <- [2]any{c, s} }}
		go w.Run(ctx)
		select {
		case r := <-got:
			return r[0].(int), r[1].(string)
		case <-time.After(wait):
			return 0, ""
		}
	}
	m, now := newMon()
	end := m.begin()
	*now = now.Add(61 * time.Second)
	code, msg := run(m, 60*time.Second, 2*time.Second)
	if code != ExitWatchdog || !strings.Contains(msg, "前向超时") || !strings.Contains(msg, "61") {
		t.Errorf("跑了 61 秒、上限 60 秒：该触发退出码 4 和\"前向超时\"：%d %q", code, msg)
	}
	end(false)

	m, now = newMon()
	m.begin()
	*now = now.Add(59 * time.Second)
	if code, _ := run(m, 60*time.Second, 200*time.Millisecond); code != 0 {
		t.Errorf("才跑了 59 秒，不该触发：%d", code)
	}
	if code, _ := run(NewMonitor(), 60*time.Second, 200*time.Millisecond); code != 0 {
		t.Errorf("没有前向在跑，不该触发：%d", code)
	}
	m, now = newMon()
	m.begin()
	*now = now.Add(time.Hour)
	if code, _ := run(m, 0, 200*time.Millisecond); code != 0 {
		t.Errorf("上限 0 = 关，不该触发：%d", code)
	}
	if got := (&Watchdog{Limit: 60 * time.Second}).interval(); got != 5*time.Second {
		t.Errorf("上限 60 秒时检查间隔该封顶 5 秒：%v", got)
	}
	if got := (&Watchdog{Limit: time.Second}).interval(); got != 250*time.Millisecond {
		t.Errorf("上限 1 秒时检查间隔该是 250 毫秒：%v", got)
	}
}
