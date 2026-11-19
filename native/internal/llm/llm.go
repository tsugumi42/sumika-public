// Package llm 是 OpenAI 兼容接口的薄客户端（规格-Go后台 二、四点五；读码结论见 调研-Go做壳和后台的取舍 第八节）：
// 只用标准库的 net/http 和 encoding/json，不依赖官方 SDK。模型都走 OpenAI 兼容接口（默认 Magpie 本机网关），对话循环自己写，这里只管"列模型"和"发一次请求、把流读成事件"。
// 不保存任何历史：每次调用的消息由调用方给（存的历史和发出去的请求分开，见 拆解-Crush与kubectl-ai-架构 四）。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/llm/media"
	"sumika-native/internal/llm/openaichat"
	"sumika-native/internal/modelref"
	"time"
)

// Source 是一个 OpenAI 兼容的模型来源（ADR-142）
type Source struct {
	Name    string // 显示名：日志、报错里用
	BaseURL string
	APIKey  string // 本机网关不查 key 也要给一个
	Hint    string // 连不上时报错后面接的一句
	Dialect Dialect
}

// Dialect 是一个服务商在 OpenAI 兼容线上的专属行为（规格-模型配置 3.1）：通用结构只放各家的共同字段，
// 多给的和专属的开关放这里，按服务商打开。零值就是纯 OpenAI 兼容，不读别家多给的东西。
type Dialect struct {
	// RouteHeaders：响应头 x-magpie-provider / x-magpie-model 表示网关实际落到的渠道和模型
	RouteHeaders bool
}

// MagpieDialect 是 Magpie 网关的方言：读 x-magpie-* 响应头，别的服务商不读
var MagpieDialect = Dialect{RouteHeaders: true}

// Magpie 是本机模型网关（127.0.0.1:3425），不查 key；但它按 key 里 "magpie-" 后面那段认客户端
// （Magpie gateway.go 的 agentOf），写成 magpie-sumika 用量页才记成 sumika，不是 local
func Magpie(baseURL string) Source {
	return Source{Name: "Magpie", BaseURL: baseURL, APIKey: "magpie-sumika", Hint: "请确认 Magpie 已经打开。", Dialect: MagpieDialect}
}

// Model 是 /models 列表里的一项：OpenAI 兼容的字段，加上服务商（例如 Magpie）多给的长度、能不能看图、思考档位
type Model struct {
	ID          string `json:"id"`
	OwnedBy     string `json:"owned_by"`
	DisplayName string `json:"display_name"`
	// Label 是服务商多给的显示名/渠道标签（Magpie 的 magpie_label）；别家没有就是空。对所有服务商都可读
	Label           string   `json:"magpie_label"`
	ReasoningLevels []string `json:"reasoning_levels"`
	ContextLength   int      `json:"context_length"`
	ContextWindow   int      `json:"context_window"`
	MaxInputTokens  int      `json:"max_input_tokens"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	Modalities      *struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Reasoning                *bool `json:"reasoning"`
	SupportedReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`

	// Limits 不是服务商给的：窗口和单次输出每个字段是谁给的、各方说了多少，由 modelmeta 在取回模型列表后填
	// （用户覆盖 > 服务商 > models.dev > 保守默认，ADR-334）。没填时（测试里手拼的模型）按"服务商报了就算服务商"读
	Limits LimitSources `json:"-"`

	// Provider 是这个模型属于哪个服务商（规格-模型配置 3.2）：不是线上字段，由装配处在取回模型列表后按来源填。
	// 空 = 没有服务商概念的地方（测试里手拼的模型、单独的小工具），引用就是 ID 本身
	Provider string `json:"-"`
}

// Ref 是模型的规范引用 `服务商ID/模型ID`：冷却、trace、用量和请求里认的模型标识都用它，
// 同名模型在两家服务商不会混在一起。发给服务商的 model 字段是不带前缀的 ID（Router 去掉）
func (m Model) Ref() string { return modelref.Ref{Provider: m.Provider, Model: m.ID}.String() }

// Image is an image carried in memory for a model request.
// 别名到叶子包：tools 产出图，不能反过来 import llm（llm 要 import chat，chat import tools）。
type Image = media.Image

