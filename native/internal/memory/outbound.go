package memory

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"

	"sumika-native/internal/embed"
	"sumika-native/internal/pyspace"
)

// OutboundConfirmPrompt 和离线量测共用同一份问法（13.2.8），避免量的和用的不一样。
//
//go:embed guard-confirm.txt
var OutboundConfirmPrompt string

const (
	GuardThreshold      = 0.70
	GuardEmbedTimeout   = 2 * time.Second
	GuardConfirmTimeout = 8 * time.Second
)

// guardCalibratedModels 是守卫门槛 GuardThreshold（0.70）标定过的嵌入模型：ADR-202 的门槛是 cmd/guard-eval 在 gte-multilingual-base 上量出来的，
// 别的模型的相似度分布不一样，同一个 0.70 没有意义（规格-Go本地模型 2.7"门槛绑模型"）。加一个模型要先在它上面重量门槛，再加进这里
var guardCalibratedModels = map[string]bool{"gte-multilingual-base": true}

// GuardCalibrated 说这个嵌入模型的相似度是不是和守卫门槛对过
func GuardCalibrated(model string) bool { return guardCalibratedModels[model] }

// guardTimeout 取选项里的时限，零或负用默认。
func guardTimeout(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

var errOutboundEmbedding = errors.New("守卫嵌入失败")

// GuardRoute 供渠道观察退路切换；提醒由渠道负责，不在记忆包里发消息。
func (m *Memory) GuardRoute() string {
	switch m.guardRoute.Load() {
	case 1:
		return "embed-down"
	case 2:
		return "model-down"
	default:
		return "normal"
	}
}

// GuardLevel 供下一步发送装配检查启用前提，和最近一次判断走的 GuardRoute 分开。
// 缓存不可读、确认模型缺失时不允许启用。嵌入就绪还不够：当前模型的句向量预热也要做完（sentences.go），才报 full——
// 换模型当天旧模型的句向量全部作废，预热期间守卫在只比原文的退路上，发送却已放行，是自己造成的降级，不是 ADR-186 第 4 条批准的"嵌入真的挂了"。
// 预热没做完报 literal-pending，发送钳在试运行。再一条：嵌入模型要是门槛标定过的（GuardCalibrated，现在只有 gte-multilingual-base），
// 否则也是 literal-pending——回滚到 Qwen 之类没量过 0.70 的模型时，嵌入就绪也不能报"守卫完整"（规格-Go本地模型 2.7，只收紧）
func (m *Memory) GuardLevel() string {
	if m.opt.Gen == nil || m.opt.Model == nil || strings.TrimSpace(m.opt.Model()) == "" || m.index == nil || m.index.db == nil || m.index.sentenceGate == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int
	if err := m.index.db.QueryRowContext(ctx, `SELECT count(*) FROM memory_sentences WHERE 0`).Scan(&n); err != nil {
		return ""
	}
	if m.embed == nil || m.embed.Model() == "" || !GuardCalibrated(m.embed.Model()) {
		return "literal-pending"
	}
	if local, ok := m.embed.(interface{ Status() (embed.Status, string) }); ok {
		if status, _ := local.Status(); status != embed.StatusReady {
			return "literal-pending"
		}
	}
	warm, err := m.sentencesWarm(ctx)
	if err != nil {
		return ""
	}
	if !warm {
		return "literal-pending"
	}
	return "full"
}

// CheckOutbound 判调用方传来的“本回合已放行的段 + 本段”，不跨会话保留待发文字。
// 字面先判全部禁看条目；句向量取最大余弦，像的合成一次模型确认（不是逐条询问）。
func (m *Memory) CheckOutbound(ctx context.Context, text string, a Audience) (blocked []string, err error) {
	return m.checkOutbound(ctx, text, a, true)
}

// CheckRepeat 给下一步复读装配用：字面和相似度命中就拦，免去模型确认（ADR-187）。
func (m *Memory) CheckRepeat(ctx context.Context, text string, a Audience) ([]string, error) {
	return m.checkOutbound(ctx, text, a, false)
}

func (m *Memory) checkOutbound(ctx context.Context, text string, a Audience, confirm bool) (blocked []string, err error) {
	gen, model, _ := m.background()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	route, reason := uint32(0), "clear"
	// judged 为真才算"这次真的走到了嵌入这一步"。没有禁看条目、没有可判的句子、字面先命中、门槛非法、上下文已结束这些
	// 提前返回的，没验证过嵌入和模型，不覆盖上一次的路线：不然嵌入挂着时一句空回复就会把路线写回"正常"，提醒来回跳。
	judged := false
	defer func() {
		if judged {
			m.guardRoute.Store(route)
		}
		m.logf("记忆守卫：route=%s reason=%s", m.GuardRoute(), reason)
	}()
	if err := ctx.Err(); err != nil {
		reason = "context_done"
		return nil, err
	}
	threshold := m.opt.GuardThreshold
	if threshold == 0 {
		threshold = GuardThreshold
	}
	if math.IsNaN(threshold) || threshold <= 0 || threshold > 1 {
		reason = "threshold_invalid"
		return nil, errors.New("守卫门槛必须在 (0, 1] 内")
	}
	allowed := DisclosureScopes(a)
	snap, _ := m.snapshot()
	items, previousOf := withPrevious(snap)
	denied := make([]Item, 0)
	clean := outboundNormalize(text)
	for _, it := range items {
		if CanSee(it.Scopes, allowed) || !guardable(it) {
			continue
		}
		denied = append(denied, it)
		if outboundLiteral(clean, it.Text) {
			blocked = append(blocked, it.Ref)
		}
	}
	if len(blocked) > 0 {
		reason = "literal"
		return blocked, nil
	}
	queries := OutboundSentences(text)
	if len(denied) == 0 || len(queries) == 0 {
		return nil, nil
	}
	judged = true
	if m.embed == nil {
		route, reason = 1, "embed_missing"
		return nil, nil
	}
	ec, stop := context.WithTimeout(ctx, guardTimeout(m.opt.GuardEmbedTimeout, GuardEmbedTimeout))
	// 先算查询：它的维度是现行模型的维度，库里缓存的句向量对不上的当作坏缓存，重算覆盖。
	vs, eerr := outboundEmbed(ec, m.embed, embed.TypeQuery, queries)
	var vectors map[string][][]float32
	if eerr == nil {
		if m.opt.Shared == nil {
			vectors, eerr = m.index.outboundVectors(ec, denied, m.embed, len(vs[0]))
		} else {
			_, by := m.opt.Shared.snapshot()
			var own, shared []Item
			for _, it := range denied {
				if by[it.Ref] != nil || by[previousOf[it.Ref]] != nil {
					shared = append(shared, it)
				} else {
					own = append(own, it)
				}
			}
			vectors, eerr = m.index.outboundVectors(ec, own, m.embed, len(vs[0]))
			if eerr == nil {
				var pooled map[string][][]float32
				pooled, eerr = m.opt.Shared.index.outboundVectors(ec, shared, m.embed, len(vs[0]))
				for ref, v := range pooled {
					vectors[ref] = v
				}
			}
		}
	}
	stop()
	if err := ctx.Err(); err != nil {
		reason = "context_done"
		return nil, err
	}
	if eerr != nil {
		if !errors.Is(eerr, errOutboundEmbedding) && !errors.Is(eerr, context.DeadlineExceeded) {
			reason = "index_error"
			return nil, eerr
		}
		route, reason = 1, "embed_error"
		return nil, nil
	}
	var suspects []Item
	for _, it := range denied {
		// 维度对不上、NaN 与任何数比都是假（会被当成"不像"悄悄放行）：上游已校验过向量，这里是最后一道，走和嵌入出错同一条退路。
		score, serr := GuardScore(vs, vectors[it.Ref])
		switch {
		case errors.Is(serr, ErrGuardDimension):
			route, reason = 1, "embed_dimension"
			return nil, nil
		case serr != nil:
			route, reason = 1, "embed_invalid"
			return nil, nil
		}
		// 按 float32 比的原因写在 GuardReaches 上。
		if GuardReaches(score, threshold) {
			suspects = append(suspects, it)
			blocked = append(blocked, it.Ref)
		}
	}
	if len(suspects) == 0 {
		reason = "below_threshold"
		return nil, nil
	}
	if !confirm {
		reason = "similarity"
		return blocked, nil
	}
	if gen == nil || model == "" {
		route, reason = 2, "model_missing"
		return blocked, nil
	}
	// yes/no 判的是这组里是否有一件；编号返回可疑集合，不把未过门槛的条目报成命中。
	type secret struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	docs := make([]secret, len(suspects))
	for i, it := range suspects {
		docs[i] = secret{it.Ref, it.Text}
	}
	body, _ := json.Marshal(map[string]any{"context": "", "outbound": text, "forbidden_memories": docs})
	mc, stop := context.WithTimeout(ctx, guardTimeout(m.opt.GuardConfirmTimeout, GuardConfirmTimeout))
	reply, merr := gen.Call(mc, model, OutboundConfirmPrompt+"\n"+string(body), "guard:"+m.opt.Character)
	if merr == nil {
		merr = mc.Err()
	}
	stop()
	// 父上下文可能在读取 mc.Err 后结束；整体取消仍返回 error，不能采信这时的 no。
	if err := ctx.Err(); err != nil {
		reason = "context_done"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "model_timeout"
		}
		m.logConfirmationFailure(reason, reply.Text)
		return nil, err
	}
	if merr != nil {
		route, reason = 2, "model_call_error"
		if errors.Is(merr, context.DeadlineExceeded) {
			reason = "model_timeout"
		}
		m.logConfirmationFailure(reason, reply.Text)
		return blocked, nil
	}
	leaked, bad := ParseOutboundConfirmation(reply.Text)
	if bad != "" {
		route, reason = 2, bad
		m.logConfirmationFailure(reason, reply.Text)
		return blocked, nil
	}
	reason = "model_no"
	if leaked {
		reason = "model_yes"
		return blocked, nil
	}
	return nil, nil
}

