package ort

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// 这个文件里的测试都不需要真 dll、真模型，也不读环境变量：合成文件加假加载器。
// 真 dll、真模型的测试在 real_test.go，用 SUMIKA_TEST_ORT / SUMIKA_TEST_MODELS_ROOT 门着。

// ---- 假的加载器、引擎和会话 ----

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type fakeEngine struct {
	rec       *recorder
	version   string
	providers []string
	opts      []Options
	sessions  []*fakeRaw
	newErr    error
	closeN    int
	extra     []TensorInfo // 新会话除了 input_ids、attention_mask 以外还声明的输入
}

func (f *fakeEngine) Version() string     { return f.version }
func (f *fakeEngine) Providers() []string { return f.providers }
func (f *fakeEngine) Close() {
	f.closeN++
	f.rec.add("engine.Close")
}

func (f *fakeEngine) NewSession(modelPath string, o Options) (rawSession, error) {
	if f.newErr != nil {
		return nil, f.newErr
	}
	f.opts = append(f.opts, o)
	raw := &fakeRaw{
		rec: f.rec,
		inputs: []TensorInfo{
			{Name: "input_ids", Type: TypeInt64, Shape: []int64{-1, -1}},
			{Name: "attention_mask", Type: TypeInt64, Shape: []int64{-1, -1}},
		},
		outputs: []TensorInfo{
			{Name: "token_embeddings", Type: TypeFloat32, Shape: []int64{-1, -1, 4}},
			{Name: "sentence_embedding", Type: TypeFloat32, Shape: []int64{-1, 4}},
		},
	}
	raw.inputs = append(raw.inputs, f.extra...)
	f.sessions = append(f.sessions, raw)
	return raw, nil
}

type fakeRaw struct {
	rec     *recorder
	inputs  []TensorInfo
	outputs []TensorInfo
	run     func(inputs []Int64Tensor, outputs []string) (map[string]FloatTensor, error)

	inRun, maxInRun atomic.Int32
	runs            atomic.Int32
	closeN          atomic.Int32
}

func (f *fakeRaw) Inputs() []TensorInfo  { return f.inputs }
func (f *fakeRaw) Outputs() []TensorInfo { return f.outputs }
func (f *fakeRaw) Close() {
	f.closeN.Add(1)
	f.rec.add("session.Close")
}

func (f *fakeRaw) Run(inputs []Int64Tensor, outputs []string) (map[string]FloatTensor, error) {
	n := f.inRun.Add(1)
	defer f.inRun.Add(-1)
	for {
		m := f.maxInRun.Load()
		if n <= m || f.maxInRun.CompareAndSwap(m, n) {
			break
		}
	}
	f.runs.Add(1)
	if f.run != nil {
		return f.run(inputs, outputs)
	}
	return map[string]FloatTensor{outputs[0]: {Shape: []int64{1}, Data: []float32{1}}}, nil
}

// fakeLoader 返回一个计数的加载函数：calls 是被调用的次数，paths 是它收到的路径。
type fakeLoader struct {
	eng   *fakeEngine
	err   error
	calls int
	paths []string
}

func (l *fakeLoader) load(path string) (engine, error) {
	l.calls++
	l.paths = append(l.paths, path)
	if l.err != nil {
		return nil, l.err
	}
	return l.eng, nil
}

func newFake(version string) (*fakeLoader, *fakeEngine, *recorder) {
	rec := &recorder{}
	eng := &fakeEngine{rec: rec, version: version, providers: []string{"CPUExecutionProvider"}}
	return &fakeLoader{eng: eng}, eng, rec
}

// writeFile 在临时目录写一个文件，返回绝对路径和它的 SHA-256。
func writeFile(t *testing.T, name string, content []byte) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return p, hex.EncodeToString(sum[:])
}

func knownFor(version, sum string) []KnownDLL {
	return []KnownDLL{{Version: version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, SHA256: sum, Source: "测试"}}
}

// ---- 验收 1：三道关，前两道在加载之前 ----

