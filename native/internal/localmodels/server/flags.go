package server

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// 退出码（规格第三节"退出码"一行）。Local 靠它们决定要不要重试。
const (
	ExitOK       = 0
	ExitFailed   = 1 // --verify 发现文件对不上登记表；服务运行中出了没法恢复的错
	ExitConfig   = 2 // 配置、文件、dll 的确定性错误：换一次也一样，Local 停止重试
	ExitPortBusy = 3 // 端口被占：瞬态，Local 换一个口立即重试
	ExitWatchdog = 4 // 看门狗：一次前向卡死，Local 重启
)

// ErrPrefix 是 stderr 里错误行的稳定前缀。Local 把启动阶段 stderr 的最后 20 行放进 lastErr（W1 契约第 6 条）。
const ErrPrefix = "[sumika-models] 错误："

// 环境变量和默认值（照 server.py）。
const (
	PortEnv        = "SUMIKA_EMBED_PORT"
	ModelsRootEnv  = "LOCAL_MODELS_ROOT"
	DefaultPort    = 11440
	DefaultMaxReq  = 600 * time.Second
	DefaultMaxByte = int64(4) << 30
)

// options 是命令行（规格第三节"命令行"一行）。
type options struct {
	host           string
	port           int // -1 = 没给，取环境变量，再取默认 11440
	maxLoaded      int
	idleUnload     float64 // 秒；0 = 不自动卸
	threads        int
	maxBytes       int64
	wait           float64 // 秒
	maxRequest     float64 // 秒；0 = 不设
	models         string
	lazyModels     string
	modelsRoot     string
	ortLibrary     string
	maxLineTokens  int
	watchdog       float64 // 秒；0 = 关
	exitOnStdinEOF bool
	verify         bool
	config         string // Python 版有、这一版没有的参数：给了就报错说清楚
}

// parseFlags 解析命令行。不认识的参数、值不合法都是错误（退出码 2）；-h 返回 flag.ErrHelp。
func parseFlags(args []string) (*options, string, error) {
	var o options
	fs := flag.NewFlagSet("sumika-models", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.host, "host", "127.0.0.1", "监听地址，只允许 127.0.0.1 / localhost / ::1")
	fs.IntVar(&o.port, "port", -1, "监听端口；默认读环境变量 "+PortEnv+"，没有再取 11440")
	fs.IntVar(&o.maxLoaded, "max-loaded", 2, "同时装几个模型（小于 1 按 1）")
	fs.Float64Var(&o.idleUnload, "idle-unload", 600, "空闲多少秒卸载；0 = 不自动卸")
	fs.IntVar(&o.threads, "threads", 2, "每个模型的 ONNX 算子内线程数")
	fs.Int64Var(&o.maxBytes, "max-bytes", DefaultMaxByte, "模型内存估计的软上限（字节）")
	fs.Float64Var(&o.wait, "wait", 30, "等加载 / 腾位置的秒数")
	fs.Float64Var(&o.maxRequest, "max-request-seconds", DefaultMaxReq.Seconds(), "一次嵌入 / 重排最多算多少秒，到了在下一行之前停下回 504；0 = 不设上限")
	fs.StringVar(&o.models, "models", "", "点名的模型 id（逗号分隔），启动时必须就绪；缺省 = 登记的全部（去掉 --lazy-models 里的）")
	fs.StringVar(&o.lazyModels, "lazy-models", "", "也登记也列出、但启动时不查文件的模型 id（逗号分隔），生产里放重排")
	fs.StringVar(&o.modelsRoot, "models-root", "", "模型根目录；缺省读环境变量 "+ModelsRootEnv+"，都没有就报错")
	fs.StringVar(&o.ortLibrary, "ort-library", "", "onnxruntime.dll 的绝对路径；缺省是本程序所在目录里的 onnxruntime.dll")
	fs.IntVar(&o.maxLineTokens, "max-line-tokens", 0, "嵌入单行 token 上限（只能收紧，超过的截断）；0 = 登记表的值")
	fs.Float64Var(&o.watchdog, "run-watchdog-seconds", 60, "单次前向超过这么多秒，进程自己退出（退出码 4）；0 = 关")
	fs.BoolVar(&o.exitOnStdinEOF, "exit-on-stdin-eof", false, "读 stdin，读到 EOF 就退出（父进程没了不留孤儿）")
	fs.BoolVar(&o.verify, "verify", false, "不起服务：算全部登记模型文件的哈希对登记表，退出码 0 / 1")
	fs.StringVar(&o.config, "config", "", "（这一版没有）")
	usage := usageText(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, usage, err
		}
		return nil, usage, err
	}
	if fs.NArg() > 0 {
		return nil, usage, fmt.Errorf("多余的参数 %q：这个程序只认 --选项", fs.Arg(0))
	}
	return &o, usage, nil
}

func usageText(fs *flag.FlagSet) string {
	var b strings.Builder
	b.WriteString("用法：sumika-models [选项]\n\n令牌从环境变量 " + TokenEnv + " 读（必填，没有就拒绝启动）。\n\n")
	out := fs.Output()
	fs.SetOutput(&b)
	fs.PrintDefaults()
	fs.SetOutput(out)
	return b.String()
}

// check 查数值的范围。返回一句话说明哪一项不对。
func (o *options) check() error {
	switch {
	case o.config != "":
		return errors.New("--config 这一版没有：登记表编在程序里（等 BurntSushi/toml 进了模块缓存再改成内嵌的 models.toml），要换模型登记请改 engine/registry.go")
	case o.port < -1 || o.port > 65535:
		return fmt.Errorf("--port %d 不在 0～65535 里", o.port)
	case o.threads < 1 || o.threads > 256:
		return fmt.Errorf("--threads %d 不在 1～256 里", o.threads)
	case o.idleUnload < 0, o.wait < 0, o.maxRequest < 0, o.watchdog < 0:
		return errors.New("--idle-unload、--wait、--max-request-seconds、--run-watchdog-seconds 都不能是负数")
	case o.maxBytes < 0:
		return fmt.Errorf("--max-bytes %d 不能是负数", o.maxBytes)
	case o.maxLineTokens < 0:
		return fmt.Errorf("--max-line-tokens %d 不能是负数", o.maxLineTokens)
	}
	return nil
}

// splitIDs 把逗号分隔的 id 拆开，去掉空格和空项。
func splitIDs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func seconds(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }
