package linktranslate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Caller 调模型：system 和 user 两条消息，返回原始文字。宿主用后台模型实现，无工具。
type Caller func(ctx context.Context, system, user string) (string, error)

// 结果状态（运行页记录用）。
const (
	StatusOK       = "ok"       // 翻译成功
	StatusCached   = "cached"   // 缓存里有译文
	StatusUnneeded = "unneeded" // 不用翻：缓存里记过，或模型明确回了哨兵 / 同原文
	StatusSkipped  = "skipped"  // 本地判断不翻
	StatusTimeout  = "timeout"
	StatusFailed   = "failed"
)

// ErrNoModel 是调用方没有可用的翻译模型（没配后台模型）：算“没法翻”，不是失败，运行页不记。
var ErrNoModel = errors.New("没有配置后台模型")

// DefaultTimeout 是单次翻译调用的默认时限（和 config.DefaultTranslateTimeout 一致）。
const DefaultTimeout = 15 * time.Second

// SegmentFailure 是长文分段翻译里某一段失败的原因类别（ADR-391）：整篇退回原文。
const SegmentFailure = "segment_failed"

// Outcome 是一次翻译的结果。Text 非空才有译文，其余状态一律只发原文。
type Outcome struct {
	Text   string
	Status string
	Reason string // 不翻或失败的原因类别，不含用户内容
	Took   time.Duration
}

// Translator 把一段正文翻成目标语言。零值不可用。
type Translator struct {
	Call    Caller
	Cache   *Cache
	Model   string        // 翻译模型 ID，进缓存键；空串表示宿主没指定
	Timeout time.Duration // 单次调用的时限；0 用 DefaultTimeout
	Retries int           // 超时后再试几次，每次重新计时；外层 ctx 到期或取消不重试
	Now     func() time.Time
}

