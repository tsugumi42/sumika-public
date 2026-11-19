// Package modelcall 是生产模型请求的唯一预算与容量边界；llm.Client 仅作 transport。
package modelcall

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/llm/openaichat"
	"sumika-native/internal/modelref"
	"sumika-native/internal/store"
	"sumika-native/internal/turnbudget"
)

type Transport interface {
	StreamChat(context.Context, chat.Request, func(llm.Event)) (llm.Result, error)
}

type Client struct {
	Transport           Transport
	Budget              kernel.Budget
	Store               *store.Store
	Slots               chan struct{}
	RequestOutputTokens int64
	Now                 func() time.Time
	Failover            *llm.Failover
	// Refs 把请求里的模型换成规范引用 `服务商ID/模型ID`（trace、用量记的就是它）；从传输层（llm.Router）取，
	// 传输层不带解析器时是零值，模型串原样记
	Refs modelref.Resolver

	// 排队的请求：运行页的"模型请求槽位"行读它（Load）。只记开始等的时刻，拿到槽或放弃就删，没有协程
	waitMu  sync.Mutex
	waitSeq uint64
	// inflight 是这个进程里正在走 Stream 的请求数（含正在等占位释放的）。账本里有占位、而本进程没有别的在途请求，
	// 说明占位是以前崩溃留下的、永远没人结算：按真耗尽处理，不能一直等
	inflight atomic.Int32
	waiting  map[uint64]time.Time
}

func New(transport Transport, budget kernel.Budget, st *store.Store, capacity int, output int64) *Client {
	refs := llm.RefsOf(transport)
	return &Client{Transport: transport, Budget: budget, Store: st, Slots: make(chan struct{}, capacity), RequestOutputTokens: output, Now: time.Now, Failover: &llm.Failover{Refs: refs}, Refs: refs}
}

// enterWait 记下一个请求开始等槽，返回"不等了"的函数（拿到槽或放弃时调；可以重复调）
func (c *Client) enterWait() (leave func()) {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	if c.waiting == nil {
		c.waiting = map[uint64]time.Time{}
	}
	c.waitSeq++
	id := c.waitSeq
	c.waiting[id] = time.Now()
	return func() {
		c.waitMu.Lock()
		delete(c.waiting, id)
		c.waitMu.Unlock()
	}
}

// Load 是槽位现在的情况：在跑几个、一共几个槽、几个在排队，最久的已经等了多久（没人排队时是 0）。
// 只读，给运行页的后台队列表用
func (c *Client) Load() (running, capacity, waiting int, oldest time.Duration) {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	now := time.Now()
	for _, t := range c.waiting {
		if d := now.Sub(t); d > oldest {
			oldest = d
		}
	}
	return len(c.Slots), cap(c.Slots), len(c.waiting), oldest
}

// InputEstimateChat 估算发送内容：messages、tools 用编码器实际会写出的两段原文，
// 和旧路径对同一份消息算出的字节一致；图仍按 3000 字节一个占位。字节数作保守上界，不宣传为供应商 token 计量。
func InputEstimateChat(req chat.Request) (int64, error) {
	_, messages, tools, err := openaichat.EncodeRequest(req)
	if err != nil {
		return 0, err
	}
	return estimateJSON(messages, tools), nil
}

func estimateJSON(messageJSON, toolsJSON string) int64 {
	n := len(messageJSON)
	if !strings.Contains(messageJSON, `"image_url"`) {
		return int64(n + len(toolsJSON) + 16)
	}
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal([]byte(messageJSON), &messages) == nil {
		for _, message := range messages {
			var parts []struct {
				Type     string `json:"type"`
				ImageURL struct {
					URL json.RawMessage `json:"url"`
				} `json:"image_url"`
			}
			if json.Unmarshal(message.Content, &parts) != nil {
				continue
			}
			for _, part := range parts {
				if part.Type == "image_url" && len(part.ImageURL.URL) > 0 && part.ImageURL.URL[0] == '"' {
					n += 3000 - len(part.ImageURL.URL)
				}
			}
		}
	}
	return int64(n + len(toolsJSON) + 16)
}