func TestOpenRejectsRelativePath(t *testing.T) {
	for _, p := range []string{"", "onnxruntime.dll", "./lib/onnxruntime.dll", `lib\onnxruntime.dll`, `..\onnxruntime.dll`, `C:onnxruntime.dll`, `\onnxruntime.dll`} {
		ld, _, _ := newFake("1.24.4")
		h := &holder{}
		_, err := h.open(p, nil, PinnedVersion, ld.load)
		if !errors.Is(err, ErrRelativePath) {
			t.Errorf("路径 %q：err = %v，想要 ErrRelativePath", p, err)
		}
		if ld.calls != 0 {
			t.Errorf("路径 %q：加载函数被调用了 %d 次，必须是 0", p, ld.calls)
		}
	}
}

func TestOpenMissingFile(t *testing.T) {
	ld, _, _ := newFake("1.24.4")
	ld.err = &os.PathError{Op: "x", Path: "x", Err: syscall.Errno(126)} // 万一被调用，就会给出 VC++ 提示；不许
	missing := filepath.Join(t.TempDir(), "nope", "onnxruntime.dll")
	_, err := (&holder{}).open(missing, KnownDLLs, PinnedVersion, ld.load)
	if !errors.Is(err, ErrFileNotFound) || !strings.Contains(err.Error(), "找不到文件") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "VC++") {
		t.Errorf("文件不存在时不该提 VC++：%v", err)
	}
	if ld.calls != 0 {
		t.Errorf("加载函数被调用了 %d 次", ld.calls)
	}
	// 目录不是文件
	_, err = (&holder{}).open(t.TempDir(), KnownDLLs, PinnedVersion, ld.load)
	if !errors.Is(err, ErrFileNotFound) || ld.calls != 0 {
		t.Errorf("目录：err = %v calls = %d", err, ld.calls)
	}
}

func TestOpenRejectsUnknownDLL(t *testing.T) {
	if testing.Short() {
		t.Skip("缺 DLL 的报错要真的起 onnxruntime，放在每日整套")
	}
	good := []byte("假装是一份 onnxruntime.dll 的内容，字节要和清单里的对得上")
	_, goodSum := writeFile(t, "good.dll", good)
	known := knownFor("1.24.4", goodSum)

	// 同样内容放行（加载函数收到的就是这个路径）
	goodPath, _ := writeFile(t, "onnxruntime.dll", good)
	ld, _, _ := newFake("1.24.4")
	h := &holder{}
	rt, err := h.open(goodPath, known, PinnedVersion, ld.load)
	if err != nil {
		t.Fatalf("清单里有的文件被拒绝：%v", err)
	}
	if ld.calls != 1 || ld.paths[0] != filepath.Clean(goodPath) {
		t.Errorf("calls=%d paths=%v", ld.calls, ld.paths)
	}
	rt.Close()

	// 改一个字节：不在清单里，加载函数一次都不许调，错误里有路径和实际哈希
	bad := append([]byte(nil), good...)
	bad[len(bad)-1] ^= 1
	badPath, badSum := writeFile(t, "onnxruntime.dll", bad)
	ld2, _, _ := newFake("1.24.4")
	_, err = (&holder{}).open(badPath, known, PinnedVersion, ld2.load)
	if !errors.Is(err, ErrUnknownDLL) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), badPath) || !strings.Contains(err.Error(), badSum) {
		t.Errorf("错误里要有路径和实际哈希：%v", err)
	}
	if ld2.calls != 0 {
		t.Errorf("哈希不在清单里，加载函数被调用了 %d 次", ld2.calls)
	}

	// 清单为空也是拒绝（没有"放行未知"的口子）
	_, err = (&holder{}).open(goodPath, nil, PinnedVersion, ld2.load)
	if !errors.Is(err, ErrUnknownDLL) || ld2.calls != 0 {
		t.Errorf("空清单：err = %v calls = %d", err, ld2.calls)
	}
}

