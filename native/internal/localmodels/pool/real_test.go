package pool

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/localmodels/ort"
)

// 真 dll、真模型的测试，用环境变量门着，没设就 t.Skip 并写明缺什么（go test ./... 不依赖它们，也不依赖 Python）：
//
//	SUMIKA_TEST_ORT          官方发布包里的 onnxruntime.dll（scripts/fetch-ort.ts 取回的 vendor/ort/1.24.4/onnxruntime.dll）的绝对路径，哈希必须在 ort.KnownDLLs 里
//	SUMIKA_TEST_MODELS_ROOT  模型根目录（里面有 _待分类/gte-multilingual-base、_待分类/bge-reranker-v2-m3）
//	SUMIKA_TEST_MEASURE      再设成 1、gte 或 bge 才跑量内存余量的那一个（它要跑一行 1024～2048 token，几秒钟）
//
// 参考数据和 ort、engine 包共用同一份（../ort/testdata）。
const (
	envDLL     = "SUMIKA_TEST_ORT"
	envModels  = "SUMIKA_TEST_MODELS_ROOT"
	envMeasure = "SUMIKA_TEST_MEASURE"
	refDir     = "../ort/testdata"
)

var (
	sharedMu sync.Mutex
	sharedRT ort.Runtime
)

// 整个测试进程里对真 dll 只 Open 一次，最后关。
func TestMain(m *testing.M) {
	code := m.Run()
	sharedMu.Lock()
	if sharedRT != nil {
		sharedRT.Close()
	}
	sharedMu.Unlock()
	os.Exit(code)
}

func realEnv(t *testing.T) (dll, root string, rt ort.Runtime) {
	t.Helper()
	if dll = os.Getenv(envDLL); dll == "" {
		t.Skipf("没设 %s（onnxruntime.dll 的绝对路径），跳过真 dll 测试", envDLL)
	}
	if root = os.Getenv(envModels); root == "" {
		t.Skipf("没设 %s（模型根目录），跳过真模型测试", envModels)
	}
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedRT == nil {
		r, err := ort.Open(dll, ort.KnownDLLs, ort.PinnedVersion)
		if err != nil {
			t.Fatalf("加载 %s：%v", dll, err)
		}
		sharedRT = r
	}
	return dll, root, sharedRT
}

func builtin(t *testing.T, ids ...string) []engine.Spec {
	t.Helper()
	reg, err := engine.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var out []engine.Spec
	for _, id := range ids {
		s, ok := reg.Get(id)
		if !ok {
			t.Fatalf("内置登记表里没有 %s", id)
		}
		out = append(out, s)
	}
	return out
}

func readRefJSON(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(refDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s：%v", name, err)
	}
}