// SupportsImages requires an explicit image input modality.
func (m Model) SupportsImages() bool {
	if m.Modalities != nil {
		for _, input := range m.Modalities.Input {
			if input == "image" {
				return true
			}
		}
	}
	return false
}

// FallbackMaxOutput 是模型没报单次输出上限时的保守默认（opencode 的值；报告里的 4096 正是实跑里思考吃光输出的数）。
// 调用端还会再按窗口余量往下压，所以不会超窗口
const FallbackMaxOutput = 32000

// Window 是上下文长度：几个字段谁有用谁，都没有按 128000（照 TS 版的 FALLBACK）
func (m Model) Window() int {
	for _, n := range []int{m.ContextLength, m.ContextWindow, m.MaxInputTokens} {
		if n > 0 {
			return n
		}
	}
	return 128_000
}

// Client 调一个来源
type Client struct {
	src  Source
	http *http.Client
	// Idle：连接上一直没有动静（连响应头都没等到，或者流到一半一个字节都没来）多久就断开，算一时的错误（IdleError）；0 是不管。
	// 按字节算，SSE 的注释心跳（Magpie 每 15 秒一句 ": keepalive"）也算动静。http.Client 不设总超时（回复可能很长），
	// 卡住的连接只能靠它；默认 5 分钟，照 pi 的 httpIdleTimeoutMs、omp 的流空闲看门狗，也和 Magpie 自己不再发心跳的时限一样
	Idle time.Duration
	// FirstContent、ContentIdle 是"内容空闲"看门狗（和 Idle 并存）：只有真正的内容才算进展——正文、思考（含结构化思考）、
	// 工具调用的名字和参数增量；SSE 注释心跳、空事件、只有用量的块都不算。FirstContent 是从发出请求到第一段内容的上限，
	// ContentIdle 是两段内容之间的最长间隔，超了算一时的错误（IdleError，Kind 是 IdleFirstContent / IdleContentGap），0 是不管。
	// 为什么要有：Idle 按字节算，Magpie 每 15 秒一句 ": keepalive"，上游一个字不给时连接看起来一直活着，要等到网关自己放弃（约 10 分钟）。
	// 做法照 omp 的 streamFirstEventTimeoutMs / streamIdleTimeoutMs（首个语义事件一个时限、之后事件间隔一个时限，0 关掉）；
	// Claude Code 的 CLAUDE_STREAM_IDLE_TIMEOUT_MS 反过来把网关心跳当成线上有数据，pi 的 httpIdleTimeoutMs 也按字节，都不能治这个病。
	// 默认都是 3 分钟；先想很久又不流式返回思考的模型，要在配置里调大（agent.firstContentSeconds、agent.contentIdleSeconds）。
	FirstContent, ContentIdle time.Duration
	// ModelsTimeout 是列模型这一个请求的时限；0 = 按来源地址判：回环 3 秒、其他 10 秒（规格-模型配置 6.1）。
	// 只管列模型，对话请求的时限照旧由 context 和空闲看门狗管。测试调短
	ModelsTimeout time.Duration
	// IdleTimer 是 Idle 看门狗用的定时器，空就用真的；测试换成手动触发、能数重置次数的（不靠墙钟）
	IdleTimer func(time.Duration, func()) WatchTimer
	// afterFunc 是内容看门狗用的定时器，测试换成能手动触发、能数重置次数的（不靠墙钟）
	afterFunc func(time.Duration, func()) watchTimer
}

// WatchTimer 是看门狗定时器要的两个方法（*time.Timer 就是）
type WatchTimer = watchTimer

// watchTimer 是看门狗定时器要的两个方法（*time.Timer 就是）
type watchTimer interface {
	Reset(time.Duration) bool
	Stop() bool
}

// DefaultContentIdle 是首个内容、内容间隔的默认时限
const DefaultContentIdle = 180 * time.Second

// New 建一个调用某个来源的客户端。这里不重试：重试由调用方管（规格 8.2 A7，照 embed.go）
func New(src Source) *Client {
	hc := &http.Client{Timeout: 0, Transport: tapTransport{http.DefaultTransport}} // 流式回复可能很长：超时由每次调用的 context 和 Idle 管
	return &Client{src: src, http: hc, Idle: 5 * time.Minute, FirstContent: DefaultContentIdle, ContentIdle: DefaultContentIdle}
}