func TestOpenRejectsKnownForOtherPlatform(t *testing.T) {
	p, sum := writeFile(t, "onnxruntime.dll", []byte("abc"))
	known := []KnownDLL{{Version: "1.24.4", GOOS: "plan9", GOARCH: "mips", SHA256: sum}}
	ld, _, _ := newFake("1.24.4")
	_, err := (&holder{}).open(p, known, PinnedVersion, ld.load)
	if !errors.Is(err, ErrUnknownDLL) || ld.calls != 0 {
		t.Fatalf("err = %v calls = %d", err, ld.calls)
	}
	// 清单里的哈希写成大写也认
	ld2, _, _ := newFake("1.24.4")
	rt, err := (&holder{}).open(p, knownFor("1.24.4", strings.ToUpper(sum)), PinnedVersion, ld2.load)
	if err != nil {
		t.Fatalf("大写哈希：%v", err)
	}
	rt.Close()
}

// 唯一允许在加载之后拒绝的一道：清单里哈希对、版本也对，但运行时自己报的版本不是钉的
func TestOpenRejectsWrongVersion(t *testing.T) {
	p, sum := writeFile(t, "onnxruntime.dll", []byte("abc"))
	ld, eng, rec := newFake("1.17.1")
	_, err := (&holder{}).open(p, knownFor("1.24.4", sum), PinnedVersion, ld.load)
	if !errors.Is(err, ErrWrongVersion) || !strings.Contains(err.Error(), "1.17.1") {
		t.Fatalf("err = %v", err)
	}
	if ld.calls != 1 {
		t.Errorf("加载函数被调用 %d 次，想要正好 1 次", ld.calls)
	}
	if eng.closeN != 1 {
		t.Errorf("被拒绝的引擎要销毁，Close 了 %d 次（%v）", eng.closeN, rec.list())
	}

	// 只差一个补丁号也不行：不是"范围内都行"
	ld2, _, _ := newFake("1.24.5")
	_, err = (&holder{}).open(p, knownFor("1.24.4", sum), PinnedVersion, ld2.load)
	if !errors.Is(err, ErrWrongVersion) {
		t.Errorf("1.24.5：err = %v", err)
	}
}

// 清单里这份哈希登记的版本就不是钉的版本：加载之前就拒绝
func TestOpenRejectsKnownVersionMismatch(t *testing.T) {
	p, sum := writeFile(t, "onnxruntime.dll", []byte("abc"))
	ld, _, _ := newFake("1.31.1")
	_, err := (&holder{}).open(p, knownFor("1.31.1", sum), PinnedVersion, ld.load)
	if !errors.Is(err, ErrWrongVersion) || ld.calls != 0 {
		t.Fatalf("err = %v calls = %d", err, ld.calls)
	}
}

func TestOpenRequiresCPUProvider(t *testing.T) {
	p, sum := writeFile(t, "onnxruntime.dll", []byte("abc"))
	ld, eng, _ := newFake("1.24.4")
	eng.providers = []string{"CUDAExecutionProvider"}
	_, err := (&holder{}).open(p, knownFor("1.24.4", sum), PinnedVersion, ld.load)
	if !errors.Is(err, ErrNoCPUProvider) || eng.closeN != 1 {
		t.Fatalf("err = %v closeN = %d", err, eng.closeN)
	}
}

// ---- 验收 2：Windows 自带的那份 ----

