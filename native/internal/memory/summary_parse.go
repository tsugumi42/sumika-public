package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/pyspace"
)

// 后台整理的输出怎么读（规格-记忆-v2 13.2.6，W4）：一条坏不拖整批，格式完全不对才算失败。
// 三级：
//   - 格式级：拿不出一个像样的对象（不是 JSON、既没有 episode_record 也没有 ops、ops 不是数组、被截断得只剩半个 episode）→ ErrBadOutput，
//     调用方退避（B3）。修复出来 ops 是空数组是合法的："这批什么都不记"（Bun 把它当失败，拆解坑 8，有意改）
//   - 条目级：一条动作不合规（op 不认、kind/visibility 不在枚举里、text 空或超过 300 字、revoke 没有 id……）→ 丢这一条、计数，其余照记
//   - 超长：截断而不丢（ops 超过 20 条取前 20，episode 超过 800 字截到 800，src 超过 10 个取前 10）
//
// 提示词是 prompts/summarize-v2.md（W3a 起；summarize.md 是 Bun 读的旧版，一字不动），输出格式还是 {episode_record, episode_reason, episode, ops[]}，
// ops 在 Bun 的三种（upsert_fact、candidate、revoke）上加 evidence，revoke 加 src 栏。
// 数字都按 Unicode 码点（Go 的 len 是字节，一个汉字 3 字节），截断只在码点边界切（清单 ② 的 388b6977）。
// 错误和计数只写原因码，不写模型的输出（清单 ㉑：脱敏只管结构化字段，正文仍会泄漏）

// SummaryVersion 是流水线版本：memory_jobs 的幂等键带它。提示词或解析写法变了、要让同一批重新整理时加一（Bun 的 SUMMARY_VERSION 是 1，Go 从 W3a 的新提示词起是 2）。
// 版本也在整理的缓存键里：内容不变就复用，版本换了就不复用（边界 ⑲）。
// 撤回 W3a：git revert 它的几个提交（提示词、summaryPrompt、版本号、解析和写入层一起退）；只删文件或只改这个数不行，summaryPrompt 写死读 summarize-v2，文件没了每批整理都是 prompt_error
const SummaryVersion = 3

const (
	maxEpisodeChars = 800 // episode 最多多少字
	maxOps          = 20  // 一批最多认几条动作
	maxSrc          = 10  // 一条动作的 src 最多认几个
)

// 解析这一层数过的东西（BatchResult.Counts、memory_jobs.counts_json 的键）。整理那一步和写入那一步另有自己的键，合并进同一张表
const (
	CountRepaired          = "repaired"          // 回复原样解析不了，修过才读出来（截断、尾逗号、字符串里的换行）
	CountEpisodeNo         = "episode_no"        // episode_record 是 no：不存片段（B7）
	CountEpisodeYesEmpty   = "episode_yes_empty" // 写了 yes 却没有 episode 文字：没有可存的
	CountEpisodeMissing    = "episode_record_missing"
	CountEpisodeUnknown    = "episode_record_unknown" // 不是 "yes" / "no"（"maybe"、"YES"、数字……）：不存经历（ADR-314）
	CountEpisodeBool       = "episode_record_bool"    // 写成了布尔：布尔值均不存经历
	CountEpisodeBadType    = "episode_bad_type"       // episode 不是字符串
	CountEpisodeTruncated  = "episode_truncated"
	CountEpisodeReason     = "episode_reason" // 写了 episode_reason（只数，不存、不记原文）
	CountOpsOverLimit      = "ops_over_limit" // 第 21 条起丢掉的条数
	CountOpNotObject       = "op_not_object"
	CountOpUnknown         = "op_unknown"     // op 名字不认；update、dispute 现在没有生产者，也算不认的（13.2.10）；提示词被改得没有候选块（prompt_stale）时 evidence 也算
	CountEvidenceNoID      = "evidence_no_id" // evidence 没有 id（或不是字符串）
	CountKindInvalid       = "kind_invalid"
	CountVisibilityInvalid = "visibility_invalid"
	CountTextEmpty         = "text_empty"
	CountTextTooLong       = "text_too_long"
	CountSubjectInvalid    = "subject_invalid" // subject 不是字符串
	CountRevokeNoID        = "revoke_no_id"
	CountTemporalUnknown   = "temporal_unknown" // 时态不认：当没写（按种类取默认），不丢条
	CountSrcMissing        = "src_missing"      // 没写 src：按"没有 src"算
	CountSrcUnparsable     = "src_unparsable"   // src 写坏了（不是整数数组）：按"没有 src"算
	CountSrcTrimmed        = "src_trimmed"      // src 超过 10 个，取前 10
)

// ErrBadOutput：后台模型回的不是合法的整理格式，这一批没整理成，游标不动，调用方退避（B3）。errors.Is 认它；原因码在错误文字里
var ErrBadOutput = errors.New("后台模型回的不是合法的整理格式")

func badOutput(code string) error { return fmt.Errorf("%w（原因 %s）", ErrBadOutput, code) }

// counter 是一批里数过的东西
type counter map[string]int

func (c counter) add(key string)         { c[key]++ }
func (c counter) addN(key string, n int) { c[key] += n }

// sumOp 是过了条目级检查的一条动作。subject、supersedes、id 是模型写的原样，还没对过号（对号要这一批的说话人表和已知事实，在 summary_scope.go）
type sumOp struct {
	op         string // upsert_fact / candidate / revoke / evidence
	kind       string // fact / promise / preference / boundary
	text       string // 去掉首尾空白，1～300 个码点
	subject    string // 空 = 没写
	visibility string // 只有 upsert_fact 有："private" / "person" / "public"；没写是 ""（按 private 算，Bun 的默认值）
	supersedes string // 空 = 没写
	validUntil string
	temporal   string  // 认得的写法；不认的是 ""
	id         string  // revoke 要撤销的编号，evidence 要补证据的候选编号
	src        []int64 // 去重、最多 10 个；没写、写坏的是 nil。revoke、evidence 只认恰好一个（summary_scope.go 查）
	// srcN 是 src 去重之前写了几个序号（最多 10；没写、写坏的是 0）。upsert_fact、candidate 判"恰好引用一行"（ADR-231）数的是它，
	// 不是去重后的 len(src)：[5,5] 算两行，比 revoke、evidence 紧（它们的 [5,5] 去重后算一个）
	srcN    int
	onlyHer *bool // 只有明确的布尔 false 才可能进入共用池；缺失或类型不对留角色。
}

// summaryOut 是模型回复读完的结果
type summaryOut struct {
	record  string // "yes" / "no"；缺失或不认的是 ""（不存经历）
	episode string // 去掉首尾空白、截到 800 字；没有要存的是 ""
	ops     []sumOp
	counts  counter
}

// parseSummary 读模型的回复。错误只有 ErrBadOutput 一种（格式级）；条目级的坏处丢掉那一条，数在 counts 里
func parseSummary(raw string) (*summaryOut, error) {
	obj, repaired, err := extractObject(raw)
	if err != nil {
		return nil, err
	}
	out := &summaryOut{counts: counter{}}
	if repaired {
		out.counts.add(CountRepaired)
	}
	// null 当没写
	hasRecord := obj["episode_record"] != nil
	opsRaw := obj["ops"]
	if !hasRecord && opsRaw == nil {
		return nil, badOutput("no_fields")
	}
	var list []any
	if opsRaw != nil {
		l, ok := opsRaw.([]any)
		if !ok {
			return nil, badOutput("ops_not_array")
		}
		list = l
	}

	out.record, out.episode = parseEpisode(obj, out.counts)

	if len(list) > maxOps {
		out.counts.addN(CountOpsOverLimit, len(list)-maxOps)
		list = list[:maxOps]
	}
	for _, item := range list {
		if op, ok := parseOp(item, out.counts); ok {
			out.ops = append(out.ops, op)
		}
	}
	return out, nil
}

