package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"sumika-native/internal/identity"
	"sumika-native/internal/prompts"
)

// 后台整理（规格-记忆-v2 13.2.4，W4）：读一批聊天记录 → 选材 → 排给模型看 → 调模型 → 读回复（summary_parse.go）→ 落成动作（summary_scope.go）→ 一个事务写库。
// 这里是"一批"怎么整理；什么时候整理、同一个会话排队、失败了怎么退避、停掉的顺序是 W5（13.2.5），不在这一步：
// SummarizeBatch 自己不写 failures / retry_at（失败了怎么记、什么时候再试是调用方的事，W5 在 memplugin 里做，库里的写法见 summary_state.go），
// 但开头会看：还在退避里、或者连着失败太多次停了，没带 force 就不做（B3）；force 不看退避（B4）。
// 日志和错误只写计数和原因码，不写原文、模型输出，也不写会话 id（QQ 会话 id 里有号码，B16、边界 ㉑）

// BatchResult 是一批的结局：没做的、没写的都要说清原因（4.3 每一步记数量和失败原因）
type BatchResult struct {
	Skipped string         // 没做的原因（下面的 Skip…）；空 = 做了
	Cursor  int64          // 游标挪到了哪（没挪就是原来的）
	Counts  map[string]int // 按动作、按丢弃原因计数
	// More：游标后面还有没读到的（这一批读满了 200 条，或凑够字数截断了）。调用方接着排下一批；
	// 在没识完的图前面截断的不算（图到了才有得读，那是等，不是接着读）
	More bool
}

// Skipped 的原因
const (
	SkipNothing     = "没有可整理的材料" // 没有新的，或读到的都被丢了、只有占位
	SkipDone        = "已整理过"     // 这一批（会话、起止 seq、版本）整理过：只挪游标，不调模型（B2）
	SkipNoModel     = "没选后台模型"   // 游标不动，不算失败（B6）
	SkipNoCursor    = "新会话还没立起点" // 没有游标行：不回填历史，起点由调用方 InitCursor（13.2.4 第 1 步，W5 的启动扫描立）
	SkipWaitImages  = "等识图结果"    // 第一条就是还没识完的图：游标不越过它，图到了再整理（边界 ⑧）
	SkipGeneration  = "忘掉或真删期间算的，作废"
	SkipCursorMoved = "游标已经被别的批挪过"
	SkipBackoff     = "退避中"        // 上次失败不久，还没到下次的时间；没带 force 不做（B3）
	SkipStopped     = "连着失败太多次，停了" // 失败满 MaxFailures 次：等新通知或 force 再开（13.2.5）
	// SkipPlace：这个会话的场合没在放行名单里（memory.write.places，规格-记忆写入上线 W5）。游标不动、不调模型、不写库；
	// 排程把它和 SkipNoModel 同类处理（停了等新通知，不重排、不计失败）
	SkipPlace = "场合没放行"
)