// 被拦在第 2 道（哈希不在清单里），不是被版本门拦的，也不会拿它去推理。
// 改坏检查：把第 2 道拿掉，假加载器会收到调用并报 1.17，这里的 calls 断言和原因断言都会红。
func TestOpenRejectsSystemDLL(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("只在 Windows 上有这个文件")
	}
	sys := filepath.Join(os.Getenv("SystemRoot"), "System32", "onnxruntime.dll")
	if _, err := os.Stat(sys); err != nil {
		t.Skipf("这台机器上没有 %s", sys)
	}
	ld, _, _ := newFake("1.17.260914") // 如果它真被加载了，版本门会说话；但我们要的是它根本没被加载
	_, err := (&holder{}).open(sys, KnownDLLs, PinnedVersion, ld.load)
	if !errors.Is(err, ErrUnknownDLL) {
		t.Fatalf("要被哈希门（ErrUnknownDLL）拦下，实际 err = %v", err)
	}
	if errors.Is(err, ErrWrongVersion) {
		t.Errorf("不该是版本门拦的：%v", err)
	}
	if ld.calls != 0 {
		t.Errorf("系统自带的 dll 不该被加载，加载函数被调用了 %d 次", ld.calls)
	}
}

// ---- 验收 3：加载失败的翻译 ----

// 现实里不是 dll 的文件会先被哈希门拦下，到不了 193；这里把它自己的哈希塞进清单，用真加载器走到 LoadLibrary。
// 文本文件不是 PE，LoadLibrary 在 193 就失败，DllMain 不会执行，安全。
func TestOpenNotADLL(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("193 是 Windows 的错误码")
	}
	p, sum := writeFile(t, "onnxruntime.dll", []byte("这是一个文本文件，不是 PE\r\n"))
	_, err := (&holder{}).open(p, knownFor("1.24.4", sum), PinnedVersion, loadBackend)
	if !errors.Is(err, ErrNotADLL) || !strings.Contains(err.Error(), "不是有效的 dll") || !strings.Contains(err.Error(), p) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "VC++") {
		t.Errorf("不是 dll 不该提 VC++：%v", err)
	}
	// 同一个文件哈希不在清单里，就根本到不了这一步
	ld, _, _ := newFake("1.24.4")
	_, err = (&holder{}).open(p, nil, PinnedVersion, ld.load)
	if !errors.Is(err, ErrUnknownDLL) || ld.calls != 0 {
		t.Errorf("err = %v calls = %d", err, ld.calls)
	}
}

// 193、126 穿过 younland 的两层 %w 也要认得出来；126 只在"文件存在"时才给 VC++ 提示（文件不存在的情形在 TestOpenMissingFile）
func TestOpenTranslatesLoadErrors(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("错误码是 Windows 的")
	}
	p, sum := writeFile(t, "onnxruntime.dll", []byte("abc"))
	known := knownFor("1.24.4", sum)
	wrap := func(e error) error { // 照 younland 的包法：sys.LoadLibrary 一层，NewEngine 一层
		return errors.Join(errors.New("failed to load onnxruntime library"), e)
	}
	cases := []struct {
		name string
		err  error
		is   error
		want string
		not  string
	}{
		{"193", wrap(wrapPct(syscall.Errno(193))), ErrNotADLL, "不是有效的 dll", "VC++"},
		{"126", wrap(wrapPct(syscall.Errno(126))), ErrMissingDependency, "VC++", ""},
	}
	for _, c := range cases {
		ld, _, _ := newFake("1.24.4")
		ld.err = c.err
		_, err := (&holder{}).open(p, known, PinnedVersion, ld.load)
		if !errors.Is(err, c.is) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：err = %v", c.name, err)
		}
		if c.not != "" && strings.Contains(err.Error(), c.not) {
			t.Errorf("%s：不该出现 %q：%v", c.name, c.not, err)
		}
		if ld.calls != 1 {
			t.Errorf("%s：calls = %d", c.name, ld.calls)
		}
	}
	// 别的错误：原样带出来，不套 VC++ 提示
	ld, _, _ := newFake("1.24.4")
	ld.err = errors.New("别的原因")
	_, err := (&holder{}).open(p, known, PinnedVersion, ld.load)
	if err == nil || !strings.Contains(err.Error(), "别的原因") || strings.Contains(err.Error(), "VC++") || errors.Is(err, ErrNotADLL) {
		t.Errorf("别的错误：%v", err)
	}
}

type pctErr struct{ e error }

