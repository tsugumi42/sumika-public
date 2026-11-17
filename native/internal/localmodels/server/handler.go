package server

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/localmodels/pool"
)

// 默认值（规格第三节、2.5 表）。
const (
	DefaultMaxBodyBytes = int64(32) << 20 // 请求体上限 32 MiB，Content-Length 和实际字节都拦
	TokenEnv            = "SUMIKA_EMBED_TOKEN"
)

// Config 是 HTTP 层要的东西。
type Config struct {
	Pool *pool.Pool
	// Models 是这个进程提供的模型（--models 点名的和 --lazy-models 的）；Registered 是登记表里的全部 id，
	// 只用来区分 404 的两种原因："登记了但这次没点名"和"登记表里没有"。
	Models     []engine.Spec
	Registered []string
	Token      string // 必填
	ORTVersion string // /health 的 ort_version

	MaxBodyBytes int64                  // 0 = 32 MiB
	MaxRequest   time.Duration          // 一次请求最多算多久（--max-request-seconds）；0 = 不设
	Monitor      *Monitor               // nil = /health 的 run_elapsed_ms、last_ok_unix 恒为 0
	RSS          func() (uint64, error) // nil = pool.ProcessRSS；读不到就报 0
	Log          *log.Logger            // nil = 不写日志
}

// Server 是 HTTP 处理器。可以多个 goroutine 同时用。
type Server struct {
	pool       *pool.Pool
	served     map[string]engine.Spec
	servedIDs  []string // 按字母排好
	registered map[string]bool
	token      []byte
	ortVersion string
	maxBody    int64
	maxRequest time.Duration
	mon        *Monitor
	rss        func() (uint64, error)
	lg         *log.Logger
}

// New 建处理器。令牌必填：没有令牌就不该有一个能响应的服务（规格"禁止的偷懒写法"：令牌可选）。
func New(cfg Config) (*Server, error) {
	if cfg.Pool == nil {
		return nil, errors.New("server: 没有模型池")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("server: 令牌必填")
	}
	if len(cfg.Models) == 0 {
		return nil, errors.New("server: 没有提供任何模型")
	}
	s := &Server{
		pool:       cfg.Pool,
		served:     make(map[string]engine.Spec, len(cfg.Models)),
		registered: make(map[string]bool, len(cfg.Registered)),
		token:      []byte("Bearer " + cfg.Token),
		ortVersion: cfg.ORTVersion,
		maxBody:    cfg.MaxBodyBytes,
		maxRequest: cfg.MaxRequest,
		mon:        cfg.Monitor,
		rss:        cfg.RSS,
		lg:         cfg.Log,
	}
	if s.maxBody <= 0 {
		s.maxBody = DefaultMaxBodyBytes
	}
	if s.rss == nil {
		s.rss = pool.ProcessRSS
	}
	for _, spec := range cfg.Models {
		if _, dup := s.served[spec.ID]; dup {
			return nil, fmt.Errorf("server: %s 提供了两次", spec.ID)
		}
		s.served[spec.ID] = spec
		s.servedIDs = append(s.servedIDs, spec.ID)
	}
	sort.Strings(s.servedIDs)
	for _, id := range cfg.Registered {
		s.registered[id] = true
	}
	return s, nil
}

func (s *Server) logf(format string, a ...any) {
	if s.lg != nil {
		s.lg.Printf(format, a...)
	}
}

// Handler 是完整的处理链：panic 兜底 → 令牌 → 请求体上限 → 路由（规格 GM5"技术路线"的顺序，令牌最先，
// 连不存在的路径、OPTIONS 没带令牌也是 401，和 server.py 的中间件一样）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// 路由不带方法：方法不对时自己回 JSON 的 405（ServeMux 自带的 405 是纯文本）
	mux.HandleFunc("/health", onlyMethod(http.MethodGet, s.handleHealth))
	mux.HandleFunc("/v1/models", onlyMethod(http.MethodGet, s.handleModels))
	mux.HandleFunc("/v1/embeddings", onlyMethod(http.MethodPost, s.handleEmbeddings))
	mux.HandleFunc("/v1/rerank", onlyMethod(http.MethodPost, s.handleRerank))
	mux.HandleFunc("/v1/models/{id}/unload", onlyMethod(http.MethodPost, s.handleUnload))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, newAPIError(http.StatusNotFound, "Not Found"))
	})
	return s.recoverMW(s.tokenMW(s.bodyLimitMW(mux)))
}

func onlyMethod(method string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeError(w, newAPIError(http.StatusMethodNotAllowed, "Method Not Allowed"))
			return
		}
		h(w, r)
	}
}