// parseEpisode 只允许显式字符串 yes 保存经历。其余记原因并跳过经历，合法事实仍可处理（ADR-314）。
func parseEpisode(obj map[string]any, c counter) (record, episode string) {
	switch v := obj["episode_record"].(type) {
	case nil:
		c.add(CountEpisodeMissing)
	case string:
		if v == "yes" || v == "no" {
			record = v
		} else {
			c.add(CountEpisodeUnknown)
		}
	case bool:
		c.add(CountEpisodeBool)
		if !v {
			record = "no"
		}
	default:
		c.add(CountEpisodeUnknown)
	}
	if s, ok := obj["episode_reason"].(string); ok && strings.TrimSpace(s) != "" {
		c.add(CountEpisodeReason)
	}
	text := ""
	switch v := obj["episode"].(type) {
	case nil:
	case string:
		text = strings.TrimSpace(v)
	default:
		c.add(CountEpisodeBadType)
	}
	switch {
	case record != "yes":
		if record == "no" {
			c.add(CountEpisodeNo)
		}
		return "no", ""
	case text == "":
		if record == "yes" {
			c.add(CountEpisodeYesEmpty)
		}
		return record, ""
	}
	if utf8.RuneCountInString(text) > maxEpisodeChars {
		text = cutEpisode(text)
		c.add(CountEpisodeTruncated)
	}
	return record, text
}

// cutEpisode 把超过 800 字的 episode 截到 800 字以内：优先在句末（。！？.!?…换行）截，句末太靠前（前一半）就直接切在第 800 个码点上。
// 只在码点边界切，不会留下半个字符
func cutEpisode(text string) string {
	runes := []rune(text)[:maxEpisodeChars]
	for i := len(runes) - 1; i >= maxEpisodeChars/2; i-- {
		if strings.ContainsRune("。！？.!?…\n", runes[i]) {
			runes = runes[:i+1]
			break
		}
	}
	return strings.TrimSpace(string(runes))
}

var (
	factKindSet   = map[string]bool{"fact": true, "promise": true, "preference": true, "boundary": true}
	visibilitySet = map[string]bool{"private": true, "person": true, "public": true}
	// 时态：Bun 的六种写法（memory.ts 的 Temporal）。不认的记 null，不丢条；过期规则归第 3 步（ADR-075），这里只存
	temporalSet = map[string]bool{"stable": true, "now": true, "temporary": true, "past": true, "plan": true, "stopped": true}
)

// parseOp 读一条动作。不合规返回 false，原因记进 counts
func parseOp(item any, c counter) (sumOp, bool) {
	m, ok := item.(map[string]any)
	if !ok {
		c.add(CountOpNotObject)
		return sumOp{}, false
	}
	name, _ := m["op"].(string)
	op := sumOp{op: strings.TrimSpace(name)}
	switch op.op {
	case "upsert_fact", "candidate", "revoke", "evidence":
	default:
		c.add(CountOpUnknown)
		return sumOp{}, false
	}

	if op.op == "revoke" || op.op == "evidence" {
		id, _ := m["id"].(string)
		if op.id = strings.TrimSpace(id); op.id == "" {
			if op.op == "revoke" {
				c.add(CountRevokeNoID)
			} else {
				c.add(CountEvidenceNoID)
			}
			return sumOp{}, false
		}
		// 这两种动作的 src 就是依据（撤销：主人说"不对"的那一行；证据：再次说到它的那一行），只认恰好一个序号，由 summary_scope.go 查；
		// 没写、写坏的当没有，那边计数丢掉。其余的字段（status、confirmed_by、by_subject、speaker、day……）不读，模型写了也没用
		op.src, op.srcN = parseSrc(m["src"], c, false)
		return op, true
	}

	kind, _ := m["kind"].(string)
	if op.kind = strings.TrimSpace(kind); !factKindSet[op.kind] {
		c.add(CountKindInvalid)
		return sumOp{}, false
	}
	text, _ := m["text"].(string)
	op.text = pyspace.Trim(text) // 同 remember：判空按嵌入服务的空白集合，不用 TrimSpace
	switch n := utf8.RuneCountInString(op.text); {
	case n == 0:
		c.add(CountTextEmpty)
		return sumOp{}, false
	case n > maxFactChars:
		c.add(CountTextTooLong)
		return sumOp{}, false
	}
	switch v := m["subject"].(type) {
	case nil:
	case string:
		op.subject = strings.TrimSpace(v)
	default:
		c.add(CountSubjectInvalid)
		return sumOp{}, false
	}
	if op.op == "upsert_fact" {
		switch v := m["visibility"].(type) {
		case nil:
		case string:
			if op.visibility = strings.TrimSpace(v); !visibilitySet[op.visibility] {
				c.add(CountVisibilityInvalid)
				return sumOp{}, false
			}
		default:
			c.add(CountVisibilityInvalid)
			return sumOp{}, false
		}
		// supersedes 写成别的类型当没写：它指向的东西要在已知事实里对号，对不上本来就是去掉
		op.supersedes, _ = m["supersedes"].(string)
		op.supersedes = strings.TrimSpace(op.supersedes)
	}
	if v, ok := m["temporal"]; ok && v != nil {
		if s, _ := v.(string); temporalSet[strings.TrimSpace(s)] {
			op.temporal = strings.TrimSpace(s)
		} else {
			c.add(CountTemporalUnknown)
		}
	}
	if v, exists := m["valid_until"]; exists && v != nil {
		var ok bool
		op.validUntil, ok = v.(string)
		if !ok || strings.TrimSpace(op.validUntil) == "" {
			c.add("invalid_lifetime")
			return sumOp{}, false
		}
	}
	op.src, op.srcN = parseSrc(m["src"], c, true)
	if v, ok := m["only_her"].(bool); ok {
		op.onlyHer = &v
	}
	return op, true
}