// 一批整理数过的东西（Counts 的键）。解析那一层的在 summary_parse.go，写入那一层的在 write.go
const (
	CountRead              = "read"                // 读了几条（被丢的也算）
	CountKept              = "kept"                // 留给模型几条
	CountModelCalls        = "model_calls"         // 调了几次模型
	CountDroppedKind       = "dropped_kind"        // kind 不是 message
	CountDroppedAutomation = "dropped_automation"  // automation:* 发的
	CountDroppedBackfill   = "dropped_backfill"    // 补录的（ADR-092）
	CountDroppedSelftest   = "dropped_selftest"    // 自检的
	CountDroppedUnseen     = "dropped_unseen"      // 她没看见（陌生人私聊）
	CountDroppedRevoked    = "dropped_revoked"     // 撤回了
	CountDroppedUnsent     = "dropped_unsent"      // 她的回复没发出去
	CountOnlyPlaceholder   = "only_placeholder"    // 只有占位：不调模型
	CountWaitImages        = "wait_images"         // 在还没识完的图前面截断
	CountBatchFull         = "batch_full"          // 凑够 6000 字，剩下的留给下一批
	CountEventClipped      = "event_clipped"       // 一条就超过 6000 字，截掉
	CountKnownOverLimit    = "known_over_limit"    // 有效事实多于 40 条，更早的撤不掉、换不了（B9 的硬顶）
	CountStartupNoBackfill = "startup_no_backfill" // 新会话，不回填
	CountDroppedGeneration = "dropped_generation"  // 世代号变了，整批作废
	CountDroppedCursor     = "dropped_cursor"      // 游标被别的批挪过，整批作废
	CountSkippedBackoff    = "skipped_backoff"     // 退避中，没带 force，没做
	CountSkippedStopped    = "skipped_stopped"     // 停了，没带 force，没做
	CountPlaceSkipped      = "place_skipped"       // 场合没放行，没做（W5）
	CountSecretDropped     = "secret_dropped"      // 事实或片段正文里有密钥形状，丢掉没写（W5，secret.go）
	CountBadOutput         = "bad_output"          // 格式级失败
	CountOpUpsert          = "op_upsert_fact"      // 模型给的、过了检查的动作，按种类
	CountOpCandidate       = "op_candidate"
	CountOpRevoke          = "op_revoke"
	CountSubjectUnknown    = "subject_unknown"    // subject 对不上说话人表
	CountSupersedesUnknown = "supersedes_unknown" // 要替换的编号不在这一批给它看的已知事实里：去掉，新事实照记
	CountRevokeUnknown     = "revoke_unknown"     // 要撤销的编号不在里面：丢掉
	CountRevokeRefused     = "revoke_refused"     // src 里没有主人的话：不撤销
	CountSrcOutOfBatch     = "src_out_of_batch"   // src 里有不是这一批的编号：整个按没有 src 算
	CountSrcNoEvidence     = "src_no_evidence"    // src 只指向她自己的话、她的号上别人发的：没有证据
	CountScopeNotNarrowed  = "scope_not_narrowed" // 模型要收紧（private 或没写），但范围里没有 conv:* 可去：保持表里的范围，不换成别的

	// evidence 动作（W3a）。记成了几次证据看写入层的 evidence_added / evidence_repeat，写入时候选已经不是待确认的是 evidence_gone
	CountOpEvidence          = "op_evidence"           // 过了检查、交给写入层的 evidence 动作
	CountEvidenceUnknown     = "evidence_unknown"      // id 不是这一批候选块里的编号（编的、没给它看的、已知事实的）：丢
	CountEvidenceBadSrc      = "evidence_bad_src"      // src 没写、写坏的、不是恰好一个序号：丢
	CountEvidenceNotHuman    = "evidence_not_human"    // src 那一行不是真人的话（她自己的、别的角色的、她的号上别人发的）：丢
	CountEvidenceDup         = "evidence_dup"          // 同一批里对同一条候选又写了一次：只认第一次
	CountCandidatesOverLimit = "candidates_over_limit" // 这个场合看得见的候选多于 20 条，更早的这一批看不到、也就补不了证据
	CountPromptStale         = "prompt_stale"          // 整理提示词里没有 {{candidates}}：这一批不给候选块，evidence 当不认的动作丢
)

// summaryPrompt：整理用的提示词，prompts/ 下的文件名（W3a 起是 summarize-v2；summarize.md 是 Bun 读的旧版，Go 不读）
const summaryPrompt = "summarize-v2"

// candidatesVar：提示词里放候选块的占位符。用户在设置里改 summarize-v2.md 时很可能把它删了，
// 删了的话 evidence 永远不会出现，功能默默失效；所以整理前查一遍，没有就整个关掉（不给候选、不认 evidence）并提醒
const candidatesVar = "{{candidates}}"

// ErrPromptStale：整理提示词里没有候选块的占位符，待确认事实的证据功能关着。errors.Is 认它，错误文字里说清是哪个文件
var ErrPromptStale = errors.New("整理提示词里没有 " + candidatesVar + "（原因 prompt_stale），待确认事实的证据功能关着")

// promptStale：这份提示词原文还能不能用。edited 是读到的是用户改过的那份（数据目录的 config/prompts/），不是出厂的
func promptStale(raw string, edited bool) error {
	if strings.Contains(raw, candidatesVar) {
		return nil
	}
	where := "出厂的 prompts/" + summaryPrompt + ".md"
	if edited {
		where = "你改过的 config/prompts/" + summaryPrompt + ".md"
	}
	return fmt.Errorf("%w：%s", ErrPromptStale, where)
}

// readSummaryPrompt 读整理提示词原文并查占位符。err 是读不到（原因 prompt_error），stale 非空是占位符没了（原文照样给，填变量时候选块就是空的）
func (m *Memory) readSummaryPrompt() (raw string, stale, err error) {
	raw, edited, err := m.opt.Prompts.Read(summaryPrompt)
	if err != nil {
		return "", nil, fmt.Errorf("读整理提示词（原因 prompt_error）：%w", err)
	}
	return raw, promptStale(raw, edited), nil
}

// warnStale：整理提示词被改得没有候选块，提醒一次（说清是哪个文件），之后每批只计数不再说。日志里只有文件名和原因码
func (w *writer) warnStale(stale error) {
	w.staleOnce.Do(func() { w.logf("记忆[%s]：%v", w.m.opt.Character, stale) })
}

// CheckSummaryPrompt：启动时查一遍（memplugin 的启动扫描调它，每次整理前 SummarizeBatch 自己也查）。
// 返回 nil 是正常；ErrPromptStale 是候选块的占位符被改没了（证据功能关着）；别的错误是提示词读不到
func (m *Memory) CheckSummaryPrompt() error {
	_, stale, err := m.readSummaryPrompt()
	if err != nil {
		return err
	}
	return stale
}

