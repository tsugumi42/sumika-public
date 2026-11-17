package embed

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sumika-native/internal/config"
	"sumika-native/internal/llm"
	"sumika-native/internal/procreg"
)

// BackendGoModels 是嵌入唯一的后端：起自己的 sumika-models.exe（规格-Go本地模型）。空也当它。
// 旧的 legacy（借外部 Python 嵌入服务的桥）已删；配置里还写着 legacy 时 Start 不启动子进程，只把原因亮出来。
const (
	BackendGoModels      = config.BackendGoModels
	removedBackendLegacy = "legacy"
)

// ErrRerankNotConfigured：go-models 后端没点名重排模型（memory.rerank.model 空），Rerank 没有可调的东西。原因码 rerank-not-configured
var ErrRerankNotConfigured = errors.New("rerank-not-configured")

// ErrRerankModelUnknown：go-models 后端起来了，但 memory.rerank.model 写的 id 服务里没有（或不是重排模型）。
// 这只让重排用不了（原因码 rerank-model-unknown），不拖累嵌入：服务对 --lazy-models 里不认识的 id 只忽略
var ErrRerankModelUnknown = errors.New("rerank-model-unknown")

// go-models 下起 sumika-models 的固定参数（规格-Go本地模型 GM7 具体参数；W1 的参数表同一组）
const (
	goModelsMaxLoaded = 2 // 嵌入加重排，同时装两个
	goModelsThreads   = 2 // 每个模型的 ONNX 算子内线程数
	// GoModelsMaxLineTokens 是生产里 --max-line-tokens 传的值（规格 2.5"单行最大长度"的规则：8192 token 一行峰值工作集超过 2 GB
	// 或单行超过 10 秒就传 2048）。GM6 对拍量到：8192 token 一行 43 秒、峰值 7.7 GB，所以传 2048，超过的截断（登记差异 S2）。
	// 2048 token 一行 3 秒多、峰值 1 GB 出头；聊天、记忆条目里没有这么长的文本。不是配置键：改它要改规格 2.5 和这里
	GoModelsMaxLineTokens = 2048
	// 服务连着报"端口被占"最多换几个口马上重试，再多就走退避（freePort 先监听再关，到子进程绑定之间有窗口，偶发一两次正常）
	maxPortRetries = 5
	// 服务启动阶段 stderr 里错误行的稳定前缀，和退出码 2 / 3（localmodels/server/flags.go 的 ErrPrefix、ExitConfig、ExitPortBusy 同一组值；
	// embed 是常驻程序的依赖、不能 import localmodels，所以写在这里，embed 的测试对着 server 包断言相等）
	modelsErrPrefix = "[sumika-models] 错误："
	exitConfig      = 2
	exitPortBusy    = 3
	// 令牌的环境变量名，和 server.TokenEnv 是同一个
	tokenEnv = "SUMIKA_EMBED_TOKEN"
)

// ModelsExeName 是 sumika-models 可执行文件在资源根里的文件名（只认这一处，不搜索不回退）
func ModelsExeName() string {
	if runtime.GOOS == "windows" {
		return "sumika-models.exe"
	}
	return "sumika-models"
}

// LocalConfig 是本机嵌入子进程的配置（照 sumika.yml 的 embed 一节）。路径都要是绝对路径（LocalConfigFrom 会先把相对路径
// 按数据目录解析好）；Model 没有默认值（ADR-098：不写死本机路径），没配全就不起
type LocalConfig struct {
	Model         string // 请求里写的模型名
	Backend       string // 空或 go-models；legacy 已移除，别的值起不来
	Exe           string // sumika-models.exe
	ModelsRoot    string // 模型目录的根
	OrtLibrary    string // onnxruntime.dll；空 = 不传，sumika-models 取它自己所在目录里的那个
	RerankModel   string // 空 = 不点名重排模型；非空时以 --lazy-models 点名：启动时不查它的文件，坏了只让它自己用不了、不拖累嵌入
	MaxLineTokens int    // --max-line-tokens；0 = 不传（用登记表的值）
	// OnStatus 把状态变化交给宿主提醒；不带原文，回调在状态锁外执行。
	OnStatus func(Status)
}