// parseSrc 读 src：这条是从哪几句看出来的，要的是消息序号的整数数组。没写、空数组、写坏的（有一个不是正整数）都按"没有 src"算，
// 并数下来，不静默（13.2.6）；超过 10 个取前 10；重复的去掉。count 为假（revoke）时没写不数。
// 第二个返回值是去重之前写了几个：upsert_fact、candidate 要按它判"恰好引用一行"（ADR-231），同一个序号写两遍不能折成一行
func parseSrc(v any, c counter, count bool) (out []int64, written int) {
	var list []any
	switch v := v.(type) {
	case nil:
	case []any:
		list = v
	default:
		c.add(CountSrcUnparsable)
		return nil, 0
	}
	if len(list) == 0 {
		if count {
			c.add(CountSrcMissing)
		}
		return nil, 0
	}
	if len(list) > maxSrc {
		list = list[:maxSrc]
		c.add(CountSrcTrimmed)
	}
	for _, e := range list {
		n, ok := asSeq(e)
		if !ok {
			c.add(CountSrcUnparsable)
			return nil, 0
		}
		dup := false
		for _, o := range out {
			dup = dup || o == n
		}
		if !dup {
			out = append(out, n)
		}
	}
	return out, len(list)
}

// asSeq：JSON 数字里的正整数。12 和 12.0 都认，12.5、"12"、负数、0 不认
func asSeq(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	if i, err := n.Int64(); err == nil {
		return i, i > 0
	}
	f, err := n.Float64()
	if err != nil || f != math.Trunc(f) || f <= 0 || f > 1<<53 {
		return 0, false
	}
	return int64(f), true
}

// ───────────── 从回复里取出 JSON 对象 ─────────────
// 照 daemon/util/json-extract.ts（ADR-122 第 2 条），Go 自己写，不依赖 zod 也没有 jsonrepair：
//  1. 第一个 { 到最后一个 }，原样解析（正常的 JSON 走这一步，不被改动；模型在前后说话、包代码块围栏也走这一步）；
//  2. 不行就取第一个括号配得上的那一段（后面跟着带括号的说明文字时）；
//  3. 还不行才修：字符串里的换行、制表符转义，尾逗号去掉，被截断的补收尾括号（数组里正写到一半的那一项整项丢掉，不会修出一个字写了一半的项）。
//
// 没抄 jsonrepair 的单引号、注释、省略号、缺逗号这些修法：模型实际出过的是截断、围栏和字符串里的换行；
// 以后在 W6 的真实回放里见到别的坏法再补，补一种加一行测试