const (
	maxBatchEvents    = 200              // 一批最多读几条
	maxBatchRunes     = 6000             // 一批给模型的字数（码点），起步值；超过的留给下一批
	maxKnown          = 40               // 给模型看的已知事实：最多40条，相关优先、最近补足
	maxCandidateBlock = 20               // 给模型看的"听说过、还没确认的"候选：最多20条，相关优先、最近补足
	imageWait         = 10 * time.Minute // 图等识图最多等多久，起步值；等够了按占位整理、标"未看清"
	summaryWriteWait  = 30 * time.Second // 写库等写锁最多等多久：模型已经花过钱了，不像 remember 只等 5 秒
	quoteChars        = 20               // "↪引用"只取被引用那条的开头几个字
)

// SummarizeBatch 整理一个会话的下一批（13.2.4）。按顺序：
//  0. 场合（W5）：会话不在放行名单里（PlaceMayWrite）返回 SkipPlace，什么都不读不写；平台私聊且名单只有 owner-private 的，读完事件判出不是主人的私聊也返回 SkipPlace
//  1. 游标：没有游标行的新会话不回填历史，返回 SkipNoCursor（起点由调用方用 InitCursor 立）。一批最多 200 条，且给模型的字数不超过 6000，
//     超过的留给下一批，游标只挪到实际读完的那条
//  2. 选材（B8）：丢掉 automation、补录、自检、陌生人、撤回的、没发出去的、kind 不是 message 的；留在聊天记录里但不能当证据的：她自己的话、
//     别的角色的话、她的号上别人发的。只剩被丢的或只有占位的，不调模型，游标挪过去。第一条没识完的图前面截断
//  3. 幂等键 (会话, 起 seq, 止 seq, 版本)：整理过只挪游标（B2）
//  4. 已知事实：这个场合能看的最多40条有效事实（相关优先）；再加"听说过、还没确认的"：这个场合能看的最多20条待确认的（相关优先），模型对它们只能写 evidence（W3a）
//  5. 聊天记录：直接用 Event 的字段排，不从存好的文字里反解（B12）
//  6. 调模型：没选后台模型就跳过（B6）；调之前读世代号，写的时候比对（忘掉、真删期间算的作废）
//  7. 读回复，8. 一个事务写库（B1），9. 向量异步补，10. 日志只写计数
//
// 格式级失败返回 ErrBadOutput（调用方退避，W5），模型调用出错、写锁等不到也是错误，原样返回；这几种游标都不动
func (m *Memory) SummarizeBatch(ctx context.Context, conv string, force bool) (BatchResult, error) {
	w, err := m.writer()
	if err != nil {
		return BatchResult{}, err
	}
	if !m.SummarizeEnabled() {
		return BatchResult{}, ErrSummarizeOff
	}
	es, err := m.events()
	if err != nil {
		return BatchResult{}, err
	}
	gen, model, err := m.background()
	if err != nil {
		return BatchResult{}, err
	}
	if err := m.FlushShared(ctx); err != nil {
		return BatchResult{}, err
	}
	counts := counter{}
	more := false
	result := func(skipped string, cursor int64) BatchResult {
		return BatchResult{Skipped: skipped, Cursor: cursor, Counts: counts, More: more}
	}

	// 0. 场合（W5，唯一的咽喉）：会话 id 不在放行名单里或认不出，就不读游标、不读事件、不调模型、不写库
	if !m.PlaceMayWrite(conv) {
		shape := "unknown"
		if p, ok := convPlace(conv); ok {
			shape = writePlaceOf(p)
		}
		w.skipPlace(conv, counts, shape)
		return result(SkipPlace, 0), nil
	}

	// 1. 游标和读取
	st, ok, err := m.SummaryState(ctx, conv)
	if err != nil {
		return BatchResult{}, err
	}
	if !ok {
		counts.add(CountStartupNoBackfill)
		return result(SkipNoCursor, 0), nil
	}
	cursor := st.LastSeq
	// 退避（B3）：排队的整理在执行时才看（B5），所以放在这里而不是排队的时候；force（压缩前、整理按钮、归档）不看（B4）
	if !force {
		switch {
		case st.Failures >= MaxFailures:
			counts.add(CountSkippedStopped)
			return result(SkipStopped, cursor), nil
		case st.Failures > 0 && st.RetryAt > m.now().UnixMilli():
			counts.add(CountSkippedBackoff)
			return result(SkipBackoff, cursor), nil
		}
	}
	evs, err := es.Events(ctx, conv, cursor, maxBatchEvents)
	if err != nil {
		return BatchResult{}, &reasoned{"读事件", "events_error", err}
	}
	b := m.selectBatch(evs, cursor, counts)
	// 平台私聊且名单里只有 owner-private：这是不是主人的私聊要读了事件才知道。必须在第一个 apply 之前判（下面的"已整理过"
	// 和"只有占位"都会挪游标），别人的私聊游标一个字不动
	place := PlaceOf(ChannelOf(conv), conv, "")
	var ownerPrivate, haveSide bool
	var partner string
	if place == PlatformPrivate && m.ownerPrivateOnly() {
		ownerPrivate, partner = m.privateSide(ctx, es, conv, b.kept)
		haveSide = true
		if !ownerPrivate {
			counts = counter{}
			w.skipPlace(conv, counts, "platform-private-not-owner")
			return result(SkipPlace, cursor), nil
		}
	}
	more = counts[CountWaitImages] == 0 && (b.read >= maxBatchEvents || counts[CountBatchFull] > 0)
	if b.read == 0 {
		if counts[CountWaitImages] > 0 {
			return result(SkipWaitImages, cursor), nil
		}
		return result(SkipNothing, cursor), nil
	}
	job := &Job{Conversation: conv, From: cursor + 1, To: b.last, Version: SummaryVersion, Prev: &cursor, Counts: counts}

	// 3. 幂等键
	done, err := m.JobDone(ctx, conv, job.From, job.To, SummaryVersion)
	if err != nil {
		return BatchResult{}, err
	}
	if done {
		if _, err := m.apply(ctx, Batch{By: BySummary, Job: job, Wait: summaryWriteWait}); err != nil {
			return BatchResult{}, err
		}
		return result(SkipDone, b.last), nil
	}
	// 只剩被丢的、只有占位的：不调模型，游标挪过去，不计失败（B8）
	if len(b.kept) == 0 || b.onlyPlaceholder() {
		if len(b.kept) > 0 {
			counts.addN(CountOnlyPlaceholder, len(b.kept))
		}
		if _, err := m.apply(ctx, Batch{By: BySummary, Job: job, Wait: summaryWriteWait}); err != nil {
			return m.settle(result("", cursor), err)
		}
		return result(SkipNothing, b.last), nil
	}

	if model == "" {
		return result(SkipNoModel, cursor), nil
	}

	// 6 的前半：世代号在读已知事实之前读，忘掉、真删发生在这以后的，这一批的结果落不了库
	generation, err := m.Generation(ctx)
	if err != nil {
		return BatchResult{}, err
	}

	// 提示词原文：查还有没有候选块的占位符（被改掉了就整个关掉 evidence，prompt_stale），然后填变量，查和用是同一份
	raw, stale, err := m.readSummaryPrompt()
	if err != nil {
		return BatchResult{}, err
	}
	if stale != nil {
		counts.add(CountPromptStale)
		w.warnStale(stale)
	}

	// 4、5. 已知事实和聊天记录
	pl := &plan{now: m.now(), conv: conv, place: place, noEvidence: stale != nil}
	if pl.place == PlatformPrivate {
		if haveSide {
			pl.ownerPrivate, pl.partner = ownerPrivate, partner
		} else {
			pl.ownerPrivate, pl.partner = m.privateSide(ctx, es, conv, b.kept)
		}
	}
	aud := pl.audience()
	related := m.relatedSummaryFacts(ctx, aud, b)
	known, shown := m.knownFacts(aud, counts, related)
	pl.known = shown
	candidates := ""
	if stale == nil {
		candidates, pl.cands = m.candidateFacts(aud, counts, related)
	}
	transcript, speakers := m.transcript(b, pl)
	pl.firstSeq, pl.lastSeq = b.first, b.last

	promptText := prompts.FillVars(raw, map[string]string{
		"name": m.herName(), "owner_name": m.ownerCall(), "now": nowText(m.now()), "known": known, "candidates": candidates, "speakers": speakers, "transcript": transcript,
	})

	// 6. 调模型
	counts.add(CountModelCalls)
	reply, err := gen.Call(ctx, model, promptText, summaryCacheKey(m.opt.Character, conv, SummaryVersion))
	if err != nil {
		w.logf("记忆[%s]：这一批整理失败（原因 model_call）", m.opt.Character)
		return BatchResult{Cursor: cursor, Counts: counts}, &reasoned{"调后台模型", "model_call", err}
	}

	// 7. 读回复
	out, err := parseSummary(reply.Text)
	if err != nil {
		counts.add(CountBadOutput)
		w.logf("记忆[%s]：这一批整理失败（%v）", m.opt.Character, err)
		return BatchResult{Cursor: cursor, Counts: counts}, err
	}
	for k, v := range out.counts {
		counts.addN(k, v)
	}

	// 8. 落成动作，一个事务写库
	ops := pl.ops(out, counts)
	// Audience：写入层按它判断"转述的证据是不是来自看得见这条候选的会话"（evidence_hidden），和给模型看的已知事实、候选块是同一个听众
	res, err := m.apply(ctx, Batch{By: BySummary, Ops: ops, Job: job, Generation: &generation, Wait: summaryWriteWait, Audience: &aud})
	if err != nil {
		return m.settle(result("", cursor), err)
	}
	if res.Duplicate {
		// 调模型的时候，同一批被另一个调用先整理完了：它写过了，这里什么都没写（B2）
		return result(SkipDone, b.last), nil
	}
	for k, v := range res.Counts {
		counts.addN(k, v)
	}
	w.logf("记忆[%s]：整理了一批，%s", m.opt.Character, countsText(counts))
	return result("", b.last), nil
}