func wrapPct(e error) error     { return &pctErr{e} }
func (p *pctErr) Error() string { return "failed to load dll: " + p.e.Error() }
func (p *pctErr) Unwrap() error { return p.e }

// ---- 单例 ----

func TestOpenSingleton(t *testing.T) {
	p, sum := writeFile(t, "onnxruntime.dll", []byte("abc"))
	known := knownFor("1.24.4", sum)
	ld, _, _ := newFake("1.24.4")
	h := &holder{}
	a, err := h.open(p, known, PinnedVersion, ld.load)
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.open(strings.ToUpper(p), known, PinnedVersion, ld.load) // Windows 路径不分大小写
	if runtime.GOOS == "windows" {
		if err != nil || a != b || ld.calls != 1 {
			t.Fatalf("同一个路径要拿到同一份：err=%v same=%v calls=%d", err, a == b, ld.calls)
		}
	}
	// 另一个路径（哪怕内容一样）在已经加载了的进程里不许再开
	p2, _ := writeFile(t, "other.dll", []byte("abc"))
	if _, err := h.open(p2, known, PinnedVersion, ld.load); !errors.Is(err, ErrAlreadyOpen) {
		t.Errorf("第二个路径：err = %v", err)
	}
	// 关掉以后可以重新开
	a.Close()
	c, err := h.open(p, known, PinnedVersion, ld.load)
	if err != nil || c == a {
		t.Errorf("关闭以后重开：err=%v 是不是旧的=%v", err, c == a)
	}
	c.Close()
}

// ---- 验收 7：关闭顺序 ----

func TestCloseOrder(t *testing.T) {
	p, sum := writeFile(t, "onnxruntime.dll", []byte("abc"))
	ld, eng, rec := newFake("1.24.4")
	rt, err := (&holder{}).open(p, knownFor("1.24.4", sum), PinnedVersion, ld.load)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := rt.NewSession("a.onnx", Options{})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := rt.NewSession("b.onnx", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil { // 关两次不 panic
		t.Fatal(err)
	}
	if n := eng.sessions[0].closeN.Load(); n != 1 {
		t.Errorf("同一个会话关两次，底层只该关一次，实际 %d", n)
	}
	// 关完 Run 返回错误、不崩
	if _, err := s1.Run([]Int64Tensor{{Name: "input_ids", Shape: []int64{1, 1}, Data: []int64{1}}}, []string{"sentence_embedding"}); !errors.Is(err, ErrClosed) {
		t.Errorf("关闭以后 Run：err = %v", err)
	}
	// 运行时关闭：还开着的 s2 先关，然后才是引擎
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil { // 关两次不 panic
		t.Fatal(err)
	}
	ev := rec.list()
	want := []string{"session.Close", "session.Close", "engine.Close"}
	if len(ev) != 3 || ev[0] != want[0] || ev[1] != want[1] || ev[2] != want[2] {
		t.Errorf("关闭顺序 = %v，想要 %v（先会话后引擎）", ev, want)
	}
	if eng.closeN != 1 {
		t.Errorf("引擎关了 %d 次", eng.closeN)
	}
	if _, err := s2.Run([]Int64Tensor{{Name: "input_ids", Shape: []int64{1, 1}, Data: []int64{1}}}, []string{"sentence_embedding"}); !errors.Is(err, ErrClosed) {
		t.Errorf("运行时关闭以后 s2.Run：err = %v", err)
	}
	if _, err := rt.NewSession("c.onnx", Options{}); !errors.Is(err, ErrClosed) {
		t.Errorf("运行时关闭以后 NewSession：err = %v", err)
	}
}

// Close 要等正在跑的 Run 结束，而不是在它脚下销毁
func TestCloseWaitsForRun(t *testing.T) {
	p, sum := writeFile(t, "onnxruntime.dll", []byte("abc"))
	ld, eng, _ := newFake("1.24.4")
	rt, err := (&holder{}).open(p, knownFor("1.24.4", sum), PinnedVersion, ld.load)
	if err != nil {
		t.Fatal(err)
	}
	s, err := rt.NewSession("a.onnx", Options{})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	eng.sessions[0].run = func([]Int64Tensor, []string) (map[string]FloatTensor, error) {
		close(started)
		<-release
		return map[string]FloatTensor{"sentence_embedding": {Shape: []int64{1}, Data: []float32{1}}}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Run(okInputs(2), []string{"sentence_embedding"})
		done <- err
	}()
	<-started
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Run 还没结束，Close 就返回了")
	case <-time.After(100 * time.Millisecond):
	}
	if eng.sessions[0].closeN.Load() != 0 {
		t.Fatal("Run 还没结束，底层会话就被关了")
	}
	close(release)
	if err := <-done; err != nil {
		t.Errorf("正在跑的 Run 被打断了：%v", err)
	}
	<-closed
	rt.Close()
}

