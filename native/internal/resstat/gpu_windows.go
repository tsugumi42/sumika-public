package resstat

import (
	"fmt"
	"math"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 显存和 GPU 引擎利用率用 Windows 性能计数器（PDH）取，和任务管理器是同一个数据源（规格-运行页 第四节第 1 条的结论：
// NVML 在 WDDM 下不报单进程显存，DXGI 只看得到自己）。不加依赖，pdh.dll 用 LazyDLL 调。
// 计数器名用 PdhAddEnglishCounterW：中文系统的计数器名被本地化，不用 English 版本会找不到。
// 利用率类计数器要两次采样才有值，第一次采样没有利用率（那一格写"—"）；显存是即时值，第一次就有。
// 取不到（计数器被禁用、权限不足、系统没有）时 Read 返回错误，页面整列写"不可用"和原因。

var (
	pdh                      = windows.NewLazySystemDLL("pdh.dll")
	pdhOpenQuery             = pdh.NewProc("PdhOpenQueryW")
	pdhAddEnglishCounter     = pdh.NewProc("PdhAddEnglishCounterW")
	pdhCollectQueryData      = pdh.NewProc("PdhCollectQueryData")
	pdhGetFormattedCounterAr = pdh.NewProc("PdhGetFormattedCounterArrayW")
	pdhCloseQuery            = pdh.NewProc("PdhCloseQuery")
)

const (
	pdhFmtDouble   = 0x00000200
	pdhMoreData    = 0x800007D2
	pdhNoData      = 0x800007D5
	pdhInvalidData = 0xC0000BC6
	pdhCStatusOK   = 0 // PDH_CSTATUS_VALID_DATA
	pdhCStatusNew  = 1 // PDH_CSTATUS_NEW_DATA
)

const (
	pathGPUMem = `\GPU Process Memory(*)\Dedicated Usage`
	pathGPUEng = `\GPU Engine(*)\Utilization Percentage`
)

// pdhItem 是 PDH_FMT_COUNTERVALUE_ITEM_W（64 位）：名字指针，加 CStatus、对齐空隙、8 字节的值
type pdhItem struct {
	name   *uint16
	status uint32
	_      uint32
	value  uint64 // 用 PDH_FMT_DOUBLE 取，这里是 double 的位
}

// 性能计数器的重开节奏：句柄打开超过 pdhRecycle 就关了重开（通配符展开出来的实例列表不保证一直跟着新进程变，
// 重开是最稳的办法）；打开失败后 pdhRetry 之内不再试，免得每次采样都白花 400 多毫秒去开一个开不了的东西
const (
	pdhRecycle = 10 * time.Minute
	pdhRetry   = 60 * time.Second
)

type pdhGPU struct {
	query, mem, eng uintptr
	collected       int // 已经 Collect 过几次；利用率要 >= 2
	openedAt        time.Time
	failedAt        time.Time
	failed          error
}

func newGPU() GPUSource { return &pdhGPU{} }

func pdhErr(r uintptr) error { return fmt.Errorf("性能计数器返回 0x%08X", uint32(r)) }

func (g *pdhGPU) open() error {
	if g.query != 0 {
		return nil
	}
	if g.failed != nil && time.Since(g.failedAt) < pdhRetry {
		return g.failed
	}
	err := g.openOnce()
	g.failed, g.failedAt = err, time.Now()
	return err
}

func (g *pdhGPU) openOnce() error {
	var q uintptr
	if r, _, _ := pdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&q))); r != 0 {
		return fmt.Errorf("打不开性能计数器查询：%w", pdhErr(r))
	}
	add := func(path string) (uintptr, error) {
		p, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			return 0, err
		}
		var c uintptr
		if r, _, _ := pdhAddEnglishCounter.Call(q, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&c))); r != 0 {
			return 0, pdhErr(r)
		}
		return c, nil
	}
	mem, err := add(pathGPUMem)
	if err != nil {
		pdhCloseQuery.Call(q)
		return fmt.Errorf("这台机器没有显存计数器（GPU Process Memory）：%w", err)
	}
	eng, err := add(pathGPUEng)
	if err != nil {
		eng = 0 // 没有引擎计数器不影响显存
	}
	g.query, g.mem, g.eng, g.collected, g.openedAt = q, mem, eng, 0, time.Now()
	return nil
}

func (g *pdhGPU) Close() {
	if g.query != 0 {
		pdhCloseQuery.Call(g.query)
	}
	g.query, g.mem, g.eng, g.collected = 0, 0, 0, 0
}

func (g *pdhGPU) Read() (GPUReading, error) {
	if g.query != 0 && time.Since(g.openedAt) > pdhRecycle {
		g.Close()
	}
	if err := g.open(); err != nil {
		return GPUReading{}, err
	}
	if r, _, _ := pdhCollectQueryData.Call(g.query); r != 0 {
		g.Close()
		return GPUReading{}, fmt.Errorf("采不到性能计数器：%w", pdhErr(r))
	}
	g.collected++
	out := GPUReading{Memory: map[int]int64{}, Util: map[int]float64{}}
	items, err := formattedArray(g.mem)
	if err != nil {
		g.Close()
		return GPUReading{}, fmt.Errorf("读不到显存计数器：%w", err)
	}
	for _, it := range items {
		if pid, ok := parsePID(it.name); ok {
			out.Memory[pid] += int64(it.value)
		}
	}
	if g.eng != 0 && g.collected >= 2 {
		// 引擎利用率：一个进程在每种引擎（3D、Copy、Video 等）上各有若干实例，同种相加，种与种之间取最大，再限到 100
		per := map[int]map[string]float64{}
		if items, err := formattedArray(g.eng); err == nil {
			for _, it := range items {
				pid, ok := parsePID(it.name)
				if !ok {
					continue
				}
				if per[pid] == nil {
					per[pid] = map[string]float64{}
				}
				per[pid][engineType(it.name)] += it.value
			}
		}
		for pid, byType := range per {
			var best float64
			for _, v := range byType {
				best = math.Max(best, v)
			}
			out.Util[pid] = math.Min(best, 100)
		}
	}
	return out, nil
}

type pdhValue struct {
	name  string
	value float64
}

// formattedArray 取一个通配符计数器当前所有实例的值（先问缓冲区大小，再取）。状态不是有效数据的实例跳过
func formattedArray(counter uintptr) ([]pdhValue, error) {
	var size, count uint32
	r, _, _ := pdhGetFormattedCounterAr.Call(counter, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	if uint32(r) == pdhNoData || uint32(r) == pdhInvalidData {
		return nil, nil // 还没有可用的值（第一次采样的速率类计数器）：不是错误
	}
	if uint32(r) != pdhMoreData {
		return nil, pdhErr(r)
	}
	buf := make([]uint64, (size+7)/8) // 用 uint64 保证 8 字节对齐；名字字符串也在这块缓冲区里
	r, _, _ = pdhGetFormattedCounterAr.Call(counter, pdhFmtDouble, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0])))
	if r != 0 {
		if uint32(r) == pdhNoData || uint32(r) == pdhInvalidData {
			return nil, nil
		}
		return nil, pdhErr(r)
	}
	items := unsafe.Slice((*pdhItem)(unsafe.Pointer(&buf[0])), count)
	out := make([]pdhValue, 0, count)
	for _, it := range items {
		if it.status != pdhCStatusOK && it.status != pdhCStatusNew {
			continue
		}
		out = append(out, pdhValue{name: windows.UTF16PtrToString(it.name), value: math.Float64frombits(it.value)})
	}
	return out, nil
}