// reasoned 是只带原因码的错误：Error() 里没有底下那个错误的原文（事件库的 SQL 里有会话 id，服务商的错误体里可能有 key 和提示词的回声），
// 调用方随手 log 它也不会漏；errors.Is / As 照样能穿过去认底下的错误
type reasoned struct {
	what, code string
	err        error
}

func (e *reasoned) Error() string { return fmt.Sprintf("%s失败（原因 %s）", e.what, e.code) }
func (e *reasoned) Unwrap() error { return e.err }

// settle 把写库的错误分成"这一批作废了"（世代号变了、游标被别的批挪过：不是失败，计数、不写库）和真的出错
func (m *Memory) settle(r BatchResult, err error) (BatchResult, error) {
	switch {
	case errors.Is(err, ErrGenerationChanged):
		counter(r.Counts).add(CountDroppedGeneration)
		r.Skipped = SkipGeneration
		return r, nil
	case errors.Is(err, ErrCursorMoved):
		counter(r.Counts).add(CountDroppedCursor)
		r.Skipped = SkipCursorMoved
		return r, nil
	}
	return r, err
}

// ───────────── 2. 选材 ─────────────

// keptEvent 是留给模型看的一条：正文已经把图片描述放回占位、转义过
type keptEvent struct {
	ev    Event
	body  string
	brief bool // 有图片描述（有内容，不算占位）
}