// tap 是一次请求在连接上的动静：回复头到了、回复体每读到字节（包括不交给调用方的注释心跳、event 行）都叫一声 touch
type tap struct{ touch func() }

type tapKey struct{}

// tapTransport 包住真正的 Transport：请求的 context 里带着 tap 的才看（列模型这些不带）
type tapTransport struct{ base http.RoundTripper }

func (t tapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	tp, _ := req.Context().Value(tapKey{}).(*tap)
	if err != nil || tp == nil {
		return resp, err
	}
	tp.touch()
	resp.Body = &tapBody{ReadCloser: resp.Body, tap: tp}
	return resp, nil
}

type tapBody struct {
	io.ReadCloser
	tap *tap
}

func (b *tapBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.tap.touch()
	}
	return n, err
}

// 列模型的时限和大小上限（规格-模型配置 第六节：探测回环 3 秒、其他 10 秒；目录响应有上限）。
// 只管列模型这一个请求，对话请求的时限不动
const (
	ModelsLocalTimeout  = 3 * time.Second
	ModelsRemoteTimeout = 10 * time.Second
	ModelsMaxBytes      = 8 << 20
)

// Source 是这个客户端连的来源（缓存按它认：地址变了旧缓存不用）
func (c *Client) Source() Source { return c.src }

// modelsTimeout 按来源地址定列模型的时限：回环（127.0.0.0/8、::1、localhost）3 秒，其余 10 秒
func modelsTimeout(baseURL string) time.Duration {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ModelsRemoteTimeout
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return ModelsLocalTimeout
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ModelsLocalTimeout
	}
	return ModelsRemoteTimeout
}