// LocalConfigFrom 把 sumika.yml 的 embed 一节（和 memory.rerank.model）变成 Local 的配置：装配（app）和评测工具共用这一处，
// 不各写一份。resourceRoot 是资源根：modelsExe 空时取里面的 sumika-models.exe（只认这一处）；给空串就是没有资源根（评测工具），
// 那时 Exe 空，Local 起不来并说清缺哪一项。
//
// dataDir 是数据目录：modelsRoot 空 = <dataDir>/models；modelsRoot、modelsExe、ortLibrary 写相对路径时相对 dataDir 解析
// （Sumika 的工作目录不固定，不许按当前工作目录解析）；绝对路径照旧
func LocalConfigFrom(e config.Embed, rerankModel, resourceRoot, dataDir string) LocalConfig {
	c := LocalConfig{Model: e.Model, Backend: e.Backend}
	if c.Backend == "" {
		c.Backend = BackendGoModels
	}
	if c.Backend != BackendGoModels {
		return c // legacy / 不认识的值：Start 会把原因说清，不拼路径
	}
	if e.ModelsRoot == "" {
		if dataDir != "" {
			c.ModelsRoot = filepath.Join(dataDir, "models")
		}
	} else {
		c.ModelsRoot = dataPath(e.ModelsRoot, dataDir)
	}
	c.OrtLibrary = dataPath(e.OrtLibrary, dataDir)
	c.Exe = dataPath(e.ModelsExe, dataDir)
	if c.Exe == "" && resourceRoot != "" {
		c.Exe = filepath.Join(resourceRoot, ModelsExeName())
	}
	c.RerankModel, c.MaxLineTokens = rerankModel, GoModelsMaxLineTokens
	return c
}

// dataPath 把配置里的路径按数据目录解析：空和绝对路径不动，相对路径拼在 dataDir 下面
func dataPath(p, dataDir string) string {
	if p == "" || filepath.IsAbs(p) || dataDir == "" {
		return p
	}
	return filepath.Join(dataDir, p)
}

// builtinModelFiles 是 sumika-models 登记表（internal/localmodels/engine）里每个模型在模型根目录下的位置：
// Dir 相对模型根，onnx 和 tokenizer.json 都在 Dir 里；page 是官方下载页（空 = 没登记）。embed 是常驻程序的依赖、
// 不能 import localmodels（会把 ORT 链进来），所以这里抄一份；TestModelFilesMatchRegistry 对着 engine.BuiltinSpecs
// 逐条核对（目录、文件、下载页），登记表改了这里会红。
var builtinModelFiles = map[string]struct{ dir, onnx, page string }{
	"gte-multilingual-base": {dir: "_待分类/gte-multilingual-base", onnx: "model_quantized.onnx", page: "https://huggingface.co/Alibaba-NLP/gte-multilingual-base"},
	"bge-reranker-v2-m3":    {dir: "_待分类/bge-reranker-v2-m3", onnx: "model_quantized.onnx"},
}

// ModelDownloadURL 是这个模型登记表里的官方下载页；没登记这个模型或没写地址返回空。
func ModelDownloadURL(model string) string { return builtinModelFiles[model].page }

// ModelFiles 是这个模型要的两个文件的完整路径（先 onnx、后 tokenizer.json）；不管是登记的模型还是别的 id 返回空。
// 给缺模型的自检和界面显示“它在找哪”：路径按模型根拼，不让 sumika-models 把绝对路径洗掉
func ModelFiles(modelsRoot, model string) []string {
	f, ok := builtinModelFiles[model]
	if !ok || modelsRoot == "" {
		return nil
	}
	dir := filepath.Join(modelsRoot, filepath.FromSlash(f.dir))
	return []string{filepath.Join(dir, f.onnx), filepath.Join(dir, "tokenizer.json")}
}

// MissingModel 是嵌入缺的模型文件：Model 是 embed.model，Path 是正在找的完整文件路径（onnx 或 tokenizer.json），
// DownloadURL 是登记表里的官方下载页，空 = 没登记。给设置页“打开下载页”“指定模型目录”两个入口用。
type MissingModel struct {
	Model       string
	Path        string
	DownloadURL string
}

const (
	healthEvery    = 30 * time.Second
	startupTimeout = 180 * time.Second
	maxBackoff     = 60 * time.Second
	// 启动阶段留子进程 stderr 的最后多少行、多少字节（W1 契约第 6 条）；就绪以后不再留，运行期的回溯可能带到输入文字
	stderrTailLines = 20
	stderrTailBytes = 2048
	// 就绪前的预热嵌入：一个短句，不是用户的文字
	warmText = "预热一句"
)

// Status 是本机嵌入子进程现在的样子（照 daemon/plugins/embed.ts 的 EmbedStatus）
type Status string

const (
	StatusOff      Status = "off"
	StatusStarting Status = "starting"
	StatusReady    Status = "ready"
	StatusDown     Status = "down"
)