func outboundNormalize(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return -1
		}
		return r
	}, text)
}

func outboundLiteral(clean, memory string) bool {
	chars := []rune(outboundNormalize(memory))
	if len(chars) < 6 {
		return false
	}
	n := min(10, len(chars))
	for i := 0; i+n <= len(chars); i++ {
		if strings.Contains(clean, string(chars[i:i+n])) {
			return true
		}
	}
	return false
}

// OutboundSentences 是守卫切句的唯一实现：句末与 CRLF 切开，小数和英文缩写保留。
// 守卫（checkOutbound）、句向量预热和 cmd/guard-eval 都调它，量门槛的和用门槛的因此永远是同一个口径。
// 每句首尾按 Python 的 str.strip() 去空白，去完是空的丢掉（internal/pyspace）：嵌入服务也用这个集合判空，
// 一句只含 U+001C～U+001F 的话（Go 的 TrimSpace 不认）送进去，整批回 400，同批别的句子也算不出来。
func OutboundSentences(text string) []string {
	runes := []rune(text)
	var out []string
	start := 0
	for i, r := range runes {
		end := strings.ContainsRune("。！？!?；;\r\n", r)
		if r == '.' {
			end = i+1 == len(runes) || pyspace.IsSpace(runes[i+1])
		}
		if end {
			if s := pyspace.Trim(string(runes[start : i+1])); s != "" && strings.Trim(s, "。！？!?；;\r\n.") != "" {
				out = append(out, s)
			}
			start = i + 1
		}
	}
	if s := pyspace.Trim(string(runes[start:])); s != "" {
		out = append(out, s)
	}
	return out
}

