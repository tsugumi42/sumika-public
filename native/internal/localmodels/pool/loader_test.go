package pool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/localmodels/ort"
	"sumika-native/internal/localmodels/tokenizer"
)

// ---- 假 ORT 运行时 ----

type fakeSess struct {
	inputs  []ort.TensorInfo
	outputs []ort.TensorInfo
	closed  int
}

func (s *fakeSess) Inputs() []ort.TensorInfo  { return s.inputs }
func (s *fakeSess) Outputs() []ort.TensorInfo { return s.outputs }
func (s *fakeSess) Close() error              { s.closed++; return nil }
func (s *fakeSess) Run([]ort.Int64Tensor, []string) (map[string]ort.FloatTensor, error) {
	return nil, errors.New("测试里不前向")
}

type fakeRuntime struct {
	mu       sync.Mutex
	paths    []string
	opts     []ort.Options
	sessions []*fakeSess
	newErr   func(path string) error
	badInput bool        // 给一个图里有不认识的输入的会话（NewEmbedder 会拒绝）
	onNew    func()      // NewSession 里额外做的事（取消 ctx 之类）
	kind     engine.Kind // 决定输出名
}

func (r *fakeRuntime) Version() string { return "fake" }
func (r *fakeRuntime) Close() error    { return nil }

func (r *fakeRuntime) NewSession(path string, o ort.Options) (ort.Session, error) {
	r.mu.Lock()
	r.paths = append(r.paths, path)
	r.opts = append(r.opts, o)
	r.mu.Unlock()
	if r.onNew != nil {
		r.onNew()
	}
	if r.newErr != nil {
		if err := r.newErr(path); err != nil {
			return nil, err
		}
	}
	s := &fakeSess{inputs: []ort.TensorInfo{
		{Name: "input_ids", Type: ort.TypeInt64, Shape: []int64{-1, -1}},
		{Name: "attention_mask", Type: ort.TypeInt64, Shape: []int64{-1, -1}},
	}}
	if r.badInput {
		s.inputs = append(s.inputs, ort.TensorInfo{Name: "mystery", Type: ort.TypeInt64})
	}
	out := "token_embeddings"
	if r.kind == engine.KindRerank {
		out = "logits"
	}
	s.outputs = []ort.TensorInfo{{Name: out, Type: ort.TypeFloat32}}
	r.mu.Lock()
	r.sessions = append(r.sessions, s)
	r.mu.Unlock()
	return s, nil
}

func (r *fakeRuntime) newCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

// fakeTokenizer 够 NewEmbedder / NewReranker 用就行，不会真的被调用。
type fakeTokenizer struct{}

func (fakeTokenizer) EncodeSingle(string, int) ([]int, error)       { return []int{0, 2}, nil }
func (fakeTokenizer) EncodePair(string, string, int) ([]int, error) { return []int{0, 2}, nil }
func (fakeTokenizer) Count(string) (int, error)                     { return 2, nil }

// ---- 模型目录 ----