// reserveOutput 定这次请求发多少 max_completion_tokens，并在账本里占位（预留）。
//
// 单次输出的取值：调用端按模型自己的输出上限和窗口余量算好放在 req.MaxOutputTokens（模型没报的由调用端用兜底）；
// 这里不再套一个固定值，只有配置了可选封顶（根里的 RequestOutputTokens，或 New 传的默认）才再往下压；
// 两者都没有时用 llm.FallbackMaxOutput。然后不能超过根剩余的输出额和令牌额。
//
// 预留（占位）不等于发送值：发送值可以是整个剩余额，如果都占住，同根并发的下一个请求算出 0，会被当成"额度用完"停掉整个根
// 和所有子代理。占位只按剩余额在并发槽数上均分（至少 1），真正的硬门是结算后的检查（超了停根）。
// 并且"算出 0"要分清：实际已花的加上这次输入已经没有余额才是真耗尽、才停根；只是被别的在途请求占住，就等它们结算后重算，
// 不停根。
func (c *Client) reserveOutput(ctx context.Context, in kernel.Invocation, requested, input int64) (int64, error) {
	for {
		snap, err := c.Budget.Snapshot(ctx, in.RootID)
		if err != nil {
			return 0, err
		}
		l := snap.Limits
		output := requested
		if output <= 0 {
			output = llm.FallbackMaxOutput
		}
		requestCap := c.RequestOutputTokens
		if l.RequestOutputTokens > 0 {
			requestCap = l.RequestOutputTokens
		}
		if requestCap > 0 {
			output = min(output, requestCap)
		}
		// 实际用掉的之外还剩多少（不算别的请求的占位）和算上占位还剩多少
		spentRoom := min(l.MaxOutputTokens-snap.OutputTokens, l.MaxTokens-snap.Tokens-input)
		freeRoom := min(l.MaxOutputTokens-snap.OutputTokens-snap.ReservedOutputTokens, l.MaxTokens-snap.Tokens-snap.ReservedTokens-input)
		if spentRoom <= 0 || snap.Requests >= l.MaxRequests {
			return 0, errors.Join(turnbudget.ErrExhausted, turnbudget.ExhaustRoot(ctx, c.Budget, in.RootID))
		}
		if freeRoom <= 0 {
			if snap.ReservedOutputTokens == 0 && snap.ReservedTokens == 0 || c.inflight.Load() <= 1 {
				return 0, errors.Join(turnbudget.ErrExhausted, turnbudget.ExhaustRoot(ctx, c.Budget, in.RootID))
			}
			if err := c.waitSettle(ctx); err != nil {
				return 0, err
			}
			continue
		}
		output = min(output, freeRoom)
		reserve := output
		if slots := int64(cap(c.Slots)); slots > 1 && !kernel.Unlimited(freeRoom) {
			reserve = max(1, min(output, (freeRoom+slots-1)/slots))
		}
		// 当前供应商接口没有可证明的有限 USD 价格上界，因此不伪造金额预留。
		err = c.Budget.Reserve(ctx, kernel.Reservation{ID: in.AttemptID, Invocation: in, Requests: 1, Tokens: input + reserve, OutputTokens: reserve})
		if err == nil {
			return output, nil
		}
		if !errors.Is(err, turnbudget.ErrExhausted) {
			return 0, err
		}
		// 读余额和占位之间别的请求先占了：有在途占位就等一等重算，否则是真的用完
		again, snapErr := c.Budget.Snapshot(ctx, in.RootID)
		if snapErr != nil || again.ReservedOutputTokens == 0 && again.ReservedTokens == 0 || c.inflight.Load() <= 1 {
			return 0, errors.Join(err, turnbudget.ExhaustRoot(ctx, c.Budget, in.RootID))
		}
		if err := c.waitSettle(ctx); err != nil {
			return 0, err
		}
	}
}

