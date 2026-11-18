package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/testutil/junction"
)

// 真 dll、真模型、真 exe 的真进程测试（规格 GM5 验收 8、8a、8b、11）。用环境变量门着，没设就跳过并写明缺什么：
//
//	SUMIKA_TEST_ORT          官方发布包里的 onnxruntime.dll（scripts/fetch-ort.ts 取回的 vendor/ort/1.24.4/onnxruntime.dll）的绝对路径，在 ort 清单里
//	SUMIKA_TEST_MODELS_ROOT  模型根目录（如 <模型根>），只读
//	SUMIKA_TEST_SOAK=1       另外跑 TestRealExeSoak（几分钟，吃 CPU）
//
// 每个测试把 cmd/sumika-models 真的编成 exe 再起；参照数据用 ort/testdata 里 Python 版算的（GM2 导出）。

const (
	gteID  = "gte-multilingual-base"
	bgeID  = "bge-reranker-v2-m3"
	token  = "real-exe-test-" + "0123456789abcdef"
	refDir = "../../internal/localmodels/ort/testdata"
)

// gteDir 是登记表里 gte 的模型目录（相对模型根目录）。从登记表取，不再写一份：模型挪目录时登记表一改，这里跟着变。
func gteDir(t *testing.T) string {
	t.Helper()
	for _, s := range engine.BuiltinSpecs() {
		if s.ID == gteID {
			return s.Dir
		}
	}
	t.Fatalf("登记表里没有 %s", gteID)
	return ""
}

func gates(t *testing.T) (ortPath, root string) {
	t.Helper()
	ortPath, root = os.Getenv("SUMIKA_TEST_ORT"), os.Getenv("SUMIKA_TEST_MODELS_ROOT")
	var missing []string
	if ortPath == "" {
		missing = append(missing, "SUMIKA_TEST_ORT（onnxruntime.dll 的绝对路径）")
	}
	if root == "" {
		missing = append(missing, "SUMIKA_TEST_MODELS_ROOT（模型根目录）")
	}
	if len(missing) > 0 {
		t.Skip("缺环境变量：" + strings.Join(missing, "、"))
	}
	return ortPath, root
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func buildExe(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "sumika-models.exe")
	cmd := exec.Command("go", "build", "-o", out, ".")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build 失败：%v\n%s", err, b)
	}
	return out
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type exeProc struct {
	t     *testing.T
	cmd   *exec.Cmd
	out   *syncBuf
	stdin io.WriteCloser
	done  chan struct{}
	code  int
	addr  string
	began time.Time
}