// batch 是读到的一批
type batch struct {
	first, last int64 // 读到的第一条和最后一条的 seq（被丢的也算）
	read        int
	kept        []keptEvent
}

func (b *batch) consume(e Event) {
	if b.first == 0 {
		b.first = e.Seq
	}
	b.last = e.Seq
	b.read++
}

// onlyPlaceholder：留下的每一条都只有占位（[图片]、表情、"嗯"），没有可记的内容。有图片描述的不算
func (b *batch) onlyPlaceholder() bool {
	for _, k := range b.kept {
		if k.brief || !onlyPlaceholder(k.ev.Text) {
			return false
		}
	}
	return true
}

var placeholderRE = regexp.MustCompile(`\[(?:图片|表情包|表情|语音|视频|商城表情：[^\]]*)\]`)

// 一个字的语气词，单独成句没有内容
const trivialRunes = "嗯哦啊哈草额诶欸噢哇呃嘿"

func onlyPlaceholder(text string) bool {
	for _, r := range placeholderRE.ReplaceAllString(text, "") {
		if !unicode.IsSpace(r) && !unicode.IsPunct(r) && !unicode.IsSymbol(r) && !strings.ContainsRune(trivialRunes, r) {
			return false
		}
	}
	return true
}

// dropReason：这一条要不要丢（B8）。空 = 留下。过滤放在记忆包里，store 不替它丢（13.2.2）
func dropReason(e Event) string {
	switch {
	case e.Kind != "message":
		return CountDroppedKind
	case strings.HasPrefix(e.Origin, "automation:"):
		return CountDroppedAutomation
	case e.Backfill:
		return CountDroppedBackfill
	case e.Selftest:
		return CountDroppedSelftest
	case !e.Seen:
		return CountDroppedUnseen
	case e.Revoked:
		return CountDroppedRevoked
	case e.Unsent:
		return CountDroppedUnsent
	}
	return ""
}

// selectBatch 顺着读到的事件往下走，丢该丢的、留该留的；在还没识完的图前面截断，凑够字数也截断。被丢的行也算读过（游标越过它们，不会反复读到）
func (m *Memory) selectBatch(evs []Event, cursor int64, counts counter) batch {
	var b batch
	now := m.now()
	total := 0
	for _, e := range evs {
		if e.Seq <= cursor {
			continue // 事件来源该给 afterSeq 之后的；多给的当没看见
		}
		if why := dropReason(e); why != "" {
			counts.add(why)
			b.consume(e)
			continue
		}
		body, brief, pending := m.describe(e, now)
		if pending {
			counts.add(CountWaitImages)
			break
		}
		cost := utf8.RuneCountInString(body)
		if len(b.kept) > 0 && total+cost > maxBatchRunes {
			counts.add(CountBatchFull)
			break
		}
		if cost > maxBatchRunes {
			// 一条就超过上限：不截掉的话这一批永远超，也不能跳过它
			body, cost = clip(body, maxBatchRunes), maxBatchRunes
			counts.add(CountEventClipped)
		}
		total += cost
		b.kept = append(b.kept, keptEvent{ev: e, body: body, brief: brief})
		b.consume(e)
	}
	counts.addN(CountRead, b.read)
	counts.addN(CountKept, len(b.kept))
	return b
}