// Translate 翻译 text。cleanURL 只用来做缓存键。单次调用超时按 Retries 再试；ctx 先到期或被取消算失败，不重试。
// 超过 MaxRunes 的原文按段落切成最多 MaxSegments 段分别翻（并发），译文按原顺序拼回；任一段失败整篇退回原文。
func (t *Translator) Translate(ctx context.Context, cleanURL, text, target string) Outcome {
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	start := now()
	done := func(o Outcome) Outcome { o.Took = now().Sub(start); return o }
	if t.Call == nil {
		return done(Outcome{Status: StatusSkipped, Reason: "no_model"})
	}
	if ok, why := Needs(text, target); !ok {
		return done(Outcome{Status: StatusSkipped, Reason: why})
	}
	key := Key(cleanURL, target, t.Model, text)
	if t.Cache != nil {
		if got, hit := t.Cache.Get(key, now()); hit {
			if got == "" {
				return done(Outcome{Status: StatusUnneeded, Reason: "cached"})
			}
			return done(Outcome{Text: got, Status: StatusCached})
		}
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if utf8.RuneCountInString(text) > MaxRunes {
		return t.translateSegments(ctx, key, text, target, timeout, now, done)
	}
	raw, timedOut, err := t.call(ctx, timeout, target, text)
	if err != nil {
		if errors.Is(err, ErrNoModel) {
			return done(Outcome{Status: StatusSkipped, Reason: "no_model"})
		}
		reason := "error"
		status := StatusFailed
		if timedOut {
			status, reason = StatusTimeout, "timeout"
		} else if errors.Is(err, context.Canceled) {
			reason = "canceled"
		}
		return done(Outcome{Status: status, Reason: reason})
	}
	out, why := CleanWhy(raw, text)
	switch why {
	case WhyNone:
	case WhyUnneeded:
		if t.Cache != nil {
			t.Cache.Put(key, "", now()) // 模型明确判过不用翻，24 小时内不再调
		}
		return done(Outcome{Status: StatusUnneeded, Reason: why})
	default: // 回空、超长是模型出了问题：不缓存，下次重试
		return done(Outcome{Status: StatusFailed, Reason: why})
	}
	if t.Cache != nil {
		t.Cache.Put(key, out, now())
	}
	return done(Outcome{Text: out, Status: StatusOK})
}

// translateSegments 把超过一次上限的原文分段翻译：按行（段落）分组，每段不超过 MaxRunes；段数超过 MaxSegments
// 整篇不翻（too_long），不截断。各段并发调用，译文按原顺序拼回；某一段失败或超时整篇用原文，
// 原因记 segment_failed（超时仍算 timeout），不把半篇译文发出去（ADR-391）。
func (t *Translator) translateSegments(ctx context.Context, key, text, target string, timeout time.Duration, now func() time.Time, done func(Outcome) Outcome) Outcome {
	chunks, ok := splitSource(text, MaxRunes, MaxSegments)
	if !ok {
		return done(Outcome{Status: StatusSkipped, Reason: SkipTooLong})
	}
	parts := make([]string, len(chunks))
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	failStatus, failReason := "", ""
	fail := func(status, reason string) {
		mu.Lock()
		if failStatus == "" {
			failStatus, failReason = status, reason
		}
		mu.Unlock()
		cancel()
	}
	for i, ch := range chunks {
		if need, _ := Needs(ch.text, target); !need {
			parts[i] = ch.text // 这一段本来就全是链接、数字或已经是目标语言：原样留着
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, timedOut, err := t.call(sctx, timeout, target, ch.text)
			if err != nil {
				if errors.Is(err, ErrNoModel) {
					fail(StatusSkipped, "no_model") // 和单段一致：算没法翻，运行页不记
					return
				}
				status, reason := StatusFailed, SegmentFailure
				if timedOut {
					status = StatusTimeout
				} else if errors.Is(err, context.Canceled) {
					reason = "canceled" // 和单段一致：外层取消不是某一段翻译坏了
				}
				fail(status, reason)
				return
			}
			out, why := CleanWhy(raw, ch.text)
			switch why {
			case WhyNone:
				parts[i] = out
			case WhyUnneeded:
				parts[i] = ch.text
			default:
				fail(StatusFailed, SegmentFailure)
			}
		}()
	}
	wg.Wait()
	if failStatus != "" {
		return done(Outcome{Status: failStatus, Reason: failReason})
	}
	out := joinSource(parts, chunks)
	if strings.TrimSpace(out) == strings.TrimSpace(text) {
		// 每段都判过不用翻（全是链接、数字或已是目标语言）：和单段一样记“不用翻”，不缓存成译文
		if t.Cache != nil {
			t.Cache.Put(key, "", now())
		}
		return done(Outcome{Status: StatusUnneeded, Reason: WhyUnneeded})
	}
	if t.Cache != nil {
		t.Cache.Put(key, out, now())
	}
	return done(Outcome{Text: out, Status: StatusOK})
}

// sourceChunk 是一段送翻原文和它后面原来跟着的空白（拼回译文时原样接回去）。
type sourceChunk struct {
	text string
	sep  string
}

// isSectionMarker 说这一行是不是 Article 的分段标记（%% 单独一行，两边可以有空白）。
func isSectionMarker(line string) bool {
	return strings.TrimSpace(line) == SectionMarker
}

// fixSectionMarkers 把落在段首、段尾的 %% 行挪到相邻段内部（ADR-391）：模型按段翻时，
// 边界上的分隔行容易被丢掉或补一条；让它两边都有正文，段数、位置在拼回时不变。
// 做法是移动整行，不切开行：段尾是 %% 时，把前一行正文和 %% 一起挪到下一段开头；
// 段首是 %% 时，把 %% 和下一行正文一起挪到上一段末尾。可能让相邻段略超 maxRunes，正文不丢。
func fixSectionMarkers(raws []string) []string {
	chunks := make([][]string, len(raws))
	for i, raw := range raws {
		chunks[i] = strings.SplitAfter(raw, "\n")
	}
	for i := 0; i < len(chunks); i++ {
		if len(chunks[i]) == 0 {
			continue
		}
		last := len(chunks[i]) - 1
		for last >= 0 && strings.TrimSpace(chunks[i][last]) == "" {
			last--
		}
		// 段尾是 %%：前一行正文 + %% 挪到下一段开头，%% 两边就都有正文。
		if last > 0 && isSectionMarker(chunks[i][last]) && i+1 < len(chunks) {
			move := append([]string(nil), chunks[i][last-1:last+1]...)
			chunks[i] = chunks[i][:last-1]
			chunks[i+1] = append(move, chunks[i+1]...)
		}
		// 段首是 %%（上一段收尾时没挪到，或原文就这么切）：%% + 下一行正文挪到上一段末尾。
		if i > 0 && len(chunks[i]) > 0 && isSectionMarker(chunks[i][0]) {
			move := []string{chunks[i][0]}
			if len(chunks[i]) > 1 && strings.TrimSpace(chunks[i][1]) != "" {
				move = append(move, chunks[i][1])
				chunks[i] = chunks[i][2:]
			} else {
				chunks[i] = chunks[i][1:]
			}
			chunks[i-1] = append(chunks[i-1], move...)
		}
	}
	var out []string
	for _, lines := range chunks {
		if raw := strings.Join(lines, ""); raw != "" {
			out = append(out, raw)
		}
	}
	return out
}

// splitSource 把长文按行分成每段不超过 maxRunes 个字符的几段；超过 maxSegments 段返回 ok=false。
// 只按行边界切，多字节字符、链接和 SectionMarker 行都不会被切开；单行本身超过上限时按句末标点再硬切。
func splitSource(s string, maxRunes, maxSegments int) ([]sourceChunk, bool) {
	var raws []string
	cur := ""
	flush := func() {
		if cur != "" {
			raws = append(raws, cur)
			cur = ""
		}
	}
	for _, line := range strings.SplitAfter(s, "\n") {
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > maxRunes {
			flush()
			for _, piece := range splitLongLine(line, maxRunes) {
				raws = append(raws, piece)
			}
			continue
		}
		if cur != "" && utf8.RuneCountInString(cur)+utf8.RuneCountInString(line) > maxRunes {
			flush()
		}
		cur += line
	}
	flush()
	raws = fixSectionMarkers(raws)
	var out []sourceChunk
	for _, raw := range raws {
		text := strings.TrimRight(raw, " \t\r\n")
		sep := raw[len(text):]
		if text == "" {
			if len(out) > 0 {
				out[len(out)-1].sep += raw
			}
			continue
		}
		out = append(out, sourceChunk{text: text, sep: sep})
	}
	if len(out) == 0 || len(out) > maxSegments {
		return nil, false
	}
	return out, true
}

// splitLongLine 切一行超过上限的文字：优先在句末标点后切，其次空白后，都找不到就在上限处按字符切。
func splitLongLine(line string, maxRunes int) []string {
	var out []string
	for rest := line; rest != ""; {
		if utf8.RuneCountInString(rest) <= maxRunes {
			out = append(out, rest)
			break
		}
		at := byteAfterRunes(rest, maxRunes)
		cut := at
		for i := at; i > at/2; i-- {
			if i >= len(rest) || !utf8.RuneStart(rest[i]) {
				continue
			}
			r, _ := utf8.DecodeLastRuneInString(rest[:i])
			if strings.ContainsRune("。！？!?；;.\n ", r) {
				cut = i
				break
			}
		}
		out = append(out, rest[:cut])
		rest = rest[cut:]
	}
	return out
}

// byteAfterRunes 返回前 n 个字符之后的字节位置（不切开多字节字符）。
func byteAfterRunes(s string, n int) int {
	at := 0
	for range n {
		if at >= len(s) {
			return len(s)
		}
		_, w := utf8.DecodeRuneInString(s[at:])
		at += w
	}
	return at
}

// joinSource 把各段译文按原顺序拼回，段与段之间接原来那段后面的空白；
// 硬切出来的段（后面没有空白）补一个换行，不把两句话粘在一起。Article 的 %% 行因此在原位保持单独一行。
func joinSource(parts []string, chunks []sourceChunk) string {
	var b strings.Builder
	for i := range chunks {
		b.WriteString(parts[i])
		sep := chunks[i].sep
		if sep == "" && i < len(chunks)-1 {
			sep = "\n"
		}
		b.WriteString(sep)
	}
	return strings.TrimSpace(b.String())
}

// call 调一次模型，单次超时就按 Retries 重新计时再试；外层 ctx 已经结束（外层时限到了、被取消）就不再试。
// timedOut 说最后一次是不是到期：单次时限和外层时限都算，运行记录照旧记 timeout，不记成 error。
func (t *Translator) call(ctx context.Context, timeout time.Duration, target, text string) (raw string, timedOut bool, err error) {
	for attempt := 0; ; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		raw, err = t.Call(cctx, System(target), User(text, target))
		timedOut = err != nil && (errors.Is(err, context.DeadlineExceeded) || cctx.Err() == context.DeadlineExceeded)
		cancel()
		if !timedOut || ctx.Err() != nil || attempt >= t.Retries {
			return raw, timedOut, err
		}
	}
}