// waitSettle 等同根别的请求结算释放占位：隔一小会儿重读余额，直到取消或根时限到
func (c *Client) waitSettle(ctx context.Context) error {
	t := time.NewTimer(50 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// StreamChat 是中立请求的预算和记账入口：和发送共用槽位、预留、结算、model_runs。
// 失败切换的 trace 由 WithFailover 带进来。
func (c *Client) StreamChat(ctx context.Context, in kernel.Invocation, req chat.Request, onEvent func(llm.Event)) (result llm.Result, err error) {
	req.Model = c.Refs.Canonical(req.Model)
	return c.stream(ctx, in, transportCall{
		model:           req.Model,
		failover:        llm.FailoverFrom(ctx),
		requestedOutput: req.MaxOutputTokens,
		estimate:        func() (int64, error) { return InputEstimateChat(req) },
		setOutput: func(n int64) {
			req.MaxOutputTokens = n
		},
		send: func(ctx context.Context, onEvent func(llm.Event)) (llm.Result, error) {
			if req.Headers == nil {
				req.Headers = map[string]string{}
			}
			if _, ok := req.Headers["x-openai-subagent"]; !ok {
				if purpose := PurposeOf(in.Kind); purpose != "" {
					req.Headers["x-openai-subagent"] = purpose
				}
			}
			return c.Transport.StreamChat(ctx, req, onEvent)
		},
	}, onEvent)
}

// transportCall 是两条入口共用的预算和记账参数：模型、切换 trace、输入估算、输出预留和实际发送。
type transportCall struct {
	model           string
	failover        *llm.FailoverInfo
	requestedOutput int64
	estimate        func() (int64, error)
	setOutput       func(int64)
	send            func(context.Context, func(llm.Event)) (llm.Result, error)
}

func (c *Client) stream(ctx context.Context, in kernel.Invocation, call transportCall, onEvent func(llm.Event)) (result llm.Result, err error) {
	local := true
	defer func() {
		if local && err != nil {
			err = llm.LocalError(err)
		}
	}()
	if !turnbudget.ValidInvocation(in) || in.AttemptID == "" || in.StepID == "" {
		return llm.Result{}, errors.New("model call requires trusted root/run/step/attempt/kind")
	}
	c.inflight.Add(1)
	defer c.inflight.Add(-1)
	snap, err := c.Budget.Snapshot(ctx, in.RootID)
	if err != nil {
		return llm.Result{}, err
	}
	rootDeadline := snap.Limits.Deadline
	defer func() {
		if errors.Is(err, context.DeadlineExceeded) && !time.Now().Before(rootDeadline) {
			err = llm.LocalError(errors.Join(turnbudget.ErrExhausted, err, turnbudget.ExhaustRoot(ctx, c.Budget, in.RootID)))
		}
	}()
	ctx, untrack := turnbudget.TrackRoot(ctx, c.Budget, in.RootID)
	defer untrack()
	ctx, cancel := context.WithDeadline(ctx, snap.Limits.Deadline)
	defer cancel()
	leave := c.enterWait()
	select {
	case c.Slots <- struct{}{}:
		leave()
	case <-ctx.Done():
		leave()
		return llm.Result{}, ctx.Err()
	}
	defer func() { <-c.Slots }()
	// 排队期间别的请求可能结算并释放预留，拿槽后再读余额，不能用排队前的零余额误停根。
	snap, err = c.Budget.Snapshot(ctx, in.RootID)
	if err != nil {
		return llm.Result{}, err
	}
	input, err := call.estimate()
	if err != nil {
		return llm.Result{}, err
	}
	output, err := c.reserveOutput(ctx, in, call.requestedOutput, input)
	if err != nil {
		return llm.Result{}, err
	}
	call.setOutput(output)
	if lifetime, ok := ctx.Value(lifetimeKey{}).(kernel.Lifetime); ok {
		activityCtx, activity, beginErr := lifetime.Begin(ctx, in)
		if beginErr != nil {
			release := c.Budget.Settle(context.WithoutCancel(ctx), in.AttemptID, kernel.Charge{})
			return llm.Result{}, errors.Join(beginErr, release)
		}
		ctx = activityCtx
		defer func() { activity.End(err) }()
	}
	if err = c.Budget.Dispatch(ctx, in.AttemptID); err != nil {
		release := c.Budget.Settle(context.WithoutCancel(ctx), in.AttemptID, kernel.Charge{})
		return llm.Result{}, errors.Join(err, release)
	}
	started := c.Now()
	local = false
	result, callErr := call.send(ctx, onEvent)
	charge := kernel.Charge{Input: result.Usage.Input, Output: result.Usage.Output, CacheRead: result.Usage.CacheRead, CacheWrite: result.Usage.CacheWrite, Reasoning: result.Usage.Reasoning, UsageSeen: result.UsageSeen, Dispatched: true}
	settleErr := c.Budget.Settle(context.WithoutCancel(ctx), in.AttemptID, charge)
	after, snapshotErr := c.Budget.Snapshot(context.WithoutCancel(ctx), in.RootID)
	if snapshotErr == nil && (after.Tokens+after.ReservedTokens > after.Limits.MaxTokens || after.OutputTokens+after.ReservedOutputTokens > after.Limits.MaxOutputTokens) {
		settleErr = errors.Join(settleErr, turnbudget.ErrExhausted, turnbudget.ExhaustRoot(ctx, c.Budget, in.RootID))
	}
	settleErr = errors.Join(settleErr, snapshotErr)
	cw, reasoning := result.Usage.CacheWrite, result.Usage.Reasoning
	var reasoningTokens *int64
	if result.UsageSeen {
		reasoningTokens = &reasoning
	}
	var message *string
	var class string
	if failure := errors.Join(callErr, settleErr); failure != nil {
		s := failure.Error()
		message = &s
		// 类别按真正出错的那一个定：请求本身的错优先，请求成功而结算出错（预算用完等）才看结算的错
		if callErr != nil {
			class = ErrorClass(callErr)
		} else {
			class = ErrorClass(settleErr)
		}
	}
	run := store.Run{ID: in.AttemptID, CharacterID: in.Scope.CharacterID, Conversation: in.Scope.Conversation, Stage: in.Kind, Model: call.model,
		StartedAt: started.UnixMilli(), TotalMs: c.Now().Sub(started).Milliseconds(), Input: charge.Input, Output: charge.Output, CacheRead: charge.CacheRead, CacheWrite: &cw, Reasoning: reasoningTokens,
		Identity: in, UsageSeen: &result.UsageSeen, Incomplete: !result.UsageSeen, Error: message, ErrorClass: class, Now: c.Now().UnixMilli(), Prompt: result.Prompt}
	if call.failover != nil {
		metadata, marshalErr := json.Marshal(struct {
			Failover *llm.FailoverInfo `json:"failover"`
		}{call.failover})
		if marshalErr != nil {
			settleErr = errors.Join(settleErr, marshalErr)
		} else {
			run.Context = string(metadata)
		}
	}
	if result.Route.Provider != "" {
		run.RouteProvider = &result.Route.Provider
	}
	if result.Route.Model != "" {
		run.RouteModel = &result.Route.Model
	}
	recordErr := c.Store.AddRun(run)
	if callErr != nil {
		// 模型那边确实出错了：记账失败不能把一次可重试的网络错误变成“本地错误”
		return result, errors.Join(callErr, settleErr, recordErr)
	}
	return result, errors.Join(llm.LocalError(settleErr), llm.LocalError(recordErr))
}

// PurposeOf 是这种调用发给 Magpie 的用途头（x-openai-subagent）：前台回复不带；其余一律 sumika_ 开头，
// 避开 Magpie 自己有专门处理的那几个名字（memory、compact、guardian、vision 等会改道或另算，cache-audit-20261113 第 2.7 节）。
// 2026-11-13 在本机 Magpie 实测过：带了这个头，/v1/magpie/usage/requests 的 purposes 里出现 kind:<值>
func PurposeOf(kind string) string {
	switch kind {
	case "", kernel.KindForeground:
		return ""
	case kernel.KindLinkTranslate:
		return "sumika_translate"
	}
	var b strings.Builder
	b.WriteString("sumika_")
	for _, r := range kind {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() > 40 { // Magpie 截到 40 个字
		return b.String()[:40]
	}
	return b.String()
}

// ErrorClass 给一次模型请求的失败定原因类别（规格-运行页 第七节）：预算用完先认（llm 不认识 turnbudget），
// 其余交给 llm.ClassifyError。nil 返回空串。回合、压缩写 model_runs 也用它，不各算一份
func ErrorClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, turnbudget.ErrExhausted) {
		return llm.ClassBudgetExhausted
	}
	return llm.ClassifyError(err)
}

type lifetimeKey struct{}

func WithLifetime(ctx context.Context, l kernel.Lifetime) context.Context {
	return context.WithValue(ctx, lifetimeKey{}, l)
}