const (
	onnxBytes = "fake-onnx-bytes-0123456789"
	tokBytes  = "fake-tokenizer-bytes"
)

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// modelRoot 建一个临时的模型根目录，里面有 m/x/model.onnx 和 m/x/tokenizer.json，返回根目录和对得上哈希的登记项。
func modelRoot(t *testing.T, kind engine.Kind) (string, engine.Spec) {
	t.Helper()
	root := t.TempDir()
	spec := testSpec("m", kind)
	spec.Dir = "_待分类/x" // 中文目录，和真实的一样
	spec.OnnxSHA256, spec.TokenizerSHA256 = sha(onnxBytes), sha(tokBytes)
	dir := filepath.Join(root, "_待分类", "x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"model.onnx": onnxBytes, "tokenizer.json": tokBytes} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, spec
}

func fakeLoaderOver(t *testing.T, root string, rt *fakeRuntime, maxLine int) *FileLoader {
	t.Helper()
	l, err := NewFileLoader(root, rt, ort.Options{IntraThreads: 3}, maxLine)
	if err != nil {
		t.Fatal(err)
	}
	l.newTokenizer = func(tokenizer.Spec, []byte) (engine.Tokenizer, error) { return fakeTokenizer{}, nil }
	return l
}

func TestFileLoaderLoads(t *testing.T) {
	for _, kind := range []engine.Kind{engine.KindEmbed, engine.KindRerank} {
		t.Run(string(kind), func(t *testing.T) {
			root, spec := modelRoot(t, kind)
			rt := &fakeRuntime{kind: kind}
			l := fakeLoaderOver(t, root, rt, 0)
			ld, err := l.Load(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if (kind == engine.KindEmbed) != (ld.Embedder != nil) || (kind == engine.KindRerank) != (ld.Reranker != nil) {
				t.Fatalf("种类对不上：%+v", ld)
			}
			if got := rt.paths; len(got) != 1 || got[0] != filepath.Join(root, "_待分类", "x", "model.onnx") {
				t.Fatalf("会话该按绝对路径开 onnx：%v", got)
			}
			if rt.opts[0].IntraThreads != 3 {
				t.Fatalf("会话选项没传下去：%+v", rt.opts[0])
			}
			if rt.sessions[0].closed != 0 {
				t.Fatal("还没关")
			}
			if err := ld.Close(); err != nil || rt.sessions[0].closed != 1 {
				t.Fatalf("Close 该关会话：%v closed=%d", err, rt.sessions[0].closed)
			}
		})
	}
}

// 错误会原样进 HTTP 500 的正文：不能带模型根目录的绝对路径。
func TestFileLoaderErrorsHideTheRoot(t *testing.T) {
	root, spec := modelRoot(t, engine.KindEmbed)
	rt := &fakeRuntime{kind: engine.KindEmbed}
	l := fakeLoaderOver(t, root, rt, 0)
	noRoot := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("应该出错")
		}
		if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), filepath.ToSlash(root)) {
			t.Fatalf("错误里带了绝对路径：%v", err)
		}
	}

	t.Run("onnx 不在", func(t *testing.T) {
		if err := os.Remove(filepath.Join(root, "_待分类", "x", "model.onnx")); err != nil {
			t.Fatal(err)
		}
		_, err := l.Load(context.Background(), spec)
		noRoot(t, err)
		if !strings.Contains(err.Error(), "找不到文件 _待分类/x/model.onnx") {
			t.Fatalf("该说缺的是哪个文件（相对路径）：%v", err)
		}
		if _, err := l.Estimate(spec); err == nil || !strings.Contains(err.Error(), "找不到文件 _待分类/x/model.onnx") {
			t.Fatalf("Estimate 也该说缺的是哪个：%v", err)
		}
		if rt.newCalls() != 0 {
			t.Fatal("文件都不在，不该碰 ORT")
		}
	})
	t.Run("tokenizer 不在", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		rt := &fakeRuntime{kind: engine.KindEmbed}
		l := fakeLoaderOver(t, root, rt, 0)
		_ = os.Remove(filepath.Join(root, "_待分类", "x", "tokenizer.json"))
		_, err := l.Load(context.Background(), spec)
		if err == nil || !strings.Contains(err.Error(), "找不到文件 _待分类/x/tokenizer.json") || strings.Contains(err.Error(), root) {
			t.Fatalf("%v", err)
		}
		if rt.newCalls() != 0 {
			t.Fatal("不该碰 ORT")
		}
	})
	t.Run("ORT 建会话失败，错误里的绝对路径被换掉", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		rt := &fakeRuntime{kind: engine.KindEmbed, newErr: func(path string) error {
			return fmt.Errorf("加载模型 %s 失败：Protobuf parsing failed", path)
		}}
		l := fakeLoaderOver(t, root, rt, 0)
		_, err := l.Load(context.Background(), spec)
		noRoot(t, err)
		if !strings.Contains(err.Error(), "<模型根目录>") || !strings.Contains(err.Error(), "Protobuf parsing failed") {
			t.Fatalf("该保留原因、只换掉根目录：%v", err)
		}
	})
}

