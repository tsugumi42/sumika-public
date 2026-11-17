package ort

import (
	"fmt"
	"runtime"

	onnx "github.com/younland/onnxruntime"
)

// 这是整个仓库里唯一 import github.com/younland/onnxruntime 的文件（internal/archcheck 钉住）。
// 换绑定（比如 cgo 版）只换这一个文件：实现 engine、rawSession 两个接口，loadBackend 指过去。
//
// 读 younland 源码读到的三件事，这里都照办：
//   - Run 一律要全部输出，所以每次 Run 之后所有输出 Value 都要 Destroy，漏一个就是泄漏；
//   - NewTensor 不拷贝，Value 只持有 Go 切片的指针，调用方在 Run 返回之前必须让输入保持可达（runtime.KeepAlive）；
//   - GetTensorData 返回的是指向 ORT 内存的切片，Destroy 以后失效，必须先拷贝出来。

// loadBackend 加载动态库。绝对路径和哈希都由 holder.open 在调它之前核过；这里不再接受别的路径。
func loadBackend(absPath string) (engine, error) {
	// 严格版本模式：比验证过的上限（1.31）新就拒绝，而不是只往标准库 log 打一行警告
	e, err := onnx.NewEngine(
		onnx.WithLibraryPath(absPath),
		onnx.WithStrictVersion(true),
		onnx.WithEnvName("sumika-models"),
	)
	if err != nil {
		return nil, err
	}
	return &younlandEngine{e: e}, nil
}

type younlandEngine struct{ e *onnx.Engine }

func (y *younlandEngine) Version() string     { return y.e.VersionString() }
func (y *younlandEngine) Providers() []string { return y.e.GetAvailableProviders() }
func (y *younlandEngine) Close()              { y.e.Destroy() }

func (y *younlandEngine) NewSession(modelPath string, o Options) (rawSession, error) {
	opts, err := y.e.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer opts.Destroy() // 会话建好以后选项就没用了（Python 的 SessionOptions 也一样）
	if err := opts.SetIntraOpNumThreads(int32(o.IntraThreads)); err != nil {
		return nil, err
	}
	if err := opts.SetInterOpNumThreads(int32(o.InterThreads)); err != nil {
		return nil, err
	}
	level, err := graphLevel(o.GraphOpt)
	if err != nil {
		return nil, err
	}
	if err := opts.SetGraphOptimizationLevel(level); err != nil {
		return nil, err
	}
	if err := opts.SetLogSeverityLevel(onnx.LogError); err != nil {
		return nil, err
	}
	if err := opts.SetCpuMemArena(o.CpuMemArena); err != nil {
		return nil, err
	}
	s, err := y.e.NewSession(modelPath, opts)
	if err != nil {
		return nil, err
	}
	return &younlandSession{engine: y.e, s: s, inputs: convertInfos(s.Inputs), outputs: convertInfos(s.Outputs)}, nil
}

func graphLevel(g GraphOpt) (onnx.GraphOptimizationLevel, error) {
	switch g {
	case GraphOptBasic:
		return onnx.GraphOptimizationLevelBasic, nil
	case GraphOptDisable:
		return onnx.GraphOptimizationLevelNone, nil
	case GraphOptExtended:
		return onnx.GraphOptimizationLevelExtended, nil
	case GraphOptAll:
		return onnx.GraphOptimizationLevelAll, nil
	}
	return 0, fmt.Errorf("不认识的图优化级别 %d", int(g))
}

func convertInfos(in []onnx.TensorInfo) []TensorInfo {
	out := make([]TensorInfo, len(in))
	for i, t := range in {
		out[i] = TensorInfo{Name: t.Name, Type: elementTypeName(t.DataType), Shape: append([]int64(nil), t.Shape...)}
	}
	return out
}

func elementTypeName(t onnx.TensorElementDataType) string {
	switch t {
	case onnx.TensorElementDataTypeFloat:
		return TypeFloat32
	case onnx.TensorElementDataTypeInt64:
		return TypeInt64
	case onnx.TensorElementDataTypeInt32:
		return "int32"
	case onnx.TensorElementDataTypeFloat16:
		return "float16"
	case onnx.TensorElementDataTypeDouble:
		return "float64"
	case onnx.TensorElementDataTypeBool:
		return "bool"
	}
	return fmt.Sprintf("onnx-type-%d", int32(t))
}

type younlandSession struct {
	engine  *onnx.Engine
	s       *onnx.Session
	inputs  []TensorInfo
	outputs []TensorInfo
}

func (y *younlandSession) Inputs() []TensorInfo  { return y.inputs }
func (y *younlandSession) Outputs() []TensorInfo { return y.outputs }
func (y *younlandSession) Close()                { y.s.Destroy() }

func (y *younlandSession) Run(inputs []Int64Tensor, outputs []string) (map[string]FloatTensor, error) {
	feeds := make(map[string]*onnx.Value, len(inputs))
	defer func() {
		for _, v := range feeds {
			v.Destroy()
		}
	}()
	for _, in := range inputs {
		v, err := y.engine.NewTensor(in.Shape, in.Data) // 实例上的 NewTensor，不用包级那个（它走全局默认引擎）
		if err != nil {
			return nil, fmt.Errorf("建输入张量 %q 失败：%w", in.Name, err)
		}
		feeds[in.Name] = v
	}

	results, err := y.s.Run(feeds)
	// 输入的 Go 切片要活到 Run 返回之后：ORT 只持有指针，不拷贝
	runtime.KeepAlive(inputs)
	if err != nil {
		return nil, err
	}
	// 全部输出都要销毁（图有几个输出就有几个），先拷出点名的那些
	defer func() {
		for _, v := range results {
			v.Destroy()
		}
	}()

	out := make(map[string]FloatTensor, len(outputs))
	for _, name := range outputs {
		v, ok := results[name]
		if !ok {
			return nil, fmt.Errorf("前向没有产出 %q", name)
		}
		shape, err := v.GetShape()
		if err != nil {
			return nil, fmt.Errorf("读输出 %q 的形状失败：%w", name, err)
		}
		data, err := onnx.GetTensorData[float32](v) // 类型不是 float32 时回错误，不 panic
		if err != nil {
			return nil, fmt.Errorf("读输出 %q 失败：%w", name, err)
		}
		// 拷出来：data 指向 ORT 的内存，Destroy 以后就失效了
		out[name] = FloatTensor{Shape: append([]int64(nil), shape...), Data: append([]float32(nil), data...)}
	}
	return out, nil
}