var imageTokenRE = regexp.MustCompile(`\[(图片|表情包)\]`)

// describe 把一条消息的正文排给模型看：转义，图片的占位换成识图的描述。
// Images 按占位出现的先后对应（13.2.2）：看完的换成"[图片：描述]"；看完了但没有描述的还是占位；还没识完的，等得不够久就 pending（这一条先别整理），
// 等够了写"[图片（未看清）]"，不再卡住这个会话（边界 ⑧：收到不等于可用）。没有识图信息的占位原样给
func (m *Memory) describe(e Event, now time.Time) (body string, brief, pending bool) {
	waited := now.Sub(time.UnixMilli(e.At)) >= imageWait
	for _, img := range e.Images {
		if !img.Ready && !waited {
			pending = true
		}
	}
	var sb strings.Builder
	idx, last := 0, 0
	for _, loc := range imageTokenRE.FindAllStringSubmatchIndex(e.Text, -1) {
		sb.WriteString(escapeUserText(e.Text[last:loc[0]]))
		last = loc[1]
		label := e.Text[loc[2]:loc[3]]
		i := idx
		idx++
		if i >= len(e.Images) {
			// 没有这张图的识图信息（store 没带 Images，或占位比图多）：占位原样给，模型至少知道这里有张图
			sb.WriteString(e.Text[loc[0]:loc[1]])
			continue
		}
		switch img := e.Images[i]; {
		case img.Ready && strings.TrimSpace(img.Brief) != "":
			sb.WriteString("[" + label + "：" + escapeUserText(strings.Join(strings.Fields(img.Brief), " ")) + "]")
			brief = true
		case img.Ready:
			sb.WriteString("[" + label + "]")
		case waited:
			sb.WriteString("[" + label + "（未看清）]")
		default:
			sb.WriteString("[" + label + "]")
		}
	}
	sb.WriteString(escapeUserText(e.Text[last:]))
	return strings.TrimSpace(sb.String()), brief, pending
}

// ───────────── 4. 已知事实 ─────────────

// audience 是整理批次的可信听众；召回不按它过滤，证据准入仍需要它的来源关联。
// 主人私聊标主人，某人私聊标本人；群里不把主人在场当成当前请求授权。
func (p *plan) audience() Audience {
	a := Audience{Channel: ChannelOf(p.conv), Conversation: p.conv}
	switch p.place {
	case LocalPrivate:
		a.Scene, a.PrivateOK = "private", true
	case LocalGroup:
		a.Scene, a.PrivateOK = "group", true
	case PlatformGroup:
		a.Scene = "group"
	default:
		a.Scene = "private"
		if p.ownerPrivate {
			a.PrivateOK, a.PersonIDs = true, []string{"owner"}
		} else if p.partner != "" {
			a.PersonIDs = []string{p.partner}
		}
	}
	return a
}

// visibleFacts 取当前角色及显式共用池的某个状态，新的在前；有效和候选共用召回资格。
func (m *Memory) visibleFacts(a Audience, status string) []Item {
	items, _ := m.snapshotForRead()
	allowed := AllowedScopes(a)
	var facts []Item
	for _, it := range items {
		if it.Type == "fact" && it.Status == status && CanSee(it.Scopes, allowed) {
			facts = append(facts, it)
		}
	}
	sort.SliceStable(facts, func(i, j int) bool {
		if facts[i].CreatedAt != facts[j].CreatedAt {
			return facts[i].CreatedAt > facts[j].CreatedAt
		}
		return facts[i].Ref > facts[j].Ref
	})
	return facts
}

// candidateFacts 给模型最多 20 条待确认事实，相关优先、最近补足（W3a），跨场合想起不放宽证据身份检查。
// 格式同已知事实；不写"已有几次证据"，免得模型为了凑次数去写。返回文字和编号 → 主体（BySubject 要用）
func (m *Memory) candidateFacts(a Audience, counts counter, related []Hit) (string, map[string]candInfo) {
	facts := m.summaryFacts(a, "candidate", related)
	if len(facts) > maxCandidateBlock {
		counts.addN(CountCandidatesOverLimit, len(facts)-maxCandidateBlock)
		facts = facts[:maxCandidateBlock]
	}
	shown := map[string]candInfo{}
	var lines []string
	for _, it := range facts {
		id := strings.TrimPrefix(it.Ref, "f:")
		shown[id] = candInfo{subject: it.Subject, col: candColumn(it.Subject)}
		lines = append(lines, factLine(id, it))
	}
	if len(lines) == 0 {
		return "（暂无）", shown
	}
	return strings.Join(lines, "\n"), shown
}

