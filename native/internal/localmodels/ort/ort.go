package ort

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
)

// PinnedVersion 是钉住的 onnxruntime 版本（规格-Go本地模型 2.3）：Python 参考环境用的就是这一版，对拍必须同版本，
// 否则差异说不清来源。升版本是单独的提交，要重跑对拍。
const PinnedVersion = "1.24.4"

// 张量元素类型的名字（TensorInfo.Type）。这一版只会用到前两个，其余原样给出名字，让引擎能报清楚"图要的是什么"。
const (
	TypeInt64   = "int64"
	TypeFloat32 = "float32"
)

// 加载和使用时的几类错误。errors.Is 能认出来；Open 的错误都是确定性的（换一次也一样），
// 上层（sumika-models 的 main）按"配置错误"退出码 2 处理，不重试。
var (
	ErrRelativePath      = errors.New("动态库路径必须是绝对路径")
	ErrFileNotFound      = errors.New("找不到文件")
	ErrUnknownDLL        = errors.New("动态库的 SHA-256 不在已知好的清单里，不加载")
	ErrWrongVersion      = errors.New("动态库版本不是钉住的版本")
	ErrNoCPUProvider     = errors.New("动态库里没有 CPU 执行后端")
	ErrNotADLL           = errors.New("不是有效的 dll")
	ErrMissingDependency = errors.New("加载 onnxruntime.dll 失败，常见原因是这台机器没装 VC++ 运行库（Microsoft Visual C++ 2015-2022 Redistributable）")
	ErrAlreadyOpen       = errors.New("这个进程已经加载了另一份动态库")
	ErrClosed            = errors.New("已经关闭")
)

// GraphOpt 是图优化级别。零值是 BASIC，即生产值：EXTENDED、ALL 会让量化图的输出改变 1e-3 量级（规格 2.5），
// 所以别的级别只给对拍里"确认不变"的记录项用。
type GraphOpt int

const (
	GraphOptBasic GraphOpt = iota
	GraphOptDisable
	GraphOptExtended
	GraphOptAll
)

// Options 是会话选项。零值就是生产值：算子内 2 线程、算子间 1、图优化 BASIC、CPU 内存池关。
// 日志级别固定 error，不可调；执行后端固定只有 CPU。
type Options struct {
	IntraThreads int      // 0 = 2；线程数不改变结果（复核方实测 1/2/4 逐位相同），只与 Python 取同值
	InterThreads int      // 0 = 1
	GraphOpt     GraphOpt // 零值 = BASIC
	CpuMemArena  bool     // false = 关。长输入以后内存池只涨不退，而生产不卸载模型（规格 2.5）
}

const (
	defaultIntraThreads = 2
	defaultInterThreads = 1
	maxThreads          = 256
)

func (o Options) normalized() (Options, error) {
	if o.IntraThreads < 0 || o.IntraThreads > maxThreads || o.InterThreads < 0 || o.InterThreads > maxThreads {
		return o, fmt.Errorf("线程数要在 0（用缺省）到 %d 之间：intra=%d inter=%d", maxThreads, o.IntraThreads, o.InterThreads)
	}
	if o.IntraThreads == 0 {
		o.IntraThreads = defaultIntraThreads
	}
	if o.InterThreads == 0 {
		o.InterThreads = defaultInterThreads
	}
	if o.GraphOpt < GraphOptBasic || o.GraphOpt > GraphOptAll {
		return o, fmt.Errorf("不认识的图优化级别 %d", int(o.GraphOpt))
	}
	return o, nil
}

// TensorInfo 是模型一个输入或输出的名字、元素类型、形状（动态维度是 -1）。
type TensorInfo struct {
	Name  string
	Type  string
	Shape []int64
}

// Int64Tensor 是喂给图的一个 int64 输入。Data 的长度必须正好等于 Shape 各维相乘，不许是空张量。
type Int64Tensor struct {
	Name  string
	Shape []int64
	Data  []int64
}

// FloatTensor 是图的一个 float32 输出，Data 是从 ORT 内存拷出来的 Go 切片，ORT 的 Value 已经销毁，可以随便持有。
type FloatTensor struct {
	Shape []int64
	Data  []float32
}

// Session 是一个加载好的模型。同一个 Session 同时只有一路 Run 在跑（内部串行），关闭会等正在跑的那一路结束。
type Session interface {
	Inputs() []TensorInfo
	Outputs() []TensorInfo
	// Run 跑一次前向，返回 outputs 里点名的输出。ORT 一律产出图的全部输出（没有"只取某个"），
	// 没点名的这里也销毁掉，不拷贝。outputs 里的名字必须是图的输出，输入张量的名字、类型、元素个数都要对得上图。
	Run(inputs []Int64Tensor, outputs []string) (map[string]FloatTensor, error)
	// Close 释放会话，可以重复调用；关闭以后 Run 回 ErrClosed。
	Close() error
}

// Runtime 是加载好的 onnxruntime 动态库，进程里只有一份（重复 Open 同一个路径拿到同一个）。
type Runtime interface {
	Version() string
	NewSession(modelPath string, o Options) (Session, error)
	// Close 先关掉还开着的所有 Session，再销毁运行时环境；可以重复调用。动态库本身不卸载（ORT 的静态析构会崩）。
	// 关闭以后同一个进程里可以再 Open（TestORTReopenAfterClose 在真 dll 上查过：会话照常建，输出逐位相同）。
	Close() error
}

// ---- 包内的两层接口：真实现在 backend.go（包 younland），测试用假的 ----

type engine interface {
	Version() string
	Providers() []string
	NewSession(modelPath string, o Options) (rawSession, error)
	Close()
}

type rawSession interface {
	Inputs() []TensorInfo
	Outputs() []TensorInfo
	Run(inputs []Int64Tensor, outputs []string) (map[string]FloatTensor, error)
	Close()
}

// loadFunc 加载动态库。它只在前两道关过了以后才会被调用。
type loadFunc func(absPath string) (engine, error)

// holder 管"进程里只有一份"。生产只有 global 一个；测试各建各的，互不串。
type holder struct {
	mu  sync.Mutex
	cur *loaded
}

var global holder

// Open 加载 libPath 指向的 onnxruntime 动态库。known 是已知好的清单（生产传 KnownDLLs），wantVersion 是钉的版本（PinnedVersion）。
// 过不了关就报错、不加载（第 3 道是例外，见包说明）；所有错误都是确定性的。
func Open(libPath string, known []KnownDLL, wantVersion string) (Runtime, error) {
	return global.open(libPath, known, wantVersion, loadBackend)
}