// Local 起、看着、挂了按 1/2/4/8… 秒（最长 60 秒）重启本机的嵌入子进程；就绪以后 Embed 走 Client（照 embed.ts）
type Local struct {
	cfg  LocalConfig
	logf func(format string, args ...any)
	// startup 是启动阶段最多等多久才算超时（默认 startupTimeout；测试里缩短）
	startup time.Duration

	mu           sync.Mutex
	status       Status
	lastErr      string
	missingModel *MissingModel // checkFiles 发现模型文件缺失时记下；下次 checkFiles 通过就清空

	notified bool // 初始 off 也要通知一次；之后只通知状态变化。
	client   *Client
	rerank   *RerankClient // go-models 且点名了重排模型时，和 client 同时就绪
	rerankNo string        // 点名的重排模型服务里没有时的原因（Rerank 回 ErrRerankModelUnknown 带它）；空 = 没问题
	cmd      *exec.Cmd
	stopped  bool
	gaveUp   bool          // 确定性的错误（退出码 2 之类）：不再重试，Wait 不用傻等
	ready    chan struct{} // 每次就绪关掉一次；Wait 用
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewLocal 建一个还没起的本机嵌入；logf 为空就不打日志
func NewLocal(cfg LocalConfig, logf func(string, ...any)) *Local {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Local{cfg: cfg, logf: logf, startup: startupTimeout, status: StatusOff, ready: make(chan struct{})}
}

// missing 是没配的键（YAML 里的名字）
func (l *Local) missing() []string {
	var m []string
	for _, k := range []struct{ key, val string }{{"embed.modelsRoot", l.cfg.ModelsRoot}, {"embed.model", l.cfg.Model}, {"embed.modelsExe", l.cfg.Exe}} {
		if k.val == "" {
			m = append(m, k.key)
		}
	}
	return m
}

func (l *Local) configured() bool { return len(l.missing()) == 0 }

// Start 在后台起子进程并一直看着，直到 Stop。没配全、找不到文件：状态停在 off / down，写明原因，不重试（等主人改配置）
func (l *Local) Start() {
	b := l.cfg.Backend
	if b == "" {
		b = BackendGoModels
	}
	switch b {
	case BackendGoModels:
	case removedBackendLegacy:
		l.set(StatusDown, "embed.backend: legacy 已移除（不再借外部的 Python 嵌入服务）：改成 go-models，并在 embed.modelsRoot 或 <数据目录>/models 里放好模型。记忆检索先只用全文检索。")
		return
	default:
		l.set(StatusDown, fmt.Sprintf("embed.backend：%q 不认识，只有 go-models（legacy 已移除）。记忆检索先只用全文检索。", b))
		return
	}
	if m := l.missing(); len(m) > 0 {
		l.set(StatusOff, "没配嵌入服务（"+strings.Join(m, "、")+"），记忆检索先只用全文检索")
		return
	}
	if why := l.checkFiles(); why != "" {
		l.set(StatusDown, why)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.mu.Lock()
	l.cancel, l.done = cancel, make(chan struct{})
	l.mu.Unlock()
	go l.run(ctx)
}

// checkFiles 起进程之前先看一眼要用的文件在不在，免得起一个必然失败的进程：sumika-models（不搜索、不回退，路径必须是绝对路径）、
// 配了 onnxruntime.dll 的话也查、登记过的模型要的 onnx 和 tokenizer.json。模型文件缺了就直接说完整路径，不先起子进程再让它退出。
// 路径写了相对路径一样拒绝（sumika-models 按它自己的工作目录解析，不是我们的）。没问题返回空串
func (l *Local) checkFiles() string {
	l.setMissing(nil)
	for _, f := range []struct{ key, path string }{{"embed.modelsExe", l.cfg.Exe}, {"embed.modelsRoot", l.cfg.ModelsRoot}, {"embed.ortLibrary", l.cfg.OrtLibrary}} {
		if f.path != "" && !filepath.IsAbs(f.path) {
			return fmt.Sprintf("%s 要写绝对路径（现在是 %q）。记忆检索先只用全文检索。", f.key, f.path)
		}
	}
	if _, err := os.Stat(l.cfg.Exe); err != nil {
		return fmt.Sprintf("找不到可执行文件：sumika-models（%s）。embed.modelsExe 空时只认资源根里的 %s，不搜索别的目录。记忆检索先只用全文检索。", l.cfg.Exe, ModelsExeName())
	}
	if l.cfg.OrtLibrary != "" {
		if _, err := os.Stat(l.cfg.OrtLibrary); err != nil {
			return fmt.Sprintf("找不到 onnxruntime 动态库：%s（embed.ortLibrary）。记忆检索先只用全文检索。", l.cfg.OrtLibrary)
		}
	}
	for _, file := range ModelFiles(l.cfg.ModelsRoot, l.cfg.Model) {
		if _, err := os.Stat(file); err != nil {
			l.setMissing(&MissingModel{Model: l.cfg.Model, Path: file, DownloadURL: ModelDownloadURL(l.cfg.Model)})
			return fmt.Sprintf("找不到模型文件：%s。把它放到模型目录里（embed.modelsRoot，空 = <数据目录>/models）。记忆检索先只用全文检索。", file)
		}
	}
	return ""
}

// setMissing 记下（或清空）缺的模型文件；设置页读的是同一份。
func (l *Local) setMissing(m *MissingModel) {
	l.mu.Lock()
	l.missingModel = m
	l.mu.Unlock()
}

// MissingModel 是现在缺的模型文件：登记过的模型少 onnx 或 tokenizer.json 时非空；别的原因（可执行文件、运行库、
// 模型 id 不认识）和没缺时为空。返回拷贝，调用方改了不影响 Local。
func (l *Local) MissingModel() *MissingModel {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.missingModel == nil {
		return nil
	}
	m := *l.missingModel
	return &m
}

// Stop 关掉子进程、不再重启；等看管的那个 goroutine 退出
func (l *Local) Stop() {
	l.mu.Lock()
	l.stopped = true
	cancel, done := l.cancel, l.done
	l.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// Status 现在的状态和最近一次的原因
func (l *Local) Status() (Status, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status, l.lastErr
}

// Wait 等到就绪，最多等 d；超时、没配返回 false
func (l *Local) Wait(d time.Duration) bool {
	l.mu.Lock()
	if l.status == StatusReady {
		l.mu.Unlock()
		return true
	}
	if !l.configured() || l.cancel == nil || l.gaveUp {
		l.mu.Unlock()
		return false
	}
	ch := l.ready
	l.mu.Unlock()
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

func (l *Local) Model() string { return l.cfg.Model }

// readyClient 就绪时的客户端；没就绪 ok 为假
func (l *Local) readyClient() (c *Client, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.client, l.status == StatusReady && l.client != nil
}

// Embed 就绪时走子进程的 /v1/embeddings；没就绪返回 ErrUnavailable
func (l *Local) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	c, ok := l.readyClient()
	if !ok {
		return nil, ErrUnavailable
	}
	return c.Embed(ctx, texts)
}

// EmbedTyped 让 Local 成为 Typed（守卫用接口断言取它，outbound.go）：sumika-models 认 input_type，类型先照校验
func (l *Local) EmbedTyped(ctx context.Context, t Type, texts []string) ([][]float32, error) {
	if t != TypeQuery && t != TypeDocument {
		return nil, fmt.Errorf("input_type 只有 query / document 两种：%q", t)
	}
	c, ok := l.readyClient()
	if !ok {
		return nil, ErrUnavailable
	}
	return c.EmbedTyped(ctx, t, texts)
}

// Rerank 重排：走同一个服务的 /v1/rerank，用 RerankModel 点名的那个模型：没点名回 ErrRerankNotConfigured，
// 服务还没就绪回 ErrUnavailable；重排模型的文件坏了不影响嵌入（--lazy-models），只是这里的请求会收到服务的 500 类错误和原因，
// 记忆那边退回原顺序
func (l *Local) Rerank(ctx context.Context, query string, docs []string) ([]int, []float64, error) {
	if l.cfg.RerankModel == "" {
		return nil, nil, ErrRerankNotConfigured
	}
	l.mu.Lock()
	rc, ok, no := l.rerank, l.status == StatusReady && l.rerank != nil, l.rerankNo
	l.mu.Unlock()
	if no != "" {
		return nil, nil, fmt.Errorf("%w：%s", ErrRerankModelUnknown, no)
	}
	if !ok {
		return nil, nil, ErrUnavailable
	}
	return rc.Rerank(ctx, query, docs)
}

func (l *Local) set(s Status, why string) {
	l.mu.Lock()
	if why != "" || s == StatusReady {
		l.lastErr = why
	}
	changed := s != l.status || !l.notified
	l.notified = true
	if changed {
		l.status = s
		if s == StatusReady {
			close(l.ready)
		} else {
			select {
			case <-l.ready:
				l.ready = make(chan struct{})
			default:
			}
		}
	}
	l.mu.Unlock()
	if changed && why != "" {
		l.logf("嵌入子进程：%s", why)
	}
	if l.cfg.OnStatus != nil && changed {
		l.cfg.OnStatus(s)
	}
}

// onceResult 是 once 起一次子进程的结果
type onceResult struct {
	healthy  bool   // 就绪过（下次重启的退避从 1 秒算）
	why      string // 没成或退出的原因（空 = 就绪过以后正常退出）
	fatal    bool   // 确定性的错误，换一次也一样：不再重试（go-models 的退出码 2；模型 id 或 kind 不对）
	portBusy bool   // 端口被占（go-models 的退出码 3）：换一个口马上重试
}

// run 是看管循环：起一个子进程 → 等它就绪 → 每 30 秒看一眼 → 挂了退避以后再起。
// go-models 的退出码有分别（第三节"退出码"一行）：2 停止重试，状态 Down 带原因，等配置改了重启后台；3 换口马上重试（连着最多 maxPortRetries 次）；
// 4（看门狗）和别的一律退避重启
func (l *Local) run(ctx context.Context) {
	defer close(l.done)
	backoff := time.Second
	busy := 0
	for ctx.Err() == nil {
		res := l.once(ctx)
		if ctx.Err() != nil {
			return
		}
		if res.fatal {
			l.mu.Lock()
			l.gaveUp = true
			l.mu.Unlock()
			l.set(StatusDown, res.why)
			return
		}
		if res.portBusy && busy < maxPortRetries {
			busy++
			l.logf("嵌入子进程：端口被占，换一个口马上重试（第 %d 次）", busy)
			continue
		}
		busy = 0
		if res.healthy {
			backoff = time.Second
		}
		secs := int(backoff / time.Second)
		why := res.why
		if why == "" {
			why = "子进程退出了"
		}
		// 带上这一轮为什么没成（模型 id 不在服务里、就绪前退出的 stderr 末尾……），状态里看得到；
		// 多行的原因（stderr 末尾）放在"N 秒后重启"后面，第一行始终是一句完整的话
		head, rest, multi := strings.Cut(why, "\n")
		why = fmt.Sprintf("%s，%d 秒后重启", head, secs)
		if multi {
			why += "\n" + rest
		}
		l.set(StatusDown, why)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// command 拼起子进程的命令和环境。令牌只走环境变量，不进命令行（命令行别的进程也看得到）；
// 端口和模型根显式传，不靠环境变量
func (l *Local) command(port int, token string) *exec.Cmd {
	args := []string{"--port", strconv.Itoa(port), "--models", l.cfg.Model}
	if l.cfg.RerankModel != "" {
		args = append(args, "--lazy-models", l.cfg.RerankModel)
	}
	args = append(args, "--max-loaded", strconv.Itoa(goModelsMaxLoaded), "--idle-unload", "0", "--threads", strconv.Itoa(goModelsThreads),
		"--models-root", l.cfg.ModelsRoot)
	if l.cfg.OrtLibrary != "" {
		args = append(args, "--ort-library", l.cfg.OrtLibrary)
	}
	args = append(args, "--exit-on-stdin-eof")
	if l.cfg.MaxLineTokens > 0 {
		args = append(args, "--max-line-tokens", strconv.Itoa(l.cfg.MaxLineTokens))
	}
	cmd := exec.Command(l.cfg.Exe, args...)
	cmd.Env = append(os.Environ(), tokenEnv+"="+token)
	return cmd
}

// once 起一次子进程，一直到它退出或被停；返回它有没有就绪过（就绪过的，下次重启退避从 1 秒算）、
// 没成或退出的原因（空 = 就绪过以后正常退出），和这个原因是不是换一次也一样
func (l *Local) once(ctx context.Context) onceResult {
	port, err := freePort()
	if err != nil {
		return onceResult{why: "找不到空闲端口：" + err.Error()}
	}
	token := randomToken()
	cmd := l.command(port, token)
	hideWindow(cmd)
	// stdin 管道开着不关：sumika-models 带 --exit-on-stdin-eof，读到 EOF 就退。父进程不管怎么没的（崩了、被任务管理器杀了），
	// 管道都会关，不留下占着几百 MB 和一个端口的孤儿进程。不开这个管道，os/exec 会把子进程的 stdin 接到 NUL，一上来就是 EOF，它起来就退
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return onceResult{why: "建不了子进程的 stdin 管道：" + err.Error()}
	}
	defer stdin.Close()
	// stderr 不是另起 goroutine 读管道：cmd.Stderr 给的是 io.Writer，os/exec 自己拷、Wait 会等拷完才返回，
	// 进程写完就退时最后几行不会随管道一起被关掉
	tail := newTailBuffer(stderrTailLines, stderrTailBytes)
	cmd.Stderr = tail
	// 进程退出以后最多再等 3 秒让 stderr 拷完；孙进程还握着 stderr 句柄不放时，Wait 不会被它一直拖住
	cmd.WaitDelay = 3 * time.Second
	l.set(StatusStarting, "")
	if err := cmd.Start(); err != nil {
		why := fmt.Sprintf("起不来嵌入服务（%s：%v）", l.cfg.Exe, err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			why = fmt.Sprintf("找不到可执行文件：%s", why)
		}
		return onceResult{why: why}
	}
	l.mu.Lock()
	l.cmd = cmd
	l.mu.Unlock()
	exited := make(chan struct{})
	// 登记到运行页的进程表（只登记类别和标签，不带命令行和路径）；进程退出就撤销
	unregister := procreg.Register(procreg.CatModels, "本地模型", cmd.Process.Pid)
	go func() { cmd.Wait(); unregister(); close(exited) }()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	src := llm.Source{Name: "本机嵌入", BaseURL: base + "/v1/", APIKey: token}
	client := NewClient(src, l.cfg.Model)
	everReady := false
	lastWhy := "" // 还没就绪时最近一次探测没成的原因：超时、提前退出时带上
	t0 := time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			l.terminateAndWait(cmd, exited)
			l.set(StatusOff, "")
			return onceResult{healthy: everReady}
		case <-exited:
			if everReady {
				return onceResult{healthy: true}
			}
			return l.earlyExit(cmd, tail.String(), token, lastWhy)
		case <-tick.C:
			if !everReady {
				res := l.probe(ctx, base, token, client)
				switch {
				case res.ok:
					everReady = true
					tail.stop() // 就绪以后不再留 stderr
					l.mu.Lock()
					l.client, l.rerank, l.rerankNo = client, nil, res.rerankNo
					if l.cfg.RerankModel != "" && res.rerankNo == "" {
						l.rerank = NewRerankClient(src, l.cfg.RerankModel, 0)
					}
					l.mu.Unlock()
					l.set(StatusReady, "")
					l.logf("嵌入子进程就绪（%d 秒）", int(time.Since(t0)/time.Second))
					if res.rerankNo != "" {
						l.logf("嵌入子进程：%s；嵌入照常，memory_search 照原来的顺序", res.rerankNo)
					}
					tick.Reset(healthEvery)
				case res.wrong:
					// 服务起来了但不能用（模型 id 不在、kind 不对、令牌被拒、预热被拒）：留着也只是占内存，杀掉按退避重起。
					// id、kind 不对是配置问题，换一次也一样（res.fatal），不再重试
					l.set(StatusDown, res.why) // 先把状态和原因亮出来：杀进程树可能要一阵，不让它显示成还在启动
					l.terminateAndWait(cmd, exited)
					return onceResult{why: res.why, fatal: res.fatal}
				default:
					if res.why != "" {
						lastWhy = res.why
					}
					if time.Since(t0) > l.startupTimeout() {
						why := "嵌入子进程启动超时"
						if lastWhy != "" {
							why += "（" + lastWhy + "）"
						}
						why += stderrSuffix(tail.String(), token)
						l.set(StatusDown, why)
						l.terminateAndWait(cmd, exited)
						return onceResult{why: why}
					}
				}
			} else if !ping(base, token) {
				l.set(StatusDown, "嵌入子进程没有响应，正在重启")
				l.terminate(cmd)
			}
		}
	}
}

// killGrace 是发出"杀掉"以后最多等多久，还没退出就直接 Process.Kill；测试里缩短
var killGrace = 5 * time.Second

// terminate 发出"杀掉"，不等结果。sumika-models 的子进程没有孙进程，直接 Process.Kill，不依赖再起一个 taskkill.exe：
// 起进程这件事会被安全软件卡住甚至拦掉（内存防护一类），taskkill 又不报告成败
func (l *Local) terminate(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

// terminateAndWait 杀掉子进程并等它退出（exited 在 cmd.Wait 返回时关）。最多等 killGrace，还没退出就直接 Process.Kill 再等一轮；
// 还是不退就放弃等（写一行日志）：Stop 和重启不能因为一个杀不掉的进程永远卡在这里
func (l *Local) terminateAndWait(cmd *exec.Cmd, exited <-chan struct{}) {
	l.terminate(cmd)
	select {
	case <-exited:
		return
	case <-time.After(killGrace):
	}
	_ = cmd.Process.Kill()
	select {
	case <-exited:
		return
	case <-time.After(killGrace):
	}
	l.logf("嵌入子进程：杀了两次，%d 秒内都没退出（pid %d），不再等它", int(2*killGrace/time.Second), cmd.Process.Pid)
}

// earlyExit 是子进程在就绪前自己退出了：go-models 按退出码分（2 确定性的错误，3 端口被占），别的是一句猜因提示加 stderr 末尾
func (l *Local) earlyExit(cmd *exec.Cmd, tail, token, lastWhy string) onceResult {
	if cmd.ProcessState != nil {
		switch cmd.ProcessState.ExitCode() {
		case exitConfig:
			// 退出码 2 也是 Go 运行时 panic、fatal error（包括内存吃紧时的 out of memory）和原生崩溃的退出码。
			// sumika-models 自己拒绝启动时一定写了"[sumika-models] 错误："行（server 里所有退出码 2 的出口都经过 fail），
			// 没有这一行的退出码 2 不是配置问题：当普通的就绪前退出，退避重试。不然一次瞬时的内存吃紧就让嵌入永久不可用
			if lastModelsError(tail) != "" {
				return onceResult{fatal: true, why: fatalWhy(tail, token)}
			}
		case exitPortBusy:
			return onceResult{portBusy: true, why: fmt.Sprintf("嵌入服务的端口连着被占（换了 %d 个口都不行）", maxPortRetries) + stderrSuffix(tail, token)}
		}
	}
	return onceResult{why: earlyExitWhy(tail, token, lastWhy)}
}

// fatalWhy 是 sumika-models 用退出码 2 拒绝启动的原因：第一行带它 stderr 里最后一条 "[sumika-models] 错误：" 的内容（提醒只取第一行），
// 后面接 stderr 末尾（令牌抹掉）。只在 lastModelsError(tail) 不空时调（earlyExit 的判断）：没有那一行的退出码 2 不说"配置有问题"
func fatalWhy(tail, token string) string {
	msg := lastModelsError(tail)
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "[令牌]")
	}
	return "嵌入服务拒绝启动：" + msg + "（退出码 2，不再重试，改好配置再重启后台）" + stderrSuffix(tail, token)
}

// lastModelsError 是 stderr 末尾里最后一行带 [sumika-models] 错误： 前缀的内容（去掉前缀），没有就是空
func lastModelsError(tail string) string {
	lines := strings.Split(tail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), modelsErrPrefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func (l *Local) startupTimeout() time.Duration {
	if l.startup > 0 {
		return l.startup
	}
	return startupTimeout
}

// probeResult 是就绪判定一次探测的结果
type probeResult struct {
	ok bool // 三步都过了，可以就绪
	// rerankNo：嵌入这边都好了，但 memory.rerank.model 点名的重排模型服务里没有（或不是重排模型）。不影响就绪，只让重排用不了
	rerankNo string
	wrong    bool   // 服务答了，但答得不能用：不用再等，杀了重起
	fatal    bool   // wrong 之中换一次也一样的（go-models 下模型 id、kind 不对）：不再重试
	why      string // 没过的原因（ok 时为空）；wrong 为假又有原因的，是还没起来或这次没问成，下一秒再问
}

// probe 是就绪判定（W1 契约第 4 条）：后端自己的 /health 通过 → 配置的模型 id 在 /v1/models 里（响应带 kind 字段时还要是 embed，带 ready 字段时还要是真）
// → 一次预热嵌入成功。只看 /health 不够：模型 id 写错时 /health 照样 200，之后每次嵌入都 400。
// kind 只在响应带这个字段时才校验。
// 只有服务明确答了不对的（HTTP 错误、id 不在、kind 不对）才算 wrong；连不上、超时都当还没好，等下一次探测
func (l *Local) probe(ctx context.Context, base, token string, client *Client) probeResult {
	status, _, err := httpGet(ctx, base+"/health", token)
	switch {
	case err != nil:
		return probeResult{} // 还没起来
	case status == http.StatusUnauthorized:
		return probeResult{wrong: true, why: "嵌入服务拒绝了令牌（/health 回 401）"}
	case status != http.StatusOK:
		return probeResult{why: fmt.Sprintf("/health 回了 %d", status)}
	}
	status, body, err := httpGet(ctx, base+"/v1/models", token)
	switch {
	case err != nil:
		return probeResult{why: "/v1/models 没问成：" + err.Error()}
	case status == http.StatusUnauthorized:
		return probeResult{wrong: true, why: "嵌入服务拒绝了令牌（/v1/models 回 401）"}
	case status != http.StatusOK:
		return probeResult{wrong: true, why: fmt.Sprintf("/v1/models 回了 %d", status)}
	}
	if why, definite := checkModels(body, l.cfg.Model); why != "" {
		if definite {
			return probeResult{wrong: true, fatal: true, why: why + "。这是配置问题，换一次也一样，不再重试：改好 embed.model 再重启后台"}
		}
		return probeResult{wrong: true, why: why}
	}
	// 预热用库存那一种：请求带 input_type，顺便验证服务认得这个字段
	vs, err := client.EmbedTyped(ctx, TypeDocument, []string{warmText})
	if err != nil {
		var api *llm.StatusError
		if errors.As(err, &api) {
			if api.Code == http.StatusUnauthorized {
				return probeResult{wrong: true, why: "嵌入服务拒绝了令牌（预热嵌入回 401）"}
			}
			return probeResult{wrong: true, why: fmt.Sprintf("预热嵌入被服务拒绝（HTTP %d）", api.Code)}
		}
		return probeResult{why: "预热嵌入没成功：" + err.Error()}
	}
	if len(vs) != 1 || len(vs[0]) == 0 {
		return probeResult{wrong: true, why: "预热嵌入回的向量是空的"}
	}
	return probeResult{ok: true, rerankNo: l.checkRerankListed(body)}
}

// checkRerankListed：点名了重排模型时，看 /v1/models 里有没有它、是不是重排模型。没问题或不相干返回空串
func (l *Local) checkRerankListed(body []byte) string {
	if l.cfg.RerankModel == "" {
		return ""
	}
	var list struct {
		Data []struct {
			ID   string  `json:"id"`
			Kind *string `json:"kind"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &list) != nil {
		return ""
	}
	var have []string
	for _, m := range list.Data {
		have = append(have, m.ID)
		if m.ID != l.cfg.RerankModel {
			continue
		}
		if m.Kind != nil && *m.Kind != "rerank" {
			return fmt.Sprintf("memory.rerank.model 写的 %s 在服务里的 kind 是 %q，不是 rerank", l.cfg.RerankModel, *m.Kind)
		}
		return ""
	}
	return fmt.Sprintf("memory.rerank.model 写的 %s 服务里没有（服务里有：%s）", l.cfg.RerankModel, strings.Join(have, "、"))
}

// checkModels 看 /v1/models 的响应里有没有要用的模型 id；有 kind 字段时必须是 embed，有 ready 字段时必须是真。返回空串就是对的。
// definite 说这个问题换一次也一样（id 不在、kind 不对）；ready=false 是服务自己说这个模型现在用不了，不算
func checkModels(body []byte, model string) (why string, definite bool) {
	var list struct {
		Data []struct {
			ID    string  `json:"id"`
			Kind  *string `json:"kind"`
			Ready *bool   `json:"ready"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return "/v1/models 回的不是模型表", false
	}
	var have []string
	for _, m := range list.Data {
		have = append(have, m.ID)
		if m.ID != model {
			continue
		}
		if m.Kind != nil && *m.Kind != "embed" {
			return fmt.Sprintf("模型 id %s 在服务里的 kind 是 %q，不是 embed", model, *m.Kind), true
		}
		if m.Ready != nil && !*m.Ready {
			return fmt.Sprintf("模型 %s 在服务里 ready=false（文件缺了或哈希对不上）", model), false
		}
		return "", false
	}
	return fmt.Sprintf("模型 id 不在服务里：%s（服务里有：%s）", model, strings.Join(have, "、")), true
}

// pingClient 给 ping 和 probe 的 GET 用：3 秒一次
var pingClient = &http.Client{Timeout: 3 * time.Second}

// httpGet 带令牌 GET 一次，返回状态码和最多 1 MiB 的响应体
func httpGet(ctx context.Context, url, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := pingClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, b, err
}

func ping(base, token string) bool {
	status, _, err := httpGet(context.Background(), base+"/health", token)
	return err == nil && status == http.StatusOK
}

// earlyExitWhy 是子进程在就绪前就退出了的原因：stderr 末尾（令牌抹掉）和一句猜因提示
func earlyExitWhy(tail, token, lastWhy string) string {
	why := "嵌入子进程在就绪前退出了"
	if lastWhy != "" {
		why += "（" + lastWhy + "）"
	}
	return why + stderrSuffix(tail, token)
}

// stderrSuffix：附 stderr 末尾（令牌抹掉）；没有 stderr 就是空
func stderrSuffix(tail, token string) string {
	tail = strings.TrimSpace(tail)
	if tail == "" {
		return ""
	}
	if token != "" {
		tail = strings.ReplaceAll(tail, token, "[令牌]")
	}
	return "。最后几行 stderr 在下面\n" + tail
}

// tailBuffer 是只留最后几行、总共不超过 max 字节的 io.Writer（接在 cmd.Stderr 上）。stop 以后照收照丢，不再留。
// Write 永远成功：写 stderr 的子进程不能被我们卡住
type tailBuffer struct {
	mu       sync.Mutex
	maxLines int
	maxBytes int
	lines    []string
	partial  []byte // 还没收到换行的半行
	stopped  bool
}

func newTailBuffer(maxLines, maxBytes int) *tailBuffer {
	return &tailBuffer{maxLines: maxLines, maxBytes: maxBytes}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return len(p), nil
	}
	t.partial = append(t.partial, p...)
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		t.lines = append(t.lines, strings.TrimRight(string(t.partial[:i]), "\r"))
		t.partial = t.partial[i+1:]
	}
	if len(t.partial) > t.maxBytes { // 一直不换行的：只留末尾
		t.partial = t.partial[len(t.partial)-t.maxBytes:]
	}
	t.trim()
	return len(p), nil
}

// trim 丢掉最旧的行，直到不超过行数和字节上限（没收尾的半行算一行，也算进字节）
func (t *tailBuffer) trim() {
	limit := t.maxLines
	if len(t.partial) > 0 {
		limit--
	}
	if limit < 0 {
		limit = 0
	}
	if len(t.lines) > limit {
		t.lines = t.lines[len(t.lines)-limit:]
	}
	size := len(t.partial)
	for _, ln := range t.lines {
		size += len(ln) + 1
	}
	for size > t.maxBytes && len(t.lines) > 0 {
		size -= len(t.lines[0]) + 1
		t.lines = t.lines[1:]
	}
}

func (t *tailBuffer) stop() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
}

// String 是现在留着的几行（含没收尾的半行），总长不超过上限，不会切在一个汉字中间
func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := strings.Join(t.lines, "\n")
	if len(t.partial) > 0 {
		if out != "" {
			out += "\n"
		}
		out += string(t.partial)
	}
	if len(out) > t.maxBytes {
		out = out[len(out)-t.maxBytes:]
	}
	for len(out) > 0 && !utf8.RuneStart(out[0]) { // 切在汉字中间的开头几个字节不要
		out = out[1:]
	}
	return out
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