func okInputs(n int) []Int64Tensor {
	ids := make([]int64, n)
	mask := make([]int64, n)
	for i := range ids {
		ids[i], mask[i] = int64(i), 1
	}
	return []Int64Tensor{
		{Name: "input_ids", Shape: []int64{1, int64(n)}, Data: ids},
		{Name: "attention_mask", Shape: []int64{1, int64(n)}, Data: mask},
	}
}

func openFake(t *testing.T) (Runtime, *fakeEngine) {
	t.Helper()
	p, sum := writeFile(t, "onnxruntime.dll", []byte("abc"))
	ld, eng, _ := newFake("1.24.4")
	rt, err := (&holder{}).open(p, knownFor("1.24.4", sum), PinnedVersion, ld.load)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	return rt, eng
}

// ---- Run 对着图核对输入输出 ----

func TestSessionRunChecks(t *testing.T) {
	rt, _ := openFake(t)
	s, err := rt.NewSession("m.onnx", Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := []string{"sentence_embedding"}
	if _, err := s.Run(okInputs(3), out); err != nil {
		t.Fatalf("正常输入被拒绝：%v", err)
	}

	bad := func(name string, in []Int64Tensor, outs []string, contains string) {
		t.Helper()
		_, err := s.Run(in, outs)
		if err == nil || !strings.Contains(err.Error(), contains) {
			t.Errorf("%s：err = %v，想要包含 %q", name, err, contains)
		}
	}
	ok := okInputs(3)
	bad("没点名输出", ok, nil, "没有点名")
	bad("不存在的输出", ok, []string{"logits"}, "没有叫")
	bad("输出点两次", ok, []string{"sentence_embedding", "sentence_embedding"}, "两次")
	bad("缺输入", ok[:1], out, "没给")
	bad("多余的输入", append(okInputs(3), Int64Tensor{Name: "token_type_ids", Shape: []int64{1, 3}, Data: []int64{0, 0, 0}}), out, "没有叫")
	bad("输入给两次", append(okInputs(3), okInputs(3)[0]), out, "两次")
	short := okInputs(3)
	short[0].Data = short[0].Data[:2]
	bad("元素个数不够", short, out, "要 3 个元素")
	long := okInputs(3)
	long[1].Data = append(long[1].Data, 9)
	bad("元素个数太多", long, out, "要 3 个元素")
	neg := okInputs(3)
	neg[0].Shape = []int64{1, -3}
	bad("负维度", neg, out, "不合法")
	empty := okInputs(3)
	empty[0].Shape, empty[0].Data = []int64{1, 0}, nil
	bad("空张量", empty, out, "不合法")
	huge := okInputs(3)
	huge[0].Shape = []int64{1 << 40, 1 << 40}
	bad("形状乘积溢出", huge, out, "不合法")
}

func TestSessionRejectsNonInt64Input(t *testing.T) {
	rt, eng := openFake(t)
	eng.extra = []TensorInfo{{Name: "past_key_values.0.key", Type: TypeFloat32, Shape: []int64{1, 8, 0, 64}}}
	s, err := rt.NewSession("kv.onnx", Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := []string{"sentence_embedding"}
	// 图要一个 float 输入而我们没给：报缺输入
	if _, err := s.Run(okInputs(2), out); err == nil || !strings.Contains(err.Error(), "没给") {
		t.Errorf("应当报缺输入：%v", err)
	}
	// 给 float 输入喂 int64 张量：拒绝（Qwen 的 KV 输入将来要扩，现在不支持）
	kv := append(okInputs(2), Int64Tensor{Name: "past_key_values.0.key", Shape: []int64{1}, Data: []int64{0}})
	if _, err := s.Run(kv, out); err == nil || !strings.Contains(err.Error(), "只会喂 int64") {
		t.Errorf("应当拒绝类型不对的输入：%v", err)
	}
}

func TestSessionInfoIsCopied(t *testing.T) {
	rt, _ := openFake(t)
	s, err := rt.NewSession("m.onnx", Options{})
	if err != nil {
		t.Fatal(err)
	}
	in := s.Inputs()
	in[0].Name = "被改了"
	in[0].Shape[0] = 99
	if again := s.Inputs(); again[0].Name != "input_ids" || again[0].Shape[0] != -1 {
		t.Errorf("Inputs() 返回的要是拷贝：%+v", again[0])
	}
	if len(s.Outputs()) != 2 {
		t.Errorf("Outputs() = %+v", s.Outputs())
	}
}

// 同一个会话同时只有一路 Run
func TestSessionRunIsSerialised(t *testing.T) {
	rt, eng := openFake(t)
	s, err := rt.NewSession("m.onnx", Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng.sessions[0].run = func([]Int64Tensor, []string) (map[string]FloatTensor, error) {
		time.Sleep(2 * time.Millisecond)
		return map[string]FloatTensor{"sentence_embedding": {Shape: []int64{1}, Data: []float32{1}}}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if _, err := s.Run(okInputs(2), []string{"sentence_embedding"}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if m := eng.sessions[0].maxInRun.Load(); m != 1 {
		t.Errorf("同时在跑的 Run 最多 %d 路，必须是 1", m)
	}
	if n := eng.sessions[0].runs.Load(); n != 40 {
		t.Errorf("跑了 %d 次，想要 40", n)
	}
}

func TestSessionRunRecoversPanic(t *testing.T) {
	rt, eng := openFake(t)
	s, err := rt.NewSession("m.onnx", Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng.sessions[0].run = func([]Int64Tensor, []string) (map[string]FloatTensor, error) {
		panic("输入文字不该出现在错误里")
	}
	_, err = s.Run(okInputs(2), []string{"sentence_embedding"})
	if err == nil || strings.Contains(err.Error(), "输入文字") {
		t.Errorf("panic 要变成错误，且不带 panic 的值：%v", err)
	}
	// 会话没被毁，下一次还能跑
	eng.sessions[0].run = nil
	if _, err := s.Run(okInputs(2), []string{"sentence_embedding"}); err != nil {
		t.Errorf("panic 之后下一次 Run：%v", err)
	}
}

// ---- 选项 ----

func TestOptionsDefaultsAndValidation(t *testing.T) {
	rt, eng := openFake(t)
	if _, err := rt.NewSession("m.onnx", Options{}); err != nil {
		t.Fatal(err)
	}
	if got, want := eng.opts[0], (Options{IntraThreads: 2, InterThreads: 1, GraphOpt: GraphOptBasic, CpuMemArena: false}); got != want {
		t.Errorf("零值选项应当等于生产值：%+v，想要 %+v", got, want)
	}
	if _, err := rt.NewSession("m.onnx", Options{IntraThreads: 4, InterThreads: 2, GraphOpt: GraphOptAll, CpuMemArena: true}); err != nil {
		t.Fatal(err)
	}
	if got := eng.opts[1]; got.IntraThreads != 4 || got.InterThreads != 2 || got.GraphOpt != GraphOptAll || !got.CpuMemArena {
		t.Errorf("显式选项被改了：%+v", got)
	}
	for _, o := range []Options{{IntraThreads: -1}, {InterThreads: -1}, {IntraThreads: 100000}, {GraphOpt: GraphOpt(9)}, {GraphOpt: GraphOpt(-1)}} {
		if _, err := rt.NewSession("m.onnx", o); err == nil {
			t.Errorf("选项 %+v 应当被拒绝", o)
		}
	}
	if _, err := rt.NewSession("", Options{}); err == nil {
		t.Error("空模型路径应当被拒绝")
	}
	eng.newErr = errors.New("模型坏了")
	if _, err := rt.NewSession("m.onnx", Options{}); err == nil || !strings.Contains(err.Error(), "m.onnx") || !strings.Contains(err.Error(), "模型坏了") {
		t.Errorf("加载模型失败的错误要带路径和原因：%v", err)
	}
}

// 清单本身的体检：每行都有版本、系统、架构、64 位小写十六进制哈希，没有重复哈希
func TestKnownDLLsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range KnownDLLs {
		if k.Version == "" || k.GOOS == "" || k.GOARCH == "" || k.Source == "" {
			t.Errorf("清单行字段不全：%+v", k)
		}
		if len(k.SHA256) != 64 || strings.ToLower(k.SHA256) != k.SHA256 {
			t.Errorf("哈希要是 64 位小写十六进制：%q", k.SHA256)
		}
		if _, err := hex.DecodeString(k.SHA256); err != nil {
			t.Errorf("哈希不是十六进制：%q", k.SHA256)
		}
		if seen[k.SHA256] {
			t.Errorf("哈希重复：%s", k.SHA256)
		}
		seen[k.SHA256] = true
	}
	if _, ok := findKnown(KnownDLLs, KnownDLLs[0].SHA256); !ok && KnownDLLs[0].GOOS == runtime.GOOS {
		t.Error("findKnown 找不到清单里的第一行")
	}
}

// 清单里 windows/amd64 只有官方发布包里的那份（ADR-233）：官方的在，Python wheel 里的（MaiBot 环境里就是它）不在。
// 把借来的 wheel 那份放回清单，这条就红。
func TestKnownDLLsOfficialOnly(t *testing.T) {
	const (
		official = "b95efb2113b603bbbf3f191061c5516a871ed546893c820e4f3b7b6c358dbf2a" // onnxruntime-win-x64-1.24.4.zip 里的 lib/onnxruntime.dll
		zipSum   = "d2319fddfb6ea4db99ccc4b60c85c517bcd855721f5daa6a06d40d7cb2ee2357" // 那个压缩包的 SHA-256（scripts/fetch-ort.ts 钉的）
		wheel    = "87da0279ab54add397fd518f7022efe8b9a89fd968669d0adff9f9f242344b6c" // Python wheel 1.24.4 里的 onnxruntime.dll
	)
	var rows []KnownDLL
	for _, k := range KnownDLLs {
		if k.GOOS == "windows" && k.GOARCH == "amd64" {
			rows = append(rows, k)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("windows/amd64 应当恰好一行（官方包里的），实际 %d 行：%+v", len(rows), rows)
	}
	if rows[0].SHA256 != official || rows[0].Version != PinnedVersion {
		t.Errorf("这一行应当是官方包里的 %s 版：%+v", PinnedVersion, rows[0])
	}
	if !strings.Contains(rows[0].Source, zipSum) {
		t.Errorf("来源要写明压缩包的 SHA-256：%q", rows[0].Source)
	}
	for _, k := range KnownDLLs {
		if k.SHA256 == wheel {
			t.Errorf("Python wheel 里的 dll 不该在清单里（ADR-233）：%+v", k)
		}
	}
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		if _, ok := findKnown(KnownDLLs, wheel); ok {
			t.Error("wheel 里的 dll 不该被认作已知好的")
		}
		if _, ok := findKnown(KnownDLLs, official); !ok {
			t.Error("官方的 dll 要被认作已知好的")
		}
	}
}