func TestFileLoaderHashChecks(t *testing.T) {
	t.Run("onnx 哈希不对：不碰 ORT", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		rt := &fakeRuntime{kind: engine.KindEmbed}
		l := fakeLoaderOver(t, root, rt, 0)
		spec.OnnxSHA256 = sha("别的内容")
		_, err := l.Load(context.Background(), spec)
		if err == nil || !strings.Contains(err.Error(), "SHA-256 是 "+sha(onnxBytes)) || !strings.Contains(err.Error(), "登记的是 "+sha("别的内容")) {
			t.Fatalf("该同时写出实际和登记的哈希：%v", err)
		}
		if rt.newCalls() != 0 {
			t.Fatal("哈希不对一律拒绝加载，不碰 ORT")
		}
	})
	t.Run("tokenizer 哈希不对：真分词器拒绝，不碰 ORT", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		rt := &fakeRuntime{kind: engine.KindEmbed}
		l, err := NewFileLoader(root, rt, ort.Options{}, 0) // 不换假分词器：走真的 tokenizer.Load
		if err != nil {
			t.Fatal(err)
		}
		spec.TokenizerSHA256 = sha("别的内容")
		_, err = l.Load(context.Background(), spec)
		if err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Fatalf("%v", err)
		}
		if rt.newCalls() != 0 {
			t.Fatal("分词器没过就不该建会话")
		}
	})
	t.Run("哈希结果缓存：大小和修改时间没变就不再算", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		rt := &fakeRuntime{kind: engine.KindEmbed}
		l := fakeLoaderOver(t, root, rt, 0)
		hashed := 0
		l.hash = func(ctx context.Context, p string) (string, error) { hashed++; return hashFile(ctx, p) }

		for i := 0; i < 3; i++ {
			ld, err := l.Load(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			ld.Close()
		}
		if hashed != 1 {
			t.Fatalf("三次加载，onnx 该只算 1 次哈希：%d", hashed)
		}
		// 改了内容、大小变了、修改时间故意改回原来的：重算，且因为对不上被拒
		onnx := filepath.Join(root, "_待分类", "x", "model.onnx")
		info, err := os.Stat(onnx)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(onnx, []byte(onnxBytes+"X"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(onnx, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		if _, err := l.Load(context.Background(), spec); err == nil || hashed != 2 {
			t.Fatalf("文件变了该重新算并拒绝：err=%v hashed=%d", err, hashed)
		}
		// 内容改回去、修改时间变了：也重算，这次过
		if err := os.WriteFile(onnx, []byte(onnxBytes), 0o644); err != nil {
			t.Fatal(err)
		}
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(onnx, future, future); err != nil {
			t.Fatal(err)
		}
		ld, err := l.Load(context.Background(), spec)
		if err != nil || hashed != 3 {
			t.Fatalf("修改时间变了该重算：err=%v hashed=%d", err, hashed)
		}
		ld.Close()
	})
	t.Run("Verify 两个文件都核，缓存也共用", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		l := fakeLoaderOver(t, root, &fakeRuntime{}, 0)
		if _, err := l.Verify(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
		bad := spec
		bad.TokenizerSHA256 = sha("x")
		if _, err := l.Verify(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "tokenizer.json") {
			t.Fatalf("分词器哈希不对该被拒：%v", err)
		}
		bad = spec
		bad.OnnxSHA256 = sha("x")
		if _, err := l.Verify(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "model.onnx") {
			t.Fatalf("onnx 哈希不对该被拒：%v", err)
		}
		hashed := 0
		l.hash = func(ctx context.Context, p string) (string, error) { hashed++; return hashFile(ctx, p) }
		if _, err := l.Verify(context.Background(), spec); err != nil || hashed != 1 { // 只剩分词器要算
			t.Fatalf("Verify 过的 onnx，缓存该命中：err=%v hashed=%d", err, hashed)
		}
	})
}

func TestFileLoaderCleansUpOnFailure(t *testing.T) {
	t.Run("引擎不认识这张图：会话关掉", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		rt := &fakeRuntime{kind: engine.KindEmbed, badInput: true}
		l := fakeLoaderOver(t, root, rt, 0)
		if _, err := l.Load(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "mystery") {
			t.Fatalf("%v", err)
		}
		if len(rt.sessions) != 1 || rt.sessions[0].closed != 1 {
			t.Fatalf("建出来的会话该关掉：%+v", rt.sessions)
		}
	})
	t.Run("ctx 在建会话之前取消：不建", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		rt := &fakeRuntime{kind: engine.KindEmbed}
		l := fakeLoaderOver(t, root, rt, 0)
		ctx, cancel := context.WithCancel(context.Background())
		l.newTokenizer = func(tokenizer.Spec, []byte) (engine.Tokenizer, error) { cancel(); return fakeTokenizer{}, nil }
		if _, err := l.Load(ctx, spec); !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
		if rt.newCalls() != 0 {
			t.Fatal("池已经在关了，不该再建会话")
		}
	})
	t.Run("ctx 在建会话时取消：会话关掉，不留半成品", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		ctx, cancel := context.WithCancel(context.Background())
		rt := &fakeRuntime{kind: engine.KindEmbed, onNew: cancel}
		l := fakeLoaderOver(t, root, rt, 0)
		if _, err := l.Load(ctx, spec); err == nil {
			t.Fatal("应该出错")
		}
		if len(rt.sessions) != 1 || rt.sessions[0].closed != 1 {
			t.Fatalf("会话该关掉：%+v", rt.sessions)
		}
	})
	t.Run("没有 ORT 运行时", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		l, _ := NewFileLoader(root, nil, ort.Options{}, 0)
		if _, err := l.Load(context.Background(), spec); err == nil {
			t.Fatal("没有运行时该报错")
		}
	})
}