func (h *holder) open(libPath string, known []KnownDLL, want string, load loadFunc) (Runtime, error) {
	if want == "" {
		return nil, errors.New("没有给钉住的版本号")
	}
	// 第 1 道：绝对路径。"C:foo"（盘符加相对）、"\foo"（没有盘符）在 Windows 上 IsAbs 也是假，一并拒绝
	if !filepath.IsAbs(libPath) {
		return nil, fmt.Errorf("%w：%q", ErrRelativePath, libPath)
	}
	path := filepath.Clean(libPath)

	// 整个 Open 串行：并发的第二个 Open 等第一个做完，然后拿到同一份
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cur != nil {
		if h.cur.isClosed() {
			return nil, fmt.Errorf("上一份运行时正在关闭，等它关完再开：%w", ErrClosed)
		}
		if samePath(h.cur.path, path) && h.cur.want == want {
			return h.cur, nil
		}
		return nil, fmt.Errorf("%w：已加载 %s（%s），不能再加载 %s（%s）", ErrAlreadyOpen, h.cur.path, h.cur.want, path, want)
	}

	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w：%s", ErrFileNotFound, path)
		}
		return nil, fmt.Errorf("读不了 %s：%w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w：%s 不是普通文件", ErrFileNotFound, path)
	}

	// 第 2 道：哈希在清单里才往下走。这一道在 LoadLibrary 之前（见包说明）。Windows 自带的那份 1.17 也是在这里被拦的，不靠版本门
	sum, err := fileSHA256(path)
	if err != nil {
		return nil, fmt.Errorf("读不了 %s：%w", path, err)
	}
	entry, ok := findKnown(known, sum)
	if !ok {
		return nil, fmt.Errorf("%w：%s（SHA-256 %s，%s/%s）", ErrUnknownDLL, path, sum, runtime.GOOS, runtime.GOARCH)
	}
	if entry.Version != want {
		// 清单里这份哈希是别的版本：加载前就拒绝，不用等加载以后
		return nil, fmt.Errorf("%w：%s 的哈希在清单里是 %s，钉的是 %s", ErrWrongVersion, path, entry.Version, want)
	}

	eng, err := load(path)
	if err != nil {
		return nil, translateLoadError(path, err)
	}
	// 第 3 道：加载以后运行时自己报的版本必须正好等于钉的。只有这一道在加载之后拒绝
	if got := eng.Version(); got != want {
		eng.Close()
		return nil, fmt.Errorf("%w：%s 加载后报告 %s，钉的是 %s", ErrWrongVersion, path, got, want)
	}
	// 第 4 道：CPU 后端
	if !hasProvider(eng.Providers(), "CPUExecutionProvider") {
		eng.Close()
		return nil, fmt.Errorf("%w：%s", ErrNoCPUProvider, path)
	}

	l := &loaded{h: h, eng: eng, path: path, want: want, sessions: map[*session]struct{}{}}
	h.cur = l
	return l, nil
}

// translateLoadError 把加载失败翻成人话。哈希门已经过了，所以文件一定存在：
// 193（不是有效的 Win32 程序）= 不是 dll；126（找不到模块）在"文件存在"时 = 缺依赖，几乎总是没装 VC++ 运行库。
func translateLoadError(path string, err error) error {
	if runtime.GOOS == "windows" {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			switch errno {
			case 193:
				return fmt.Errorf("%w：%s（%v）", ErrNotADLL, path, err)
			case 126:
				return fmt.Errorf("%w：%s（%v）", ErrMissingDependency, path, err)
			}
		}
	}
	return fmt.Errorf("加载 %s 失败：%w", path, err)
}

func hasProvider(list []string, name string) bool {
	for _, p := range list {
		if p == name {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
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

// ---- Runtime ----

type loaded struct {
	h    *holder
	eng  engine
	path string
	want string

	mu       sync.Mutex // 保护下面三个
	sessions map[*session]struct{}
	closed   bool
}

func (l *loaded) Version() string { return l.want }

func (l *loaded) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func (l *loaded) NewSession(modelPath string, o Options) (Session, error) {
	o, err := o.normalized()
	if err != nil {
		return nil, err
	}
	if modelPath == "" {
		return nil, errors.New("没有给模型文件路径")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, fmt.Errorf("运行时%w", ErrClosed)
	}
	raw, err := l.eng.NewSession(modelPath, o)
	if err != nil {
		return nil, fmt.Errorf("加载模型 %s 失败：%w", modelPath, err)
	}
	s := &session{owner: l, raw: raw, inputs: raw.Inputs(), outputs: raw.Outputs()}
	l.sessions[s] = struct{}{}
	return s, nil
}

func (l *loaded) forget(s *session) {
	l.mu.Lock()
	delete(l.sessions, s)
	l.mu.Unlock()
}

// Close 先 Session 后环境（ORT 的 Session 引用着环境，顺序反了会崩）。
func (l *loaded) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	open := make([]*session, 0, len(l.sessions))
	for s := range l.sessions {
		open = append(open, s)
	}
	l.mu.Unlock()

	for _, s := range open { // 每个 Session.Close 会等它正在跑的 Run 结束
		_ = s.Close()
	}
	l.eng.Close()

	l.h.mu.Lock()
	if l.h.cur == l {
		l.h.cur = nil
	}
	l.h.mu.Unlock()
	return nil
}

// ---- Session ----

type session struct {
	owner   *loaded
	inputs  []TensorInfo
	outputs []TensorInfo

	mu     sync.Mutex // 一次只有一路 Run；Close 也拿它，等正在跑的 Run 结束
	raw    rawSession
	closed bool
}

func (s *session) Inputs() []TensorInfo  { return cloneInfos(s.inputs) }
func (s *session) Outputs() []TensorInfo { return cloneInfos(s.outputs) }

func cloneInfos(in []TensorInfo) []TensorInfo {
	out := make([]TensorInfo, len(in))
	for i, t := range in {
		out[i] = TensorInfo{Name: t.Name, Type: t.Type, Shape: append([]int64(nil), t.Shape...)}
	}
	return out
}

func (s *session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	raw := s.raw
	s.raw = nil
	raw.Close()
	s.mu.Unlock()
	s.owner.forget(s) // 不在持有 s.mu 时拿 owner.mu：Runtime.Close 先拿 owner.mu 再等各个 s.mu
	return nil
}

func (s *session) Run(inputs []Int64Tensor, outputs []string) (res map[string]FloatTensor, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("会话%w", ErrClosed)
	}
	if err := s.check(inputs, outputs); err != nil {
		return nil, err
	}
	// Go 一侧的 panic 不带走进程；panic 的值不写进错误（别处的值可能是输入）
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, fmt.Errorf("前向内部出错（panic，类型 %T）", r)
		}
	}()
	return s.raw.Run(inputs, outputs)
}