// knownFacts 给模型最多 40 条有效事实，相关优先、最近补足（B9），只可撤销或替换这里列出的编号。
// 返回给模型的文字（"编号｜种类｜内容"一行一条）和编号集合
func (m *Memory) knownFacts(a Audience, counts counter, related []Hit) (string, map[string]bool) {
	facts := m.summaryFacts(a, "active", related)
	if len(facts) > maxKnown {
		counts.addN(CountKnownOverLimit, len(facts)-maxKnown)
		facts = facts[:maxKnown]
	}
	shown := map[string]bool{}
	var lines []string
	for _, it := range facts {
		id := strings.TrimPrefix(it.Ref, "f:")
		shown[id] = true
		lines = append(lines, factLine(id, it))
	}
	if len(lines) == 0 {
		return "（暂无）", shown
	}
	return strings.Join(lines, "\n"), shown
}

// factBreaks：事实正文里的换行并成空格，一条事实在提示词里始终是一行
var factBreaks = strings.NewReplacer("\r", " ", "\n", " ")

// factLine 是已知事实在提示词里的一行："编号｜种类｜内容"。正文是任意说话人说过的话，当数据、不当指令：先并成一行，
// 再过 escapeUserText（方括号换全角、"↪"换掉），里面伪造不出一行"[12] 主人(主人)：……"（规格-记忆写入上线 W3a）
func factLine(id string, it Item) string {
	return id + "｜" + it.Kind + "｜" + escapeUserText(factBreaks.Replace(it.Text))
}

// privateSide 认平台私聊是谁的：先看这一批里有没有主人的行（是主人的私聊）、有没有群友的行（是那个人的私聊）；
// 这一批只有她自己的话（主人还没回）就问事件来源这个会话里她最近看到的人话，不能把主人还没回的那一批判成别人的私聊（13.2.6）。
// 都认不出来就按"某人"算（收紧），对方是谁也不知道
func (m *Memory) privateSide(ctx context.Context, es EventSource, conv string, kept []keptEvent) (ownerPrivate bool, partner string) {
	for _, k := range kept {
		switch {
		case k.ev.ActorKind == "owner":
			ownerPrivate = true
		case k.ev.Origin == "peer" && partner == "":
			partner = k.ev.ActorID
		}
	}
	if ownerPrivate {
		return true, ""
	}
	if partner != "" {
		return false, partner
	}
	ev, ok, err := es.LastSaid(ctx, conv)
	switch {
	case err != nil || !ok || ev.Conversation != conv:
		return false, ""
	case ev.ActorKind == "owner":
		return true, ""
	case ev.Origin == "peer":
		return false, ev.ActorID
	}
	return false, ""
}

// ───────────── 5. 聊天记录 ─────────────

func (m *Memory) herName() string {
	if m.opt.Name != "" {
		return m.opt.Name
	}
	return m.opt.Character
}

// ownerCall 是这个角色怎么称呼所有者；没配叫"用户"
func (m *Memory) ownerCall() string {
	if m.opt.OwnerName != "" {
		return m.opt.OwnerName
	}
	return identity.OwnerDefault
}

// roster 给聊天记录里的人起叫法：同一批里两个不同的人同名，后出现的加 #2、#3；
// 所有者固定叫卡上的称呼，她叫她的名字，别人的名字不能占用这两个（群名片和称呼一样的群友会被改名）
type roster struct {
	names map[string]string // 身份 → 叫法
	uses  map[string]int    // 基础叫法 → 有几个不同的人用过
}

func newRoster() *roster { return &roster{names: map[string]string{}, uses: map[string]int{}} }

func (r *roster) call(key, base string) string {
	if n, ok := r.names[key]; ok {
		return n
	}
	r.uses[base]++
	n := base
	if r.uses[base] > 1 {
		n = fmt.Sprintf("%s#%d", base, r.uses[base])
	}
	r.names[key] = n
	return n
}