func readRefF32(t *testing.T, name string) []float32 {
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

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func rssMiB(t *testing.T) float64 {
	t.Helper()
	b, err := ProcessRSS()
	if err != nil {
		t.Skipf("读不到进程常驻内存：%v", err)
	}
	return float64(b) / float64(mib)
}

func acquireReal(t *testing.T, p *Pool, id string) *Lease {
	t.Helper()
	l, err := p.Acquire(context.Background(), id)
	if err != nil {
		t.Fatalf("Acquire(%s)：%v", id, err)
	}
	return l
}

func stateOf(t *testing.T, p *Pool, id string) ModelState {
	t.Helper()
	for _, m := range p.Snapshot().Models {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("Snapshot 里没有 %s", id)
	return ModelState{}
}

// 真模型走一遍池：真 FileLoader 加载、真 RSS 对账、两个模型同时装、卸载再加载、算出来的数对 Python 的参考。
func TestPoolRealGTEAndBGE(t *testing.T) {
	_, root, rt := realEnv(t)
	loader, err := NewFileLoader(root, rt, ort.Options{}, 2048) // 生产值：单行上限 2048
	if err != nil {
		t.Fatal(err)
	}
	specs := builtin(t, "gte-multilingual-base", "bge-reranker-v2-m3")
	logs := &syncBuffer{}
	p, err := New(Config{Models: entries(specs...), Load: loader.Load, Estimate: loader.Estimate, LogOut: logs})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// --- gte：冷加载（含 onnx 的完整哈希）---
	start := time.Now()
	lg := acquireReal(t, p, "gte-multilingual-base")
	cold := time.Since(start)
	st := stateOf(t, p, "gte-multilingual-base")
	t.Logf("GM4-真 gte 冷加载：Acquire 共 %v，load_ms=%d，其中 hash_ms=%d；估计 %d MiB，实测加载前后常驻内存 +%d MiB，记账 %d MiB",
		cold.Round(time.Millisecond), st.LoadMs, st.HashMs, st.EstimatedBytes/mib, st.MeasuredBytes/mib, st.ChargedBytes/mib)
	if st.HashMs <= 0 {
		t.Errorf("第一次加载该核 onnx 的哈希，hash_ms=%d", st.HashMs)
	}
	if st.MeasuredBytes < 100*mib {
		t.Errorf("加载前后常驻内存只涨了 %d MiB：对账没量到东西", st.MeasuredBytes/mib)
	}
	if st.MeasuredBytes*2 > st.EstimatedBytes*3 {
		t.Errorf("实测 %d MiB 超过估计 %d MiB 的 1.5 倍：登记表里的内存估计偏低", st.MeasuredBytes/mib, st.EstimatedBytes/mib)
	}
	if w := logs.lines("WARN"); len(w) != 0 {
		t.Errorf("不该有告警：%q", w)
	}

	var cases []struct{ Cat, Text string }
	readRefJSON(t, "cases.json", &cases)
	refVec := readRefF32(t, "ref-vecs-gte.f32")
	dim := specs[0].Dimension
	texts := []string{cases[0].Text, cases[1].Text, cases[2].Text}
	res, err := lg.Embedder().Embed(context.Background(), engine.EmbedRequest{Texts: texts})
	if err != nil {
		t.Fatal(err)
	}
	worst := 1.0
	for i, v := range res.Vectors {
		worst = math.Min(worst, cosine(v, refVec[i*dim:(i+1)*dim]))
	}
	t.Logf("GM4-真 gte 经池算出的 3 句向量对 Python 参考的余弦最低 %.9f", worst)
	if worst < 0.999999 {
		t.Errorf("经池加载的 gte 算出的向量对不上 Python 参考：余弦 %.9f", worst)
	}

	// --- bge：和 gte 同时装着（K=2）---
	lb := acquireReal(t, p, "bge-reranker-v2-m3")
	sb := stateOf(t, p, "bge-reranker-v2-m3")
	t.Logf("GM4-真 bge 冷加载：load_ms=%d，其中 hash_ms=%d；估计 %d MiB，实测 +%d MiB，记账 %d MiB",
		sb.LoadMs, sb.HashMs, sb.EstimatedBytes/mib, sb.MeasuredBytes/mib, sb.ChargedBytes/mib)
	if sb.MeasuredBytes*2 > sb.EstimatedBytes*3 {
		t.Errorf("bge 实测 %d MiB 超过估计 %d MiB 的 1.5 倍", sb.MeasuredBytes/mib, sb.EstimatedBytes/mib)
	}
	var pairs []struct {
		Q     string  `json:"q"`
		D     string  `json:"d"`
		Logit float64 `json:"logit"`
	}
	readRefJSON(t, "ref-logits-bge.json", &pairs)
	rr, err := lb.Reranker().Rerank(context.Background(), engine.RerankRequest{Query: pairs[0].Q, Documents: []string{pairs[0].D}})
	if err != nil {
		t.Fatal(err)
	}
	if d := math.Abs(rr.Results[0].Logit - pairs[0].Logit); d > 1e-5 {
		t.Errorf("经池加载的 bge 的 logit 对不上 Python 参考：差 %.3g", d)
	}
	snap := p.Snapshot()
	t.Logf("GM4-真 两个模型同时装着：记账 %d MiB（上限 %d MiB），进程常驻内存 %.0f MiB", snap.ResidentBytes/mib, snap.MaxBytes/mib, rssMiB(t))
	if snap.LoadedCount != 2 {
		t.Errorf("两个都该装着：%+v", snap)
	}

	// --- 卸载、再加载：onnx 的哈希缓存命中，加载更快 ---
	lg.Release()
	lb.Release()
	before := rssMiB(t)
	if ok, why := p.Unload("gte-multilingual-base"); !ok {
		t.Fatalf("卸 gte：%v %s", ok, why)
	}
	justAfter := rssMiB(t)
	time.Sleep(3 * time.Second) // ORT 释放会话慢，内存过几秒才掉（规格-本地模型服务第八节）
	later := rssMiB(t)
	t.Logf("GM4-真 卸载 gte：常驻内存 %.0f → 刚卸完 %.0f → 3 秒后 %.0f MiB（只记录，不断言：ORT 释放慢）", before, justAfter, later)
	lg2 := acquireReal(t, p, "gte-multilingual-base")
	again := stateOf(t, p, "gte-multilingual-base")
	t.Logf("GM4-真 gte 重新加载：load_ms=%d，hash_ms=%d（首次 %d）", again.LoadMs, again.HashMs, st.HashMs)
	if again.HashMs != 0 {
		t.Errorf("重新加载时 onnx 的哈希该命中缓存：hash_ms=%d", again.HashMs)
	}
	lg2.Release()

	// 日志格式
	for _, want := range []string{"LOADED model=gte-multilingual-base kind=embed dimension=768 load_ms=", "LOADED model=bge-reranker-v2-m3 kind=rerank dimension=- load_ms=", "UNLOADED model=gte-multilingual-base"} {
		if len(logs.lines(want)) == 0 {
			t.Errorf("日志里该有 %q：\n%s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), root) {
		t.Errorf("日志里不该有模型根目录的绝对路径")
	}
}

// K=1：装第二个会卸掉第一个，真会话关闭不崩，能再装回来。
func TestPoolRealEvictionWithOneSlot(t *testing.T) {
	_, root, rt := realEnv(t)
	loader, err := NewFileLoader(root, rt, ort.Options{}, 2048)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	p, err := New(Config{Models: entries(builtin(t, "gte-multilingual-base", "bge-reranker-v2-m3")...), Load: loader.Load, Estimate: loader.Estimate, MaxLoaded: 1, LogOut: logs})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	acquireReal(t, p, "gte-multilingual-base").Release()
	acquireReal(t, p, "bge-reranker-v2-m3").Release()    // 卸 gte
	acquireReal(t, p, "gte-multilingual-base").Release() // 卸 bge，再装 gte
	if n := len(logs.lines(" UNLOADED model=")); n != 2 {
		t.Fatalf("该有两行 UNLOADED：%d\n%s", n, logs.String())
	}
	if n := len(logs.lines(" LOADED model=")); n != 3 {
		t.Fatalf("该有三行 LOADED：%d\n%s", n, logs.String())
	}
	t.Logf("GM4-真 K=1 来回装三次：LOADED 3 行、UNLOADED 2 行，没崩")
}

// 量内存余量（登记表 ActivationMiB 的出处）：用池的生产配置加载，再跑模型自己最长的一行，看工作集峰值比加载完高多少。
// 要在新进程里单独跑这一个：峰值是进程到现在为止的，前面别的测试会把它顶高。
func TestRealMeasureActivation(t *testing.T) {
	which := os.Getenv(envMeasure)
	if which == "" {
		t.Skipf("没设 %s（1、gte 或 bge），跳过量内存余量（它要跑一行 1024～2048 token，几秒钟，单独跑）", envMeasure)
	}
	_, root, rt := realEnv(t)
	if peakRSS() == 0 {
		t.Skip("这个系统上读不到工作集峰值")
	}
	loader, err := NewFileLoader(root, rt, ort.Options{}, 2048)
	if err != nil {
		t.Fatal(err)
	}
	run := func(id string, fn func(l *Lease) (int, error)) {
		p, err := New(Config{Models: entries(builtin(t, id)...), Load: loader.Load, Estimate: loader.Estimate, LogOut: &syncBuffer{}})
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		l := acquireReal(t, p, id)
		defer l.Release()
		time.Sleep(time.Second)
		base := rssMiB(t)
		peak0 := float64(peakRSS()) / float64(mib) // 加载过程中的瞬时峰值（读文件、哈希、建会话），不是前向的
		// 前向期间每 2 毫秒采一次工作集，取最大值：进程的峰值计数器里混着加载时的瞬时峰值，分不开
		var sampled uint64
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			for {
				if b, err := ProcessRSS(); err == nil && b > sampled {
					sampled = b
				}
				select {
				case <-stop:
					return
				case <-time.After(2 * time.Millisecond):
				}
			}
		}()
		start := time.Now()
		tokens, err := fn(l)
		took := time.Since(start)
		close(stop)
		<-done
		if err != nil {
			t.Fatal(err)
		}
		fwd := float64(sampled) / float64(mib)
		time.Sleep(time.Second)
		t.Logf("GM4-量内存 %s：一行 %d token（前向 %v），加载完常驻 %.0f MiB，加载时瞬时峰值 %.0f MiB，前向期间采样峰值 %.0f MiB，激活余量 = %.0f MiB，之后回落到 %.0f MiB",
			id, tokens, took.Round(time.Millisecond), base, peak0, fwd, fwd-base, rssMiB(t))
	}
	if which == "1" || which == "bge" {
		run("bge-reranker-v2-m3", func(l *Lease) (int, error) {
			doc := strings.Repeat("可以用 go build 生成单个可执行文件，CGO_ENABLED=0 时不依赖 C 运行库。", 80) // 约 3840 字（上限 4096），分词后远超 1024，截到 1024 token
			r, err := l.Reranker().Rerank(context.Background(), engine.RerankRequest{Query: "怎么把Go程序打包成一个exe", Documents: []string{doc}})
			return r.Tokens, err
		})
	}
	if which == "1" || which == "gte" {
		run("gte-multilingual-base", func(l *Lease) (int, error) {
			text := strings.Repeat("今天晚上想吃火锅，你有什么推荐的店吗？我昨天把那个角色卡改了一下，感觉说话方式自然多了。", 120) // 截到 2048 token
			r, err := l.Embedder().Embed(context.Background(), engine.EmbedRequest{Texts: []string{text}})
			return r.Tokens, err
		})
	}
}
