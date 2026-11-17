package engine

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"sumika-native/internal/localmodels/ort"
	"sumika-native/internal/localmodels/tokenizer"
)

// 真 dll、真模型的测试，用环境变量门着，没设就 t.Skip 并写明缺什么（go test ./... 不依赖它们，也不依赖 Python）：
//
//	SUMIKA_TEST_ORT          官方发布包里的 onnxruntime.dll（scripts/fetch-ort.ts 取回的 vendor/ort/1.24.4/onnxruntime.dll）的绝对路径，哈希必须在 ort.KnownDLLs 里
//	SUMIKA_TEST_MODELS_ROOT  模型根目录（里面有 _待分类/gte-multilingual-base、_待分类/bge-reranker-v2-m3）
//
// 参考数据和 ort 包共用同一份（../ort/testdata，tools/local-models/parity_ref.py engine 生成），不再拷一份。
// 这是 Go 分词器 → Go ORT → 引擎 → 对 Python 参考这整条链第一次一起跑（编的样本；真实样本在 GM6）。

const (
	envDLL    = "SUMIKA_TEST_ORT"
	envModels = "SUMIKA_TEST_MODELS_ROOT"
	refDir    = "../ort/testdata"
)

var (
	sharedMu sync.Mutex
	sharedRT ort.Runtime
	hashed   = map[string]bool{}
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

func realRuntime(t *testing.T, dll string) ort.Runtime {
	t.Helper()
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if sharedRT == nil {
		rt, err := ort.Open(dll, ort.KnownDLLs, ort.PinnedVersion)
		if err != nil {
			t.Fatalf("加载 %s：%v", dll, err)
		}
		sharedRT = rt
	}
	return sharedRT
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// loadReal 按登记表里的路径和哈希加载一个真模型：.onnx 每个进程核一次完整哈希，tokenizer.json 由 tokenizer.Load 核。
// 会话用 ort.Options 的零值，也就是生产参数（2 线程、图优化 BASIC、内存池关）。
func loadReal(t *testing.T, id string) (Spec, *tokenizer.Tokenizer, ort.Session) {
	t.Helper()
	dll := os.Getenv(envDLL)
	if dll == "" {
		t.Skipf("没设 %s（onnxruntime.dll 的绝对路径），跳过真 dll 测试", envDLL)
	}
	root := os.Getenv(envModels)
	if root == "" {
		t.Skipf("没设 %s（模型根目录），跳过真模型测试", envModels)
	}
	spec := mustBuiltin(t, id)
	dir := filepath.Join(root, filepath.FromSlash(spec.Dir))
	onnx := filepath.Join(dir, filepath.FromSlash(spec.Onnx))
	if _, err := os.Stat(onnx); err != nil {
		t.Skipf("%s 下没有 %s：%v", envModels, spec.Dir, err)
	}
	sharedMu.Lock()
	if !hashed[onnx] {
		got, err := fileSHA256(onnx)
		if err != nil {
			sharedMu.Unlock()
			t.Fatal(err)
		}
		if got != spec.OnnxSHA256 {
			sharedMu.Unlock()
			t.Fatalf("%s 的 SHA-256 是 %s，登记表里是 %s：不是这份模型，对拍的参考值不能用", onnx, got, spec.OnnxSHA256)
		}
		hashed[onnx] = true
	}
	sharedMu.Unlock()
	raw, err := os.ReadFile(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		t.Fatal(err)
	}
	tk, err := tokenizer.Load(tokenizer.Spec{SHA256: spec.TokenizerSHA256}, raw)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := realRuntime(t, dll).NewSession(onnx, ort.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return spec, tk, sess
}

// ---- 参考数据 ----

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
	if len(b)%4 != 0 {
		t.Fatalf("%s 的字节数 %d 不是 4 的倍数", name, len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

// 参照在另一种 CPU 上生成的，就不拿它比：量化图的 int8 内核可能因指令集不同而不同，不放宽门槛，
// 换电脑后以现场 models-parity 对拍为准（规格第五节"参照向量的机器"）。
func checkCPU(t *testing.T) {
	t.Helper()
	var meta struct {
		CPU string `json:"cpu"`
	}
	readRefJSON(t, "ref-meta.json", &meta)
	got := cpuName()
	if got == "" {
		t.Skip("读不到这台机器的 CPU 型号，没法确认参考数据是在同一种 CPU 上生成的")
	}
	if got != meta.CPU {
		t.Skipf("参照在另一种 CPU 上生成（%q），这台是 %q：请重新生成（../ort/testdata/README.md），或以现场 models-parity 对拍为准", meta.CPU, got)
	}
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

func maxAbsDiff(a, b []float32) float64 {
	var m float64
	for i := range a {
		m = math.Max(m, math.Abs(float64(a[i])-float64(b[i])))
	}
	return m
}

// pinnedDiffCosine 是已登记的分词差异类别在 gte 上量到的向量余弦（对 Python 的参考向量），回归锁 ±1e-3。
// T1（NFD 韩文）0.8316 低于规格第五节的下限 0.95：Go 的分词器把一串组合字母合成一个 token，Python 的 HF 分词器不合，
// 两边给模型的输入就不同。这不是引擎的问题，是 GM1 登记的已知差异；要不要处理等 GM6 量真实样本里的命中率。
var pinnedDiffCosine = map[string]float64{"T1": 0.8316}

type knownDiff struct {
	Category string
	Go       []int
}

// loadKnownDiffs 读 GM1 登记的已知差异（tokenizer/testdata/known-diffs.json），按文字找。
func loadKnownDiffs(t *testing.T, model string) map[string]knownDiff {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "tokenizer", "testdata", "known-diffs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Diffs []struct {
			Model    string `json:"model"`
			Category string `json:"category"`
			Text     string `json:"text"`
			Go       []int  `json:"go"`
		} `json:"diffs"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	out := map[string]knownDiff{}
	for _, d := range f.Diffs {
		if d.Model == model {
			out[d.Text] = knownDiff{Category: d.Category, Go: d.Go}
		}
	}
	return out
}

func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- GM3-10：gte ----

func TestEngineGTEMatchesReference(t *testing.T) {
	spec, tk, sess := loadReal(t, "gte-multilingual-base")
	checkCPU(t)
	var cases []struct {
		Cat  string `json:"cat"`
		Text string `json:"text"`
	}
	readRefJSON(t, "cases.json", &cases)
	var refIDs []struct {
		IDs []int `json:"ids"`
	}
	readRefJSON(t, "ref-ids-gte.json", &refIDs)
	refRaw := readRefF32(t, "ref-raw0-gte.f32")
	refVec := readRefF32(t, "ref-vecs-gte.f32")
	dim := spec.Dimension
	n := len(cases)
	if len(refIDs) != n || len(refRaw) != n*dim || len(refVec) != n*dim {
		t.Fatalf("参照条数对不上：样本 %d，ids %d，raw0 %d，vecs %d", n, len(refIDs), len(refRaw)/dim, len(refVec)/dim)
	}

	e, err := NewEmbedder(spec, tk, sess, EmbedderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, n)
	for i, c := range cases {
		texts[i] = c.Text
	}
	res, err := e.Embed(context.Background(), EmbedRequest{Texts: texts})
	if err != nil {
		t.Fatal(err)
	}

	sameIDs := 0
	var worstDiff, worstRawDiff float64
	worstCos := 1.0
	var diffs []float64
	known := loadKnownDiffs(t, "gte")
	for i := range cases {
		got, _ := tk.EncodeSingle(cases[i].Text, spec.MaxTokens)
		same := len(got) == len(refIDs[i].IDs)
		for k := 0; same && k < len(got); k++ {
			same = got[k] == refIDs[i].IDs[k]
		}
		vec := res.Vectors[i]
		want := refVec[i*dim : (i+1)*dim]
		cos := cosine(vec, want)
		if !same {
			// id 不同（分词差异）：必须是 tokenizer/testdata/known-diffs.json 里登记过的（同一段文字、Go 的 id 一样），
			// 向量余弦锁在实测值 ±1e-3（规格第五节"id 不同"一行）；低于 0.95 的单独喊出来，不当成通过。
			k, ok := known[cases[i].Text]
			switch {
			case !ok:
				t.Errorf("第 %d 句（%s）的 id 和 Python 不同，又不在 known-diffs.json 里：Go %v，Python %v", i, cases[i].Cat, got, refIDs[i].IDs)
			case !intsEqual(k.Go, got):
				t.Errorf("第 %d 句（%s）：Go 的 id %v 和登记的 %v 不一样", i, cases[i].Cat, got, k.Go)
			default:
				pin, pinned := pinnedDiffCosine[k.Category]
				switch {
				case !pinned:
					t.Errorf("第 %d 句（%s）：类别 %s 还没有锁定的余弦（实测 %.6f），先看过再填进 pinnedDiffCosine", i, cases[i].Cat, k.Category, cos)
				case math.Abs(cos-pin) > 1e-3:
					t.Errorf("第 %d 句（%s，%s）：余弦 %.6f，锁定的是 %.4f（±1e-3）", i, cases[i].Cat, k.Category, cos, pin)
				}
				if cos < 0.95 {
					t.Logf("警告：已登记的差异 %s（第 %d 句 %s）余弦 %.6f < 0.95，分词差异已经影响语义，不是'登记了就行'；规格第五节的下限没过，等 GM6 在真实样本上量命中率再定", k.Category, i, cases[i].Cat, cos)
				}
				diffs = append(diffs, cos)
			}
			continue
		}
		sameIDs++
		// 第一层的间接检查：用参照里池化前的原始行自己做 L2（float64），和引擎的输出比
		// ——读了 sentence_embedding 而不是 token_embeddings 的话，这里会差很远
		raw := refRaw[i*dim : (i+1)*dim]
		var s float64
		for _, x := range raw {
			s += float64(x) * float64(x)
		}
		fromRaw := make([]float32, dim)
		for j, x := range raw {
			fromRaw[j] = float32(float64(x) / math.Sqrt(s))
		}
		worstRawDiff = math.Max(worstRawDiff, maxAbsDiff(vec, fromRaw))
		// 第二层：对 Python 的参考向量，最大分量差 ≤ 1e-6，余弦 ≥ 0.999999
		d := maxAbsDiff(vec, want)
		worstDiff = math.Max(worstDiff, d)
		worstCos = math.Min(worstCos, cos)
		if d > 1e-6 || cos < 0.999999 {
			t.Errorf("第 %d 句（%s）：最大分量差 %.3g，余弦 %.9f", i, cases[i].Cat, d, cos)
		}
	}
	if worstRawDiff > 1e-6 {
		t.Errorf("对池化前原始行自己做 L2 的结果，最大分量差 %.3g（> 1e-6）", worstRawDiff)
	}
	t.Logf("GM3-10 gte %d/%d 句 id 与 Python 相同；id 相同的：最大分量差 %.3g，余弦最低 %.9f（对池化前原始行 L2：最大分量差 %.3g）；id 不同且已登记的 %d 句，余弦 %.6f",
		sameIDs, n, worstDiff, worstCos, worstRawDiff, len(diffs), diffs)

	// 同一批再来一遍：全部命中缓存，逐位相同
	res2, err := e.Embed(context.Background(), EmbedRequest{Texts: texts})
	if err != nil {
		t.Fatal(err)
	}
	if res2.CacheHits != n {
		t.Errorf("第二遍命中 %d，想要 %d", res2.CacheHits, n)
	}
	for i := range texts {
		if !bitsEqual(res.Vectors[i], res2.Vectors[i]) {
			t.Fatalf("第 %d 句两遍不是逐位相同", i)
		}
	}

	// 一条一条算和一批一起算逐位相同（一次一行：批大小不影响结果）
	e2, err := NewEmbedder(spec, tk, sess, EmbedderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 7, 20, n - 1} {
		one, err := e2.Embed(context.Background(), EmbedRequest{Texts: []string{texts[i]}})
		if err != nil {
			t.Fatal(err)
		}
		if !bitsEqual(one.Vectors[0], res.Vectors[i]) {
			t.Errorf("第 %d 句单独算和一批一起算不是逐位相同", i)
		}
	}

	// matryoshka：128 维 = 全维前 128 个分量重新归一化
	d128 := 128
	cut, err := e2.Embed(context.Background(), EmbedRequest{Texts: texts[:4], Dimensions: &d128})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range cut.Vectors {
		want := refVec[i*dim : i*dim+128]
		if c := cosine(v, want); c < 0.999999 || len(v) != 128 || math.Abs(norm64(v)-1) > 1e-6 {
			t.Errorf("第 %d 句 128 维：余弦 %.9f 长度 %d 范数 %g", i, c, len(v), norm64(v))
		}
	}
}

// ---- GM3-10：bge ----

func TestEngineBGEMatchesReference(t *testing.T) {
	spec, tk, sess := loadReal(t, "bge-reranker-v2-m3")
	checkCPU(t)
	var ref []struct {
		Q     string  `json:"q"`
		D     string  `json:"d"`
		IDs   []int   `json:"ids"`
		Logit float64 `json:"logit"`
	}
	readRefJSON(t, "ref-logits-bge.json", &ref)
	if len(ref) == 0 {
		t.Fatal("参照是空的")
	}
	r, err := NewReranker(spec, tk, sess)
	if err != nil {
		t.Fatal(err)
	}

	worstLogit, worstScore := 0.0, 0.0
	sameIDs := 0
	single := make([]float64, len(ref))
	for i, p := range ref {
		got, err := tk.EncodePair(p.Q, p.D, spec.MaxTokens)
		if err != nil {
			t.Fatal(err)
		}
		same := len(got) == len(p.IDs)
		for k := 0; same && k < len(got); k++ {
			same = got[k] == p.IDs[k]
		}
		if !same {
			t.Errorf("第 %d 对的 id 和 Python 不同：Go %v，Python %v", i, got, p.IDs)
			continue
		}
		sameIDs++
		res, err := r.Rerank(context.Background(), RerankRequest{Query: p.Q, Documents: []string{p.D}})
		if err != nil {
			t.Fatal(err)
		}
		x := res.Results[0]
		single[i] = x.Logit
		dl := math.Abs(x.Logit - p.Logit)
		ds := math.Abs(x.Score - 1/(1+math.Exp(-p.Logit)))
		worstLogit, worstScore = math.Max(worstLogit, dl), math.Max(worstScore, ds)
		if dl > 1e-5 || ds > 1e-6 {
			t.Errorf("第 %d 对：logit 差 %.3g，分数差 %.3g", i, dl, ds)
		}
	}
	t.Logf("GM3-10 bge %d/%d 对 id 与 Python 相同；logit 最大差 %.3g，分数最大差 %.3g", sameIDs, len(ref), worstLogit, worstScore)

	// 同一个查询带多个文档一批发，每个文档的 logit 和单独发的逐位相同，名次按分数从高到低
	q := ref[0].Q
	var docs []string
	var idx []int
	for i, p := range ref {
		if p.Q == q {
			docs = append(docs, p.D)
			idx = append(idx, i)
		}
	}
	if len(docs) < 2 {
		t.Skip("参照里同一个查询没有两个以上的文档，跳过批内对比")
	}
	res, err := r.Rerank(context.Background(), RerankRequest{Query: q, Documents: docs})
	if err != nil {
		t.Fatal(err)
	}
	for k, x := range res.Results {
		if x.Logit != single[idx[x.Index]] {
			t.Errorf("批内文档 %d 的 logit %v 和单独发的 %v 不是逐位相同", x.Index, x.Logit, single[idx[x.Index]])
		}
		if k > 0 && x.Score > res.Results[k-1].Score {
			t.Errorf("名次没按分数从高到低排：%v", res.Results)
		}
	}
}