// ---- 中间件 ----

// recoverMW 兜住处理中的 panic：回 500，日志只写 panic 值的类型和出事的函数名，不写值
// （值可能就是输入文字或带着输入文字的错误）。
func (s *Server) recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec) // net/http 约定的"中止这个连接"，不是 bug，原样交回去
			}
			s.logf("PANIC type=%T func=%s", rec, panicSite())
			writeError(w, newAPIError(http.StatusInternalServerError, "Internal server error"))
		}()
		next.ServeHTTP(w, r)
	})
}

// panicSite 找出 panic 发生的函数：栈里 runtime.gopanic 之后第一个不属于 runtime 的帧。
func panicSite() string {
	pcs := make([]uintptr, 48)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	seenPanic := false
	for {
		f, more := frames.Next()
		switch {
		case f.Function == "runtime.gopanic":
			seenPanic = true
		case seenPanic && f.Function != "" && !strings.HasPrefix(f.Function, "runtime."):
			return f.Function
		}
		if !more {
			return "unknown"
		}
	}
}

// tokenMW：每个请求（含 /health）都要 Authorization: Bearer <令牌>。比较整个头的值，常量时间。
func (s *Server) tokenMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), s.token) != 1 {
			writeError(w, &apiError{
				status: http.StatusUnauthorized,
				detail: "Missing or wrong Authorization header; send 'Bearer <" + TokenEnv + ">'",
				header: http.Header{"Www-Authenticate": {"Bearer"}},
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bodyLimitMW：Content-Length 超了就 413，不读正文；没写 Content-Length 的（分块）靠 MaxBytesReader 在读的时候拦。
// 真正读正文在 readBody，先于 JSON 解析和分词。
func (s *Server) bodyLimitMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > s.maxBody {
			w.Header().Set("Connection", "close") // 正文不读了，别让这条连接再被拿去发下一个请求
			writeError(w, newAPIError(http.StatusRequestEntityTooLarge,
				fmt.Sprintf("Request body is larger than %d bytes (Content-Length %d)", s.maxBody, r.ContentLength)))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
		next.ServeHTTP(w, r)
	})
}

// readBody 读完正文（最多 maxBody 字节，不是先读完再判大小）。读正文期间给连接设个读截止（跟 --max-request-seconds），
// 防慢速滴灌的正文一直占着连接。不用自己清：net/http 读到正文结尾、开始盯连接是否断开之前会自己把读截止清掉
// （实测：不清这个截止，TestMaxRequestSeconds 的 504 照样是 504，没有变成 499）。
func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, *apiError) {
	rc := http.NewResponseController(w)
	if s.maxRequest > 0 {
		_ = rc.SetReadDeadline(time.Now().Add(s.maxRequest))
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, newAPIError(http.StatusRequestEntityTooLarge, fmt.Sprintf("Request body is larger than %d bytes", s.maxBody))
		}
		return nil, newAPIError(http.StatusBadRequest, "Failed to read the request body")
	}
	return body, nil
}

// requestContext 是这次请求用的 ctx：客户端断开时取消（net/http 在正文读完后盯着连接），加上 --max-request-seconds。
func (s *Server) requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if s.maxRequest > 0 {
		return context.WithTimeout(parent, s.maxRequest)
	}
	return context.WithCancel(parent)
}

// ---- 模型查找和错误映射 ----

// lookup 按 id 和用途找登记项：不认识、种类不对都是 404。
func (s *Server) lookup(id string, kind engine.Kind) (engine.Spec, *apiError) {
	spec, ok := s.served[id]
	if !ok {
		return engine.Spec{}, newAPIError(http.StatusNotFound, s.unknownDetail(id))
	}
	if spec.Kind != kind {
		return engine.Spec{}, newAPIError(http.StatusNotFound, fmt.Sprintf("Model '%s' is a %s model, not %s", clip(id), spec.Kind, kind))
	}
	return spec, nil
}

func (s *Server) unknownDetail(id string) string {
	avail := joinList(s.servedIDs)
	if s.registered[id] {
		return fmt.Sprintf("Model '%s' is registered but not selected in this process (--models / --lazy-models); available: %s", clip(id), avail)
	}
	return fmt.Sprintf("Unknown model '%s'; available: %s", clip(id), avail)
}