// transcript 把留下的每一条排成 "[seq] 名字(身份)：正文"，回复别人的接 "↪引用 谁：开头几个字"，并给出"说话的人"对照（subject 照这里写）。
// 身份只由 ActorKind、Origin 定，不由名字定：群名片叫"主人"的群友仍是群友（B12）。
// 平台来的行带时间（Bun 排好的那一行就带）；本地主窗口不带。同时把每一行记进 pl.lines（src 的编号要对得上），说话人记进 pl.people
func (m *Memory) transcript(b batch, pl *plan) (transcript, speakers string) {
	me := m.herName()
	r := newRoster()
	owner := r.call("owner/owner", m.ownerCall())
	self := r.call("character/"+m.opt.Character, escapeName(me))
	pl.lines = map[int64]sayLine{}
	pl.people = map[string]bool{}
	group := pl.place == LocalGroup || pl.place == PlatformGroup
	console := ChannelOf(pl.conv) == "console"
	loc := m.w.loc
	table := []string{"- " + owner + "：owner", "- " + self + "（你）：self"}
	var lines []string
	for _, k := range b.kept {
		e := k.ev
		pl.lines[e.Seq] = sayLine{seq: e.Seq, at: e.At, kind: e.ActorKind, id: e.ActorID, origin: e.Origin, text: e.Text}
		if e.ActorKind == "owner" {
			pl.hasOwner = true
		}
		if e.Origin == "peer" {
			pl.anyPeer = true
		}
		if pl.firstAt == 0 || e.At < pl.firstAt {
			pl.firstAt = e.At
		}
		if pl.firstOrigin == "" || (pl.firstOrigin == "character" && e.Origin != "" && e.Origin != "character") {
			pl.firstOrigin = e.Origin
		}

		var head string
		switch {
		case e.ActorKind == "owner":
			head = owner
		case e.ActorKind == "character" && e.ActorID == m.opt.Character:
			head = self + "（" + identity.Self + "）"
			if e.Origin == "human" || e.Origin == "external" {
				// 她的号上发出、但不是她说的（多半是主人自己用她的号发的，qq.ts:300-304）：不能记成她说的，更不是证据
				head = self + "（" + identity.SelfAccount + "上别人发的）"
			}
		default:
			who, tag := m.otherSpeaker(r, e.ActorKind, e.ActorID, e.ActorName, group)
			head = who + "(" + tag + ")"
			if e.ActorID != "" && !pl.people[e.ActorID] {
				pl.people[e.ActorID] = true
				table = append(table, "- "+who+"："+e.ActorID)
			}
		}
		stamp := ""
		if !console {
			stamp = "[" + lineTime(e.At, loc) + "] "
		}
		quote := ""
		if q := e.Quote; q != nil {
			quote = "↪引用 " + m.quoteName(r, q) + "：" + escapeUserText(firstRunes(strings.Join(strings.Fields(q.Text), " "), quoteChars)) + "\n  "
		}
		lines = append(lines, fmt.Sprintf("[%d] %s%s：%s%s", e.Seq, stamp, head, quote, k.body))
	}
	return strings.Join(lines, "\n"), strings.Join(table, "\n")
}

// otherSpeaker：不是主人、不是她自己的人。名字用发言时的显示名（ActorName），没有就写"某位群友"之类，不退回号码（边界 ㉑）
func (m *Memory) otherSpeaker(r *roster, kind, id, name string, group bool) (who, tag string) {
	tag, anonymous := identity.Friend, "某位"+identity.Friend
	switch {
	case kind == "character":
		tag, anonymous = identity.Character, "某位"+identity.Character
	case group:
		tag, anonymous = identity.GroupPeer, "某位"+identity.GroupPeer
	}
	base := escapeName(name)
	if base == "" {
		base = anonymous
	}
	key := kind + "/" + id
	if id == "" {
		key = "name/" + base
	}
	return r.call(key, base), tag
}

// quoteName：被引用的人叫什么。所有者和聊天记录里一样叫卡上的称呼，她叫"你"，别人和他在聊天记录里的叫法一致
func (m *Memory) quoteName(r *roster, q *Quote) string {
	switch {
	case q.ActorKind == "owner":
		return r.call("owner/owner", m.ownerCall())
	case q.ActorKind == "character" && q.ActorID == m.opt.Character:
		return identity.Self
	}
	who, _ := m.otherSpeaker(r, q.ActorKind, q.ActorID, q.ActorName, true)
	return who
}

// firstRunes 取开头 n 个码点，不加省略号（Bun 的引用就是 slice(0, 20)）
func firstRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// lineTime：消息行首的时间，"10月17日 13:40"（Bun 的 lineTime，本地时间）
func lineTime(ms int64, loc *time.Location) string {
	t := time.UnixMilli(ms).In(loc)
	return fmt.Sprintf("%d月%d日 %02d:%02d", int(t.Month()), t.Day(), t.Hour(), t.Minute())
}

// nowText 是给模型看的当前时间，和 Bun 版 nowText 一样："2026年10月25日星期日 00:05"。memory 不引 turn，抄 turn.NowText 的一份
func nowText(t time.Time) string {
	week := []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}
	return fmt.Sprintf("%d年%d月%d日%s %02d:%02d", t.Year(), int(t.Month()), t.Day(), week[t.Weekday()], t.Hour(), t.Minute())
}

// summaryCacheKey：整理的缓存键，角色 + 会话 + 版本（内容不变就复用，版本换了不复用，边界 ⑲）。哈希过，不往上游带会话 id（里面有号码）
func summaryCacheKey(character, conv string, version int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("memory-summary:%s:%s:%d", character, conv, version)))
	return "sumika-sum-" + hex.EncodeToString(h[:])[:24]
}

// countsText：计数写成 "键=数" 排好序，日志里用。键都是原因码，没有原文
func countsText(c counter) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, c[k])
	}
	return strings.Join(parts, " ")
}