// check 对着图的输入输出声明核对一遍。ORT 自己也会报错，但数据不够时的越界读它不一定拦得住，所以元素个数在这里钉死。
func (s *session) check(inputs []Int64Tensor, outputs []string) error {
	if len(outputs) == 0 {
		return errors.New("没有点名要哪个输出")
	}
	wantOut := map[string]bool{}
	for _, o := range s.outputs {
		wantOut[o.Name] = true
	}
	seen := map[string]bool{}
	for _, name := range outputs {
		if !wantOut[name] {
			return fmt.Errorf("图没有叫 %q 的输出（有 %v）", name, infoNames(s.outputs))
		}
		if seen[name] {
			return fmt.Errorf("输出 %q 点了两次", name)
		}
		seen[name] = true
	}

	types := map[string]string{}
	for _, in := range s.inputs {
		types[in.Name] = in.Type
	}
	got := map[string]bool{}
	for _, in := range inputs {
		typ, ok := types[in.Name]
		if !ok {
			return fmt.Errorf("图没有叫 %q 的输入（有 %v）", in.Name, infoNames(s.inputs))
		}
		if got[in.Name] {
			return fmt.Errorf("输入 %q 给了两次", in.Name)
		}
		got[in.Name] = true
		if typ != TypeInt64 {
			return fmt.Errorf("输入 %q 在图里是 %s，这一版只会喂 int64", in.Name, typ)
		}
		n, ok := elementCount(in.Shape)
		if !ok || n == 0 {
			return fmt.Errorf("输入 %q 的形状 %v 不合法（有负数、溢出，或者是空张量）", in.Name, in.Shape)
		}
		if int64(len(in.Data)) != n {
			return fmt.Errorf("输入 %q 的形状 %v 要 %d 个元素，给了 %d 个", in.Name, in.Shape, n, len(in.Data))
		}
	}
	for _, in := range s.inputs {
		if !got[in.Name] {
			return fmt.Errorf("图要输入 %q，没给", in.Name)
		}
	}
	return nil
}

func infoNames(infos []TensorInfo) []string {
	names := make([]string, len(infos))
	for i, t := range infos {
		names[i] = t.Name
	}
	return names
}

// elementCount 把形状各维相乘；有负数或溢出时 ok 为假。
func elementCount(shape []int64) (int64, bool) {
	total := int64(1)
	for _, d := range shape {
		if d < 0 {
			return 0, false
		}
		if d != 0 && total > (1<<62)/d {
			return 0, false
		}
		total *= d
	}
	return total, true
}