// extractObject 取出回复里的 JSON 对象。repaired 为真说明原样解析不了、是修出来的。
// 被截断修出来的结果还要有 ops 数组：截在 episode 的字符串中间时，修出来的 episode 是半句话，不能当成整理成功（Bun 要求 ops 非空，同一个道理；
// 这里只要求有 ops 数组、可以为空，"ops: []"合法）
func extractObject(raw string) (obj map[string]any, repaired bool, err error) {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return nil, false, badOutput("no_json")
	}
	if last := strings.LastIndexByte(raw, '}'); last > start {
		if m, ok := decodeObject(raw[start : last+1]); ok {
			return m, false, nil
		}
	}
	if first := balancedObject(raw[start:]); first != "" {
		if m, ok := decodeObject(first); ok {
			return m, false, nil
		}
	}
	fixed, truncated := repairJSON(raw[start:])
	m, ok := decodeObject(fixed)
	if !ok {
		return nil, false, badOutput("json_invalid")
	}
	if truncated {
		if _, has := m["ops"].([]any); !has {
			return nil, false, badOutput("truncated_no_ops")
		}
	}
	return m, true, nil
}

// decodeObject 解析一个 JSON 对象，数字保持原样（json.Number），后面不许跟别的东西
func decodeObject(s string) (map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return m, true
}

// balancedObject 取 s（以 { 开头）里第一个括号配得上的一段，字符串里的括号不算；配不上返回空
func balancedObject(s string) string {
	depth, inStr, esc := 0, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr && esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case inStr:
			inStr = c != '"'
		case c == '"':
			inStr = true
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			if depth--; depth == 0 {
				return s[:i+1]
			}
		}
	}
	return ""
}

// repairJSON 修一段以 { 开头的文字，返回修过的和"是不是被截断的"（括号没配完或字符串没收尾）。
// 被截断时：数组里正写到一半的那个对象整个丢掉，只补收尾括号；停在没写完的键后面补 null；停在字符串中间先补引号
func repairJSON(s string) (string, bool) {
	type frame struct {
		open byte
		at   int // 这个括号在 buf 里的位置
	}
	var (
		buf        = make([]byte, 0, len(s)+16)
		stack      []frame
		inStr, esc bool
		closed     bool
	)
	for i := 0; i < len(s) && !closed; i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
				buf = append(buf, c)
			case c == '\\':
				esc = true
				buf = append(buf, c)
			case c == '"':
				inStr = false
				buf = append(buf, c)
			case c == '\n':
				buf = append(buf, '\\', 'n')
			case c == '\r':
				buf = append(buf, '\\', 'r')
			case c == '\t':
				buf = append(buf, '\\', 't')
			case c < 0x20:
				buf = append(buf, fmt.Sprintf(`\u%04x`, c)...)
			default:
				buf = append(buf, c)
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
			buf = append(buf, c)
		case '{', '[':
			stack = append(stack, frame{c, len(buf)})
			buf = append(buf, c)
		case '}', ']':
			buf = append(buf, c)
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			closed = len(stack) == 0
		case ',':
			// 尾逗号：后面第一个不是空白的字符是收尾括号，这个逗号不要
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\r' || s[j] == '\n') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
			buf = append(buf, c)
		default:
			buf = append(buf, c)
		}
	}
	if closed && !inStr {
		return string(buf), false
	}
	// 被截断：先把数组里没写完的那个对象整个去掉（最里面的、父亲是数组的那个）
	for k := len(stack) - 1; k >= 1; k-- {
		if stack[k].open == '{' && stack[k-1].open == '[' {
			buf = buf[:stack[k].at]
			stack = stack[:k]
			inStr = false
			break
		}
	}
	if inStr {
		buf = append(buf, '"')
	}
	buf = bytes.TrimRight(buf, " \t\r\n,")
	if n := len(buf); n > 0 && buf[n-1] == ':' {
		buf = append(buf, "null"...)
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].open == '{' {
			buf = append(buf, '}')
		} else {
			buf = append(buf, ']')
		}
	}
	return string(buf), true
}