// Models 列模型（GET /models）。Magpie 多给的字段也在 Model 里。
// 时限只管这一个请求（c.ModelsTimeout，0 按地址）；响应体超过 ModelsMaxBytes 报"模型列表太大"，不整份读进来
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	timeout := c.ModelsTimeout
	if timeout <= 0 {
		timeout = modelsTimeout(c.src.BaseURL)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.src.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.src.APIKey)
	res, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s 的 /models 超时：%s 内没回应（%w）", c.src.Name, timeout, err)
		}
		return nil, fmt.Errorf("连不上 %s（%s）：%w。%s", c.src.Name, c.src.BaseURL, err, c.src.Hint)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 的 /models 返回 %d", c.src.Name, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, ModelsMaxBytes+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s 的 /models 超时：%s 内没回应（%w）", c.src.Name, timeout, err)
		}
		return nil, fmt.Errorf("%s 的 /models 读不懂：%w", c.src.Name, err)
	}
	if len(body) > ModelsMaxBytes {
		return nil, fmt.Errorf("%s 的 /models 模型列表太大（超过 %d MB）", c.src.Name, ModelsMaxBytes>>20)
	}
	var list struct {
		Data []Model `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("%s 的 /models 读不懂：%w", c.src.Name, err)
	}
	return list.Data, nil
}

// Usage 是一次调用的用量，口径照 pi（openai-completions.js parseChunkUsage）：
// Input 是没命中缓存的输入（prompt_tokens 减去缓存读、缓存写），总输入 = Input + CacheRead + CacheWrite；
// Output 已经含 Reasoning
type Usage struct {
	Input, Output, CacheRead, CacheWrite, Reasoning int64
}

// Route 是网关实际落到的渠道和模型（响应头 x-magpie-provider / x-magpie-model）
type Route struct {
	Provider, Model string
}

// Event 是流式读出来的一件事：文本增量、思考增量、用量
type Event struct {
	Text, Reasoning string
	Usage           *Usage
}

// Result 是一次调用的结果（事件已经一路交给 onEvent，这里是汇总）
type Result struct {
	Text, Reasoning string
	// reasoningField 是思考从哪个字段来的（reasoning_content / reasoning / reasoning_text，第一段思考的那个）：
	// 下一次请求把这一步的思考按同一个字段带回去（pi 记成思考块的 signature，openai-completions.js 985 行）
	reasoningField string
	// reasoningDetails 是合并好的结构化思考（没有是空）：下一次请求原样带回 reasoning_details，这时不再按 reasoningField 带思考文字
	reasoningDetails []jsjson.Value
	// Replay 是中立格式的思考回放（reasoningField、reasoningDetails 收进去）
	Replay chat.Replay
	// ToolCalls 是模型要调的工具，按第一次出现的先后（pi：按 index、再按 id 找已有的那个，都没有才新开一个）
	ToolCalls []ToolCall
	// Stop 是为什么停："stop" 说完了、"length" 到长度上限被截断（不算错，调用方决定怎么办）、"toolUse" 要调工具
	Stop  string
	Usage Usage
	// UsageSeen：收到过用量那一块（说到一半被停下、请求没发出去的没有；pi 这时 usage.reasoning 是空的，记成 NULL）
	UsageSeen bool
	Route     Route
	FirstByte time.Duration // 从发出到响应头回来（0 是没等到）
	// FirstContent 是从发出到第一段真内容（正文、思考、工具调用，和内容看门狗同一个算法）；只调工具的一步没有 FirstToken，但有它
	FirstContent time.Duration
	FirstToken   time.Duration // 从发出到第一个字（或第一段思考）
	Total        time.Duration
	// Prompt 是这一次真正发出去的 messages、tools 的指纹（PromptHashes；拼不出请求时是空），记进 model_runs.prompt_hashes_json
	Prompt string
}

// elapsedSince 是到现在过了多久，至少 1 纳秒：Windows 上 Go 的时钟精度粗，很快的调用量出来正好是 0，而这几个字段的 0 表示“没等到”。
func elapsedSince(start time.Time) time.Duration { return max(time.Since(start), 1) }

// ToolCall 是一次工具调用：参数是拼好的原文（还没解析）
type ToolCall struct {
	ID, Name, Args string
}

// StreamChat 发一次中立的对话请求：openaichat 拼出请求体，之后走发送、读流、看门狗、重试判断的路。
func (c *Client) StreamChat(ctx context.Context, r chat.Request, onEvent func(Event)) (Result, error) {
	body, messages, tools, err := openaichat.EncodeRequest(r)
	if err != nil {
		return Result{}, LocalError(fmt.Errorf("%s 调用 %s 出错：拼不出请求：%w", c.src.Name, r.Model, err))
	}
	res, err := c.stream(ctx, streamRequest{body: body, Model: r.Model, headers: r.Headers, prompt: promptHashes(messages, tools)}, onEvent)
	return res, err
}

// streamRequest 是一次发送的参数：体已经拼好，这里只管发和读。
type streamRequest struct {
	body    []byte
	Model   string
	headers map[string]string
	prompt  string
}

// reservedHeaders 是调用方不许设置的请求头（规格-模型配置 3.1 的 headers 一条）：由客户端自己填，配置里出现就报本地错误
var reservedHeaders = []string{"Authorization", "Host", "Content-Length", "Content-Type", "Accept"}

// reservedInHeaders 返回 Headers 里不许设置的名字（不分大小写），排好序好让报错稳定
func reservedInHeaders(headers map[string]string) []string {
	var bad []string
	for k := range headers {
		for _, r := range reservedHeaders {
			if strings.EqualFold(k, r) {
				bad = append(bad, k)
				break
			}
		}
	}
	sort.Strings(bad)
	return bad
}

func (c *Client) stream(ctx context.Context, sr streamRequest, onEvent func(Event)) (Result, error) {
	r := sr // 下面的报错和看门狗文案沿用旧的 r.Model 写法
	res := Result{Prompt: sr.prompt}
	if bad := reservedInHeaders(sr.headers); len(bad) > 0 {
		return res, LocalError(fmt.Errorf("%s 调用 %s 出错：请求头 %s 不能由配置设置", c.src.Name, r.Model, strings.Join(bad, "、")))
	}
	start := time.Now()
	// 看门狗：连接上超过 c.Idle 没有动静就断开这一次（原因记成 IdleError，和被停下分开）
	cctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	tp := &tap{touch: func() {}}
	var idle watchTimer
	if c.Idle > 0 {
		newIdle := c.IdleTimer
		if newIdle == nil {
			newIdle = func(d time.Duration, f func()) watchTimer { return time.AfterFunc(d, f) }
		}
		idle = newIdle(c.Idle, func() { cancel(&IdleError{After: c.Idle}) })
		defer idle.Stop()
		tp.touch = func() { idle.Reset(c.Idle) }
	}
	// 内容看门狗：第一段内容前按 FirstContent 计，之后每段内容把 ContentIdle 重新计；心跳不走这里
	after := c.afterFunc
	if after == nil {
		after = func(d time.Duration, f func()) watchTimer { return time.AfterFunc(d, f) }
	}
	var contentTimer watchTimer
	gotContent := false
	stopContent := func() {
		if contentTimer != nil {
			contentTimer.Stop()
		}
	}
	defer stopContent()
	if c.FirstContent > 0 {
		d := c.FirstContent
		contentTimer = after(d, func() { cancel(&IdleError{After: d, Kind: IdleFirstContent}) })
	}
	contentSeen := func() {
		if !gotContent {
			gotContent = true
			stopContent()
			contentTimer = nil
			if c.ContentIdle > 0 {
				d := c.ContentIdle
				contentTimer = after(d, func() { cancel(&IdleError{After: d, Kind: IdleContentGap}) })
			}
			return
		}
		if contentTimer != nil {
			contentTimer.Reset(c.ContentIdle)
		}
	}
	req, err := http.NewRequestWithContext(context.WithValue(cctx, tapKey{}, tp), http.MethodPost, strings.TrimRight(c.src.BaseURL, "/")+"/chat/completions", bytes.NewReader(sr.body))
	if err != nil {
		return res, LocalError(fmt.Errorf("%s 调用 %s 出错：%w", c.src.Name, r.Model, err))
	}
	req.Header.Set("Authorization", "Bearer "+c.src.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "sumika")
	for k, v := range sr.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err == nil {
		res.FirstByte = elapsedSince(start)
	}
	if err != nil {
		// 被停下、看门狗断开、连不上：分开记（和读流中途出的错一个规矩）
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		var ie *IdleError
		if errors.As(context.Cause(cctx), &ie) {
			return res, fmt.Errorf("%s 调用 %s 出错：%w", c.src.Name, r.Model, ie)
		}
		return res, fmt.Errorf("%s 调用 %s 出错：%w", c.src.Name, r.Model, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		// 回了 4xx、5xx：带上状态码、头（重试看 Retry-After、x-should-retry）和服务商说的原因
		raw := errorBody(resp)
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		var ie *IdleError
		if errors.As(context.Cause(cctx), &ie) {
			return res, fmt.Errorf("%s 调用 %s 出错：%w", c.src.Name, r.Model, ie)
		}
		err = &StatusError{Code: resp.StatusCode, Header: resp.Header, Body: errorMessage([]byte(raw)), RawBody: raw}
		return res, fmt.Errorf("%s 调用 %s 出错：%w", c.src.Name, r.Model, err)
	}
	out, decodeErr := openaichat.Decode(resp.Body, func(ev openaichat.Event) {
		if ev.Progress {
			contentSeen()
			if res.FirstContent == 0 {
				res.FirstContent = elapsedSince(start)
			}
		}
		if ev.Usage != nil {
			res.UsageSeen = true
		}
		if (ev.Text != "" || ev.Thinking != "") && res.FirstToken == 0 {
			res.FirstToken = elapsedSince(start)
		}
		if onEvent != nil && (ev.Text != "" || ev.Thinking != "" || ev.Usage != nil) {
			var usage *Usage
			if ev.Usage != nil {
				usage = &Usage{Input: ev.Usage.Input, Output: ev.Usage.Output, CacheRead: ev.Usage.CacheRead, CacheWrite: ev.Usage.CacheWrite, Reasoning: ev.Usage.Reasoning}
			}
			onEvent(Event{Text: ev.Text, Reasoning: ev.Thinking, Usage: usage})
		}
	})
	stopContent()
	if idle != nil {
		idle.Stop() // 流读完了：看门狗不再算（不然读完以后刚好到点会被当成卡住）
	}
	res.Total = elapsedSince(start)
	res.applyDecoded(out, r.Model)
	if c.src.Dialect.RouteHeaders {
		res.Route = Route{Provider: resp.Header.Get("x-magpie-provider"), Model: resp.Header.Get("x-magpie-model")}
	}
	// 流读完了才发现被停下（pi 读完以后还看一次 signal.aborted）：照样算停下
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if decodeErr != nil {
		// 结束原因已经收到了（不认识的 reason 也是收到了）就不看门狗：旧写法这时把完整回复扔掉重来
		var stop *openaichat.StopError
		if !(errors.As(decodeErr, &stop) && stop.Reason != "") {
			var ie *IdleError
			if errors.As(context.Cause(cctx), &ie) {
				return res, fmt.Errorf("%s 调用 %s 出错：%w", c.src.Name, r.Model, ie)
			}
		}
		// 被停下、读流中途出错分开记（和旧写法一个规矩）
		if errors.Is(decodeErr, context.Canceled) {
			return res, decodeErr
		}
		var se *openaichat.StreamError
		if errors.As(decodeErr, &se) {
			decodeErr = &StreamError{Message: se.Message, Data: se.Data, Header: resp.Header}
		}
		var oe *openaichat.StopError
		if errors.As(decodeErr, &oe) {
			decodeErr = &StopError{Source: c.src.Name, Model: r.Model, Reason: oe.Reason}
		}
		var ne *openaichat.NonStreamError
		if errors.As(decodeErr, &ne) {
			decodeErr = &StatusError{Code: resp.StatusCode, Header: resp.Header, Body: errorMessage([]byte(ne.Body)), RawBody: ne.Body}
		}
		return res, fmt.Errorf("%s 调用 %s 出错：%w", c.src.Name, r.Model, decodeErr)
	}
	return res, nil
}

// applyDecoded 把解码器的中立结果收成对外字段：正文、思考、工具调用、用量、结束原因、回放。
func (res *Result) applyDecoded(out openaichat.Output, model string) {
	res.Stop = string(out.Result.StopReason)
	if out.Result.StopReason == chat.StopReasonError {
		res.Stop = "" // 旧写法出错时 Stop 是空
	}
	u := out.Result.Usage
	res.Usage = Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Reasoning: u.Reasoning}
	res.Text, res.Reasoning = "", ""
	for _, block := range out.Result.Blocks {
		switch b := block.(type) {
		case chat.Text:
			res.Text += string(b)
		case chat.Thinking:
			res.Reasoning += b.Text
		}
	}
	res.ToolCalls = res.ToolCalls[:0]
	for _, tc := range out.ToolCalls {
		res.ToolCalls = append(res.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args})
	}
	res.Replay = out.Result.Replay
	if res.Replay.Codec != "" {
		res.Replay.Model = model
	}
	res.reasoningField, res.reasoningDetails = openaichat.ReplayParts(res.Replay)
}

// errorBody 是出错回复里服务商说的原因
func errorBody(resp *http.Response) string {
	if resp.Body == nil {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return string(b)
}

// errorMessage：JSON 的 error.message，没有就取原文；截到 300 个字
func errorMessage(b []byte) string {
	s := strings.TrimSpace(string(b))
	var j struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &j) == nil && j.Error.Message != "" {
		s = j.Error.Message
	}
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

// StopError：服务商的结束原因表示出错（content_filter、不认识的），或者流结束了却没给 finish_reason（Reason 是空）。
// 出错以前收到的字、思考、用量照样在 Result 里
type StopError struct {
	Source, Model, Reason string
}

func (e *StopError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("%s 调用 %s：回复流结束了，但没有 finish_reason（可能中途断了）", e.Source, e.Model)
	}
	return fmt.Sprintf("%s 调用 %s：服务商的 finish_reason 是 %s", e.Source, e.Model, e.Reason)
}

// PiMessage 是 pi 记进 errorMessage 的那句（openai-completions.js mapStopReason、489 行）：Bun 版把它存进 model_runs.error
func (e *StopError) PiMessage() string {
	if e.Reason == "" {
		return "Stream ended without finish_reason"
	}
	return "Provider finish_reason: " + e.Reason
}