func outboundEmbed(ctx context.Context, e embed.Embedder, kind embed.Type, texts []string) ([][]float32, error) {
	var vs [][]float32
	var err error
	if typed, ok := e.(embed.Typed); ok {
		vs, err = typed.EmbedTyped(ctx, kind, texts)
	} else {
		vs, err = e.Embed(ctx, texts)
	}
	if err != nil {
		return nil, errors.Join(errOutboundEmbedding, err)
	}
	if ctx.Err() != nil {
		return nil, errors.Join(errOutboundEmbedding, ctx.Err())
	}
	if len(vs) != len(texts) {
		return nil, errOutboundEmbedding
	}
	dim := 0
	for i, v := range vs {
		u, ok := outboundUnit(v)
		if !ok || (dim != 0 && len(v) != dim) {
			return nil, errOutboundEmbedding
		}
		dim = len(v)
		vs[i] = u
	}
	return vs, nil
}

// outboundUnit 归一化一个向量；空、全零、含 NaN / Inf，或模长小到归一化后溢出的，一律不收。
func outboundUnit(v []float32) ([]float32, bool) {
	if len(v) == 0 {
		return nil, false
	}
	norm := float64(0)
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
		return nil, false
	}
	u := embed.Unit(v)
	for _, x := range u {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, false
		}
	}
	return u, true
}