// fail 把计算中的错误翻成响应。顺序有讲究：*pool.Error 先于 ctx 错误（池关闭时取消加载，错误里会包着 Canceled，
// 但那是 503 / 500，不是调用方走了）。
func (s *Server) fail(w http.ResponseWriter, model string, started time.Time, err error) {
	var (
		reqErr  *engine.RequestError
		poolErr *pool.Error
	)
	switch {
	case errors.As(err, &reqErr):
		writeError(w, newAPIError(http.StatusBadRequest, reqErr.Detail))
	case errors.As(err, &poolErr):
		if poolErr.Status >= 500 && poolErr.Status != http.StatusServiceUnavailable {
			s.logf("FAILED model=%s status=%d detail=%s", model, poolErr.Status, poolErr.Detail)
		}
		writeError(w, newAPIError(poolErr.Status, poolErr.Detail))
	case errors.Is(err, context.DeadlineExceeded):
		s.logf("STOPPED model=%s reason=timeout ms=%d", model, time.Since(started).Milliseconds())
		writeError(w, newAPIError(http.StatusGatewayTimeout, fmt.Sprintf(
			"Gave up after --max-request-seconds %g: the batch did not finish. Send fewer inputs or raise the limit", s.maxRequest.Seconds())))
	case errors.Is(err, context.Canceled):
		// 客户端已经走了：499 是 nginx 那个"客户端关了连接"的码，没人收得到这个响应
		s.logf("STOPPED model=%s reason=disconnect ms=%d", model, time.Since(started).Milliseconds())
		writeError(w, newAPIError(499, "Client disconnected"))
	default:
		// 引擎和分词器的错误里没有输入文字（GM3 的约定），可以回原因
		s.logf("FAILED model=%s status=500 detail=%s", model, err.Error())
		writeError(w, newAPIError(http.StatusInternalServerError, err.Error()))
	}
}

// ---- 路由 ----

type healthModel struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Dimension     int    `json:"dimension"`
	MaxTokens     int    `json:"max_tokens"`
	Matryoshka    bool   `json:"matryoshka"`
	Loaded        bool   `json:"loaded"`
	LoadMs        *int64 `json:"load_ms"`          // 没加载是 null，不是 0
	LastUsedMsAgo *int64 `json:"last_used_ms_ago"` // 同上
}

type healthResponse struct {
	Status        string        `json:"status"`
	Engine        string        `json:"engine"`
	ORTVersion    string        `json:"ort_version"`
	Models        []healthModel `json:"models"`
	Loaded        []string      `json:"loaded"`
	ResidentBytes uint64        `json:"resident_bytes"` // 记账里的估计值
	RSSBytes      uint64        `json:"rss_bytes"`      // 进程真实常驻内存；读不到是 0
	MaxLoaded     int           `json:"max_loaded"`
	RunElapsedMs  int64         `json:"run_elapsed_ms"` // 当前跑得最久的那次前向已经跑了多久，没有是 0
	LastOKUnix    int64         `json:"last_ok_unix"`   // 上一次成功的前向结束的 Unix 秒，还没有是 0
}

// handleHealth 只读池的一份快照，不等池锁、不等模型锁：Local 对已就绪的进程每 30 秒 ping 一次、超时 3 秒、
// 失败一次就杀进程，/health 被一次加载或一次长前向挡住就会误杀健康的进程。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	snap := s.pool.Snapshot()
	rss, err := s.rss()
	if err != nil {
		rss = 0
	}
	resp := healthResponse{
		Status: "ok", Engine: "go", ORTVersion: s.ortVersion,
		Models: make([]healthModel, 0, len(snap.Models)), Loaded: []string{},
		ResidentBytes: snap.ResidentBytes, RSSBytes: rss, MaxLoaded: snap.MaxLoaded,
	}
	for _, m := range snap.Models {
		row := healthModel{ID: m.ID, Kind: string(m.Kind), Dimension: m.Dimension, MaxTokens: m.MaxTokens, Matryoshka: m.Matryoshka, Loaded: m.Loaded}
		if m.Loaded {
			loadMs, ago := m.LoadMs, m.LastUsedMsAgo
			row.LoadMs, row.LastUsedMsAgo = &loadMs, &ago
			resp.Loaded = append(resp.Loaded, m.ID)
		}
		resp.Models = append(resp.Models, row)
	}
	if s.mon != nil {
		resp.RunElapsedMs = s.mon.Longest().Milliseconds()
		resp.LastOKUnix = s.mon.LastOKUnix()
	}
	writeJSON(w, http.StatusOK, resp)
}