// startExe 像 Local 那样起：令牌走环境变量，stdin 管道不关。
func startExe(t *testing.T, exe string, args ...string) *exeProc {
	t.Helper()
	p := &exeProc{t: t, out: &syncBuf{}, done: make(chan struct{})}
	port := freePort(t)
	p.addr = "127.0.0.1:" + strconv.Itoa(port)
	p.cmd = exec.Command(exe, append([]string{"--port", strconv.Itoa(port)}, args...)...)
	p.cmd.Env = append(os.Environ(), "SUMIKA_EMBED_TOKEN="+token)
	p.cmd.Stderr = p.out
	var err error
	if p.stdin, err = p.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	p.began = time.Now()
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := p.cmd.Wait()
		var ee *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &ee):
			p.code = ee.ExitCode()
		default:
			p.code = -1
		}
		close(p.done)
	}()
	t.Cleanup(func() { // 按 PID 结束，已经退出就什么也不做
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

func (p *exeProc) wait(d time.Duration) int {
	select {
	case <-p.done:
		return p.code
	case <-time.After(d):
		return -100
	}
}

// waitReady 等 /health 回 200，返回从起进程到就绪的毫秒数。
func (p *exeProc) waitReady() time.Duration {
	p.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.done:
			p.t.Fatalf("进程在就绪之前退出了（退出码 %d）：\n%s", p.code, p.out.String())
		default:
		}
		if code, _ := p.get("/health"); code == 200 {
			return time.Since(p.began)
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.t.Fatalf("60 秒内没就绪：\n%s", p.out.String())
	return 0
}

var client = &http.Client{Timeout: 5 * time.Minute}

func (p *exeProc) req(method, path string, body any) (int, []byte) {
	p.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(method, "http://"+p.addr+path, rd)
	if err != nil {
		p.t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(r)
	if err != nil {
		p.t.Fatalf("%s %s：%v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (p *exeProc) get(path string) (int, []byte) {
	p.t.Helper()
	r, err := http.NewRequest(http.MethodGet, "http://"+p.addr+path, nil)
	if err != nil {
		return -1, nil
	}
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(r)
	if err != nil {
		return -1, nil
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

type health struct {
	Engine       string `json:"engine"`
	ORTVersion   string `json:"ort_version"`
	RSSBytes     uint64 `json:"rss_bytes"`
	RunElapsedMs int64  `json:"run_elapsed_ms"`
	LastOKUnix   int64  `json:"last_ok_unix"`
	Models       []struct {
		ID     string `json:"id"`
		Loaded bool   `json:"loaded"`
		LoadMs *int64 `json:"load_ms"`
	} `json:"models"`
}

func (p *exeProc) health() health {
	p.t.Helper()
	code, out := p.get("/health")
	var h health
	if code != 200 || json.Unmarshal(out, &h) != nil {
		p.t.Fatalf("/health：%d %s", code, out)
	}
	return h
}

func mib(b uint64) float64 { return float64(b) / (1 << 20) }

func realArgs(ortPath, root string, extra ...string) []string {
	return append([]string{"--models-root", root, "--ort-library", ortPath, "--idle-unload", "0", "--threads", "2", "--max-line-tokens", "2048", "--exit-on-stdin-eof"}, extra...)
}

// ---- 参照数据 ----

func readF32(t *testing.T, name string) []float32 {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(refDir, name))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

func decodeEmbeddings(t *testing.T, body []byte) [][]float32 {
	t.Helper()
	var r struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("嵌入响应：%v\n%.300s", err, body)
	}
	out := make([][]float32, len(r.Data))
	for i, d := range r.Data {
		out[i] = d.Embedding
	}
	return out
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return dot / math.Sqrt(na*nb)
}

// longText 造一段互不重复的长文本，分词以后远超 2048 个 token（会被 --max-line-tokens 截到 2048）。
func longText(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n*3; i++ {
		fmt.Fprintf(&b, "第%d段：这是为了把前向撑到两千多个 token 编的一段话，编号%d，随便写点别的字免得重复。", i, i*7919%10007)
	}
	return b.String()
}

// ---- 验收 8（真 dll 真模型）：按生产方式起一次，冷启动秒数，对 Python 参照 ----

func TestRealExeProductionStart(t *testing.T) {
	ortPath, root := gates(t)
	exe := buildExe(t)
	info, _ := os.Stat(exe)
	t.Logf("exe 大小：%d 字节（%.1f MiB）", info.Size(), mib(uint64(info.Size())))

	p := startExe(t, exe, realArgs(ortPath, root, "--models", gteID, "--lazy-models", bgeID)...)
	cold := p.waitReady()
	t.Logf("冷启动（起进程到 /health 200，含 gte 的 onnx 和分词器哈希、打开 dll）：%d 毫秒", cold.Milliseconds())
	h := p.health()
	if h.Engine != "go" || h.ORTVersion != "1.24.4" || h.RSSBytes == 0 {
		t.Errorf("/health：%+v", h)
	}
	t.Logf("刚就绪时进程常驻内存 %.0f MiB，ort_version=%s", mib(h.RSSBytes), h.ORTVersion)

	// 嵌入：前 4 句编的样本对 Python 参照
	var cases []struct {
		Cat  string `json:"cat"`
		Text string `json:"text"`
	}
	raw, err := os.ReadFile(filepath.Join(refDir, "cases.json"))
	if err != nil || json.Unmarshal(raw, &cases) != nil {
		t.Fatalf("cases.json：%v", err)
	}
	ref := readF32(t, "ref-vecs-gte.f32")
	const n = 4
	texts := make([]string, n)
	for i := range texts {
		texts[i] = cases[i].Text
	}
	t0 := time.Now()
	code, out := p.req(http.MethodPost, "/v1/embeddings", map[string]any{"model": gteID, "input": texts})
	firstEmbed := time.Since(t0)
	if code != 200 {
		t.Fatalf("嵌入：%d %s\n%s", code, out, p.out.String())
	}
	t.Logf("第一次嵌入请求（含加载 gte）：%d 毫秒", firstEmbed.Milliseconds())
	vecs := decodeEmbeddings(t, out)
	worstDiff, worstCos := 0.0, 1.0
	for i, v := range vecs {
		want := ref[i*768 : (i+1)*768]
		if len(v) != 768 {
			t.Fatalf("第 %d 句向量长度 %d", i, len(v))
		}
		for j := range v {
			worstDiff = math.Max(worstDiff, math.Abs(float64(v[j])-float64(want[j])))
		}
		worstCos = math.Min(worstCos, cosine(v, want))
	}
	t.Logf("经 HTTP 的 %d 句向量对 Python 参照：最大分量差 %.3g，最低余弦 %.12f", n, worstDiff, worstCos)
	if worstDiff > 1e-6 || worstCos < 0.999999 {
		t.Errorf("向量对不上 Python 参照（门槛 1e-6、0.999999）")
	}

	// 重排（--lazy-models 里的，第一次请求才加载 bge）：6 对对 Python 的 logit
	var pairs []struct {
		Q     string  `json:"q"`
		D     string  `json:"d"`
		Logit float64 `json:"logit"`
	}
	raw, err = os.ReadFile(filepath.Join(refDir, "ref-logits-bge.json"))
	if err != nil || json.Unmarshal(raw, &pairs) != nil {
		t.Fatalf("ref-logits-bge.json：%v", err)
	}
	worstScore := 0.0
	for i, pr := range pairs {
		t0 := time.Now()
		code, out := p.req(http.MethodPost, "/v1/rerank", map[string]any{"model": bgeID, "query": pr.Q, "documents": []string{pr.D}})
		if code != 200 {
			t.Fatalf("重排：%d %s\n%s", code, out, p.out.String())
		}
		if i == 0 {
			t.Logf("第一次重排请求（含加载 bge）：%d 毫秒", time.Since(t0).Milliseconds())
		}
		var r struct {
			Results []struct {
				Score float64 `json:"relevance_score"`
			} `json:"results"`
		}
		_ = json.Unmarshal(out, &r)
		want := 1 / (1 + math.Exp(-pr.Logit))
		worstScore = math.Max(worstScore, math.Abs(r.Results[0].Score-want))
	}
	t.Logf("%d 对重排分数对 Python 参照（sigmoid(logit)）：最大差 %.3g", len(pairs), worstScore)
	if worstScore > 1e-6 {
		t.Errorf("重排分数对不上（门槛 1e-6）")
	}

	h = p.health()
	t.Logf("两个模型都装着：进程常驻内存 %.0f MiB", mib(h.RSSBytes))
	for _, m := range h.Models {
		if !m.Loaded || m.LoadMs == nil {
			t.Errorf("%s 该已加载：%+v", m.ID, m)
		}
	}

	// 父进程（这里是测试）关掉 stdin：进程自己退出
	t0 = time.Now()
	_ = p.stdin.Close()
	if code := p.wait(5 * time.Second); code != 0 {
		t.Fatalf("关掉 stdin 以后想要退出码 0，实际 %d\n%s", code, p.out.String())
	}
	t.Logf("关 stdin 到进程退出（两个模型都装着，要先关会话）：%d 毫秒", time.Since(t0).Milliseconds())
}

// ---- 验收 8a（真文件）：只查点名的 ----

func TestRealExeOnlyNamedModels(t *testing.T) {
	ortPath, root := gates(t)
	exe := buildExe(t)

	// 只有 gte 的模型根目录：用目录联接指到真的 gte 目录（只读，不拷 325 MB），没有 bge 的目录
	fake := t.TempDir()
	gte := filepath.FromSlash(gteDir(t))
	if err := os.MkdirAll(filepath.Dir(filepath.Join(fake, gte)), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(fake, gte)
	if err := junction.Create(link, filepath.Join(root, gte)); err != nil {
		t.Fatalf("建目录联接失败：%v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) }) // 只删联接本身，不碰目标

	t.Run("只点名 gte：bge 的请求说没点名，进程不退", func(t *testing.T) {
		p := startExe(t, exe, realArgs(ortPath, fake, "--models", gteID)...)
		p.waitReady()
		if code, out := p.req(http.MethodPost, "/v1/embeddings", map[string]any{"model": gteID, "input": "你好"}); code != 200 {
			t.Fatalf("gte：%d %s", code, out)
		}
		code, out := p.req(http.MethodPost, "/v1/rerank", map[string]any{"model": bgeID, "query": "q", "documents": []string{"d"}})
		if code != 404 || !strings.Contains(string(out), "registered but not selected") {
			t.Errorf("bge：想要说没点名的 404，实际 %d %s", code, out)
		}
	})

	t.Run("bge 放 --lazy-models：gte 200，bge ready=false 且请求 500", func(t *testing.T) {
		p := startExe(t, exe, realArgs(ortPath, fake, "--models", gteID, "--lazy-models", bgeID)...)
		p.waitReady()
		if code, out := p.req(http.MethodPost, "/v1/embeddings", map[string]any{"model": gteID, "input": "你好"}); code != 200 {
			t.Fatalf("gte：%d %s", code, out)
		}
		_, out := p.get("/v1/models")
		var ml struct {
			Data []struct {
				ID    string `json:"id"`
				Ready bool   `json:"ready"`
			} `json:"data"`
		}
		_ = json.Unmarshal(out, &ml)
		ready := map[string]bool{}
		for _, m := range ml.Data {
			ready[m.ID] = m.Ready
		}
		if !ready[gteID] || ready[bgeID] || len(ready) != 2 {
			t.Errorf("ready 该是 gte true、bge false：%s", out)
		}
		code, out := p.req(http.MethodPost, "/v1/rerank", map[string]any{"model": bgeID, "query": "q", "documents": []string{"d"}})
		if code != 500 || !strings.Contains(string(out), "找不到文件") || !strings.Contains(string(out), "bge-reranker-v2-m3/model_quantized.onnx") {
			t.Errorf("bge：想要 500 并写清哪个文件，实际 %d %s", code, out)
		}
		if strings.Contains(string(out), fake) {
			t.Errorf("响应里有模型根目录的绝对路径：%s", out)
		}
		select {
		case <-p.done:
			t.Fatalf("lazy 模型的问题让进程退出了：\n%s", p.out.String())
		default:
		}
	})

	t.Run("缺省（全部登记的）：bge 缺了就退出码 2", func(t *testing.T) {
		p := startExe(t, exe, realArgs(ortPath, fake)...)
		if code := p.wait(30 * time.Second); code != 2 {
			t.Fatalf("想要退出码 2，实际 %d\n%s", code, p.out.String())
		}
		first := strings.SplitN(p.out.String(), "\n", 2)[0]
		if !strings.HasPrefix(first, "[sumika-models] 错误：") || !strings.Contains(first, bgeID) {
			t.Errorf("第一行：%q", first)
		}
	})
}

// ---- 验收 8b（真模型）：一次 2048 token 的前向进行中，/health 照样答；看门狗 ----

func TestRealExeHealthNotBlockedAndWatchdog(t *testing.T) {
	ortPath, root := gates(t)
	exe := buildExe(t)

	t.Run("2048 token 的前向进行中 /health 3 秒内答", func(t *testing.T) {
		p := startExe(t, exe, realArgs(ortPath, root, "--models", gteID, "--run-watchdog-seconds", "0")...)
		p.waitReady()
		// 先暖一下，免得"加载"混进要量的前向
		p.req(http.MethodPost, "/v1/embeddings", map[string]any{"model": gteID, "input": "暖一下"})
		done := make(chan time.Duration, 1)
		go func() {
			t0 := time.Now()
			code, out := p.req(http.MethodPost, "/v1/embeddings", map[string]any{"model": gteID, "input": longText(2048)})
			if code != 200 {
				t.Errorf("长输入：%d %.200s", code, out)
			}
			done <- time.Since(t0)
		}()
		time.Sleep(700 * time.Millisecond) // 前向在跑
		var worst time.Duration
		var maxElapsed int64
		for i := 0; i < 5; i++ {
			t0 := time.Now()
			code, out := p.get("/health")
			if d := time.Since(t0); d > worst {
				worst = d
			}
			var h health
			_ = json.Unmarshal(out, &h)
			maxElapsed = max(maxElapsed, h.RunElapsedMs)
			if code != 200 {
				t.Fatalf("前向进行中 /health 是 %d", code)
			}
			time.Sleep(300 * time.Millisecond)
		}
		took := <-done
		t.Logf("2048 token 一行：%d 毫秒；其间 /health 最慢 %d 毫秒，run_elapsed_ms 最大看到 %d", took.Milliseconds(), worst.Milliseconds(), maxElapsed)
		if worst > 3*time.Second {
			t.Errorf("/health 最慢 %v，超过 Local ping 的 3 秒超时", worst)
		}
		if maxElapsed < 300 {
			t.Errorf("前向进行中 run_elapsed_ms 该大于 300，实际最大 %d", maxElapsed)
		}
		if h := p.health(); h.RunElapsedMs != 0 || h.LastOKUnix == 0 {
			t.Errorf("跑完以后 run_elapsed_ms 该回 0、last_ok_unix 该有值：%+v", h)
		}
	})

	t.Run("看门狗：--run-watchdog-seconds 1 遇上 2048 token 的前向，退出码 4", func(t *testing.T) {
		p := startExe(t, exe, realArgs(ortPath, root, "--models", gteID, "--run-watchdog-seconds", "1")...)
		p.waitReady()
		go func() {
			r, _ := http.NewRequest(http.MethodPost, "http://"+p.addr+"/v1/embeddings", bytes.NewReader(mustJSON(map[string]any{"model": gteID, "input": longText(2048)})))
			r.Header.Set("Authorization", "Bearer "+token)
			if resp, err := client.Do(r); err == nil {
				resp.Body.Close()
			}
		}()
		t0 := time.Now()
		if code := p.wait(30 * time.Second); code != 4 {
			t.Fatalf("想要退出码 4，实际 %d\n%s", code, p.out.String())
		}
		t.Logf("发出长请求到看门狗让进程退出：%d 毫秒", time.Since(t0).Milliseconds())
		lines := strings.Split(strings.TrimRight(p.out.String(), "\r\n"), "\n")
		if last := lines[len(lines)-1]; !strings.HasPrefix(last, "[sumika-models] 错误：前向超时") {
			t.Errorf("最后一行：%q", last)
		}
	})
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ---- 验收 11：内存 ----

func TestRealExeSoak(t *testing.T) {
	ortPath, root := gates(t)
	if os.Getenv("SUMIKA_TEST_SOAK") != "1" {
		t.Skip("缺环境变量：SUMIKA_TEST_SOAK=1（几分钟、吃 CPU，单独跑）")
	}
	exe := buildExe(t)
	p := startExe(t, exe, realArgs(ortPath, root, "--models", gteID, "--run-watchdog-seconds", "0")...)
	p.waitReady()
	embed := func(text string) {
		code, out := p.req(http.MethodPost, "/v1/embeddings", map[string]any{"model": gteID, "input": text})
		if code != 200 {
			t.Fatalf("嵌入：%d %.200s", code, out)
		}
	}
	embed("暖一下")
	base := p.health().RSSBytes
	t.Logf("① 加载 gte 并跑过第一句以后：%.0f MiB", mib(base))

	t0 := time.Now()
	embed(longText(2048))
	afterLong := p.health().RSSBytes
	t.Logf("② 一次 2048 token 的请求（%d 毫秒）之后：%.0f MiB（比①多 %.0f MiB）", time.Since(t0).Milliseconds(), mib(afterLong), mib(afterLong)-mib(base))
	if afterLong > base+50<<20 {
		t.Errorf("②该回到基线 + 50 MiB 以内：基线 %.0f MiB，现在 %.0f MiB", mib(base), mib(afterLong))
	}

	// ④ 预热 500 次变长请求（5～200 token），基线取预热之后
	t0 = time.Now()
	for i := 0; i < 500; i++ {
		n := 5 + (i*37)%196
		embed(fmt.Sprintf("预热%d号：", i) + strings.Repeat("字", n))
	}
	warm := p.health().RSSBytes
	t.Logf("④ 预热 500 次变长请求（%.0f 秒）之后：%.0f MiB", time.Since(t0).Seconds(), mib(warm))

	t0 = time.Now()
	for i := 0; i < 10000; i++ {
		embed(fmt.Sprintf("小请求%d号：今天天气%d，随便写点别的", i, i*7919%10007))
		if i%2500 == 2499 {
			t.Logf("  %d 次小请求以后：%.0f MiB", i+1, mib(p.health().RSSBytes))
		}
	}
	end := p.health().RSSBytes
	growth := (float64(end) - float64(warm)) / float64(warm) * 100
	t.Logf("④ 一万次小请求（%.0f 秒）之后：%.0f MiB，比预热之后多 %.2f%%", time.Since(t0).Seconds(), mib(end), growth)
	if growth >= 5 {
		t.Errorf("一万次小请求后增长 %.2f%%，门槛 < 5%%", growth)
	}
}

// ---- 复核补的：长输入连续跑，常驻内存要有平台，不能一直涨 ----

// 每次 2048 token 的嵌入会在 Go 堆上分配约 6 MiB（token_embeddings 的输出拷贝），回落靠 Go 的 GC 和后台归还。
// 复核看到的是连着 4 次每次涨 6 MiB、没有看到平台期；上面 TestRealExeSoak 的"10000 次小请求"和单次长请求对这种逐次涨法太宽。
// 这里连续 40 次互不相同的 2048 token 请求（不同的文本，免得被引擎的缓存挡住），每 5 次记一次常驻内存，要求后 30 次涨幅不超过 24 MiB。
// 要几分钟、吃 CPU，单独跑：SUMIKA_TEST_SOAK=1。
func TestRealExeLongInputSoak(t *testing.T) {
	ortPath, root := gates(t)
	if os.Getenv("SUMIKA_TEST_SOAK") != "1" {
		t.Skip("缺环境变量：SUMIKA_TEST_SOAK=1（几分钟、吃 CPU，单独跑）")
	}
	exe := buildExe(t)
	p := startExe(t, exe, realArgs(ortPath, root, "--models", gteID, "--run-watchdog-seconds", "0")...)
	p.waitReady()
	const total, settle = 40, 10
	var atSettle uint64
	t0 := time.Now()
	for i := 1; i <= total; i++ {
		code, out := p.req(http.MethodPost, "/v1/embeddings", map[string]any{"model": gteID, "input": fmt.Sprintf("第%d次长请求：", i) + longText(2048)})
		if code != 200 {
			t.Fatalf("第 %d 次：%d %.200s", i, code, out)
		}
		rss := p.health().RSSBytes
		if i == settle {
			atSettle = rss
		}
		if i%5 == 0 || i == 1 {
			t.Logf("第 %d 次 2048 token 请求之后：%.1f MiB（已用 %.0f 秒）", i, mib(rss), time.Since(t0).Seconds())
		}
		if i == total {
			growth := mib(rss) - mib(atSettle)
			t.Logf("第 %d 次到第 %d 次共 %d 次长请求，常驻内存 %.1f → %.1f MiB（涨 %.1f MiB）", settle, total, total-settle, mib(atSettle), mib(rss), growth)
			if growth > 24 {
				t.Errorf("第 %d 次之后还涨了 %.1f MiB（门槛 24 MiB）：没有平台期，是堆没还给系统还是真的泄漏要查", settle, growth)
			}
		}
	}
}