func TestFileLoaderCheckAndEstimate(t *testing.T) {
	root, spec := modelRoot(t, engine.KindEmbed)
	l := fakeLoaderOver(t, root, &fakeRuntime{}, 2048)

	if err := l.Check(spec); err != nil {
		t.Fatal(err)
	}
	spec.ActivationMiB, spec.TokenizerResidentMiB = 700, 22
	spec.MaxTokens = 8192
	got, err := l.Estimate(spec)
	if err != nil {
		t.Fatal(err)
	}
	if want := EstimateBytes(spec, int64(len(onnxBytes)), 2048); got != want {
		t.Fatalf("Estimate 该是 EstimateBytes(文件大小, 单行上限)：%d vs %d", got, want)
	}

	dir := filepath.Join(root, "_待分类", "x")
	_ = os.Remove(filepath.Join(dir, "model.onnx"))
	_ = os.Remove(filepath.Join(dir, "tokenizer.json"))
	err = l.Check(spec)
	if err == nil || !strings.Contains(err.Error(), "model.onnx") || !strings.Contains(err.Error(), "tokenizer.json") || strings.Contains(err.Error(), root) {
		t.Fatalf("Check 该一次列出两个都缺：%v", err)
	}
	// 目录冒充文件
	if err := os.Mkdir(filepath.Join(dir, "model.onnx"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Estimate(spec); err == nil || !strings.Contains(err.Error(), "是目录") {
		t.Fatalf("目录不是文件：%v", err)
	}
}

// 文件形状 × 入口：空文件、分词器位置上是个目录。四个入口（Estimate、Check、Verify、Load）各自该怎么样。
func TestFileLoaderOddFiles(t *testing.T) {
	t.Run("空的 onnx", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		rt := &fakeRuntime{kind: engine.KindEmbed}
		l := fakeLoaderOver(t, root, rt, 0)
		if err := os.WriteFile(filepath.Join(root, "_待分类", "x", "model.onnx"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := l.Check(spec); err != nil {
			t.Fatalf("Check 只看在不在，不看内容：%v", err)
		}
		if _, err := l.Estimate(spec); err != nil {
			t.Fatalf("Estimate 只读大小：%v", err)
		}
		if _, err := l.Verify(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Fatalf("Verify 该因哈希不对而拒绝：%v", err)
		}
		if _, err := l.Load(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Fatalf("Load 该因哈希不对而拒绝：%v", err)
		}
		if rt.newCalls() != 0 {
			t.Fatal("不该碰 ORT")
		}
	})
	t.Run("tokenizer.json 的位置是个目录", func(t *testing.T) {
		root, spec := modelRoot(t, engine.KindEmbed)
		rt := &fakeRuntime{kind: engine.KindEmbed}
		l := fakeLoaderOver(t, root, rt, 0)
		tok := filepath.Join(root, "_待分类", "x", "tokenizer.json")
		if err := os.Remove(tok); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(tok, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, err := range map[string]error{
			"Check":  l.Check(spec),
			"Verify": func() error { _, err := l.Verify(context.Background(), spec); return err }(),
			"Load":   func() error { _, err := l.Load(context.Background(), spec); return err }(),
		} {
			if err == nil || !strings.Contains(err.Error(), "tokenizer.json") || strings.Contains(err.Error(), root) {
				t.Errorf("%s：该拒绝且说出相对路径、不带根目录：%v", name, err)
			}
		}
		if rt.newCalls() != 0 {
			t.Fatal("不该碰 ORT")
		}
	})
}

func TestRowTokens(t *testing.T) {
	embed, rerank := embedSpec("e"), rerankSpec("r")
	embed.MaxTokens = 8192
	rerank.MaxTokens = 1024
	for _, c := range []struct {
		maxLine int
		spec    engine.Spec
		want    int
	}{
		{0, embed, 8192}, {2048, embed, 2048}, {99999, embed, 8192}, // 单行上限只能收紧
		{2048, rerank, 1024}, {0, rerank, 1024}, // 重排不受它管
	} {
		l, _ := NewFileLoader(t.TempDir(), nil, ort.Options{}, c.maxLine)
		if got := l.RowTokens(c.spec); got != c.want {
			t.Errorf("maxLine=%d %s：%d，该是 %d", c.maxLine, c.spec.Kind, got, c.want)
		}
	}
}

func TestNewFileLoaderNeedsAbsoluteRoot(t *testing.T) {
	if _, err := NewFileLoader(`models`, nil, ort.Options{}, 0); err == nil {
		t.Fatal("相对路径该被拒绝：模型根目录没有默认、不搜索（ADR-098）")
	}
}

// 池和真的 FileLoader 一起：单行上限放太宽、激活余量大到超过预算，立刻 503，不会真的去加载。
func TestPoolWithFileLoaderRefusesWhatCannotFit(t *testing.T) {
	root, spec := modelRoot(t, engine.KindEmbed)
	spec.MaxTokens, spec.ActivationMiB, spec.TokenizerResidentMiB = 8192, 700, 22
	rt := &fakeRuntime{kind: engine.KindEmbed}

	try := func(maxLine int) (*Lease, error) {
		l := fakeLoaderOver(t, root, rt, maxLine)
		p, err := New(Config{Models: entries(spec), Load: l.Load, Estimate: l.Estimate, LogOut: &syncBuffer{}, RSS: func() (uint64, error) { return 0, errors.New("x") }})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p.Acquire(context.Background(), spec.ID)
	}
	// 生产值 2048：700 + 22 MiB，装得下
	lease, err := try(2048)
	if err != nil {
		t.Fatalf("2048 该装得下：%v", err)
	}
	lease.Release()
	// 缺省 8192：700×16 = 11200 MiB，超过 4 GiB，立刻 503
	_, err = try(0)
	pe := asPoolError(t, err)
	if pe.Status != 503 || !strings.Contains(pe.Detail, "alone needs about 11") {
		t.Fatalf("8192 token 一行的激活余量该让它装不下：%+v", pe)
	}
	if rt.newCalls() != 1 {
		t.Fatalf("装不下的那次不该碰 ORT：会话共建了 %d 次", rt.newCalls())
	}
}