type modelRow struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	OwnedBy   string `json:"owned_by"`
	Kind      string `json:"kind"`
	Dimension int    `json:"dimension"`
	MaxTokens int    `json:"max_tokens"`
	Loaded    bool   `json:"loaded"`
	Ready     bool   `json:"ready"` // Go 版多的一个字段：文件在、（--models 里的）哈希对
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	snap := s.pool.Snapshot()
	rows := make([]modelRow, 0, len(snap.Models))
	for _, m := range snap.Models {
		rows = append(rows, modelRow{
			ID: m.ID, Object: "model", OwnedBy: "local", Kind: string(m.Kind),
			Dimension: m.Dimension, MaxTokens: m.MaxTokens, Loaded: m.Loaded, Ready: m.Ready,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": rows})
}

func (s *Server) handleUnload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.served[id]; !ok {
		writeError(w, newAPIError(http.StatusNotFound, s.unknownDetail(id)))
		return
	}
	unloaded, reason := s.pool.Unload(id)
	writeJSON(w, http.StatusOK, map[string]any{"model": id, "unloaded": unloaded, "reason": reason})
}

type embeddingItem struct {
	Object    string `json:"object"`
	Index     int    `json:"index"`
	Embedding any    `json:"embedding"` // []float32，或 base64 串
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	raw, apiErr := s.readBody(w, r)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	body, apiErr := parseEmbeddings(raw)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	spec, apiErr := s.lookup(body.Model, engine.KindEmbed)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	req := engine.EmbedRequest{Texts: body.Input, InputType: body.InputType, Task: body.Task, Dimensions: body.Dimensions}
	// 先验再取租约：这些检查不碰模型，坏请求不为了回 400 去加载模型，池满时也回 400
	if err := engine.CheckEmbedRequest(spec, req); err != nil {
		writeError(w, newAPIError(http.StatusBadRequest, err.Error()))
		return
	}

	started := time.Now()
	ctx, cancel := s.requestContext(r.Context())
	defer cancel()
	lease, err := s.pool.Acquire(ctx, spec.ID)
	if err != nil {
		s.fail(w, spec.ID, started, err)
		return
	}
	defer lease.Release()
	res, err := lease.Embedder().Embed(ctx, req)
	if err != nil {
		s.fail(w, spec.ID, started, err)
		return
	}

	s.logf("EMBED model=%s inputs=%d tokens=%d cache_hits=%d dimensions=%d ms=%d",
		spec.ID, len(req.Texts), res.Tokens, res.CacheHits, len(res.Vectors[0]), time.Since(started).Milliseconds())
	items := make([]embeddingItem, len(res.Vectors))
	for i, v := range res.Vectors {
		items[i] = embeddingItem{Object: "embedding", Index: i}
		if body.EncodingFormat == "base64" {
			items[i].Embedding = encodeBase64(v)
		} else {
			items[i].Embedding = v // []float32：encoding/json 按 float32 输出最短表示，客户端 float32() 还原得到同一个值
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list", "data": items, "model": spec.ID,
		"usage": map[string]int{"prompt_tokens": res.Tokens, "total_tokens": res.Tokens},
	})
}

// encodeBase64 是小端 float32 打包再 base64（和 OpenAI 一样）。
func encodeBase64(v []float32) string {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(x))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

type rerankItem struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

func (s *Server) handleRerank(w http.ResponseWriter, r *http.Request) {
	raw, apiErr := s.readBody(w, r)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	body, apiErr := parseRerank(raw)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	spec, apiErr := s.lookup(body.Model, engine.KindRerank)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}
	req := engine.RerankRequest{Query: body.Query, Documents: body.Documents, TopN: body.TopN}
	if err := engine.CheckRerankRequest(spec, req); err != nil {
		writeError(w, newAPIError(http.StatusBadRequest, err.Error()))
		return
	}

	started := time.Now()
	ctx, cancel := s.requestContext(r.Context())
	defer cancel()
	lease, err := s.pool.Acquire(ctx, spec.ID)
	if err != nil {
		s.fail(w, spec.ID, started, err)
		return
	}
	defer lease.Release()
	res, err := lease.Reranker().Rerank(ctx, req)
	if err != nil {
		s.fail(w, spec.ID, started, err)
		return
	}

	s.logf("RERANK model=%s documents=%d tokens=%d top_n=%d ms=%d",
		spec.ID, len(req.Documents), res.Tokens, len(res.Results), time.Since(started).Milliseconds())
	items := make([]rerankItem, len(res.Results))
	for i, it := range res.Results {
		items[i] = rerankItem{Index: it.Index, RelevanceScore: it.Score}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model": spec.ID, "results": items, "usage": map[string]int{"total_tokens": res.Tokens},
	})
}
