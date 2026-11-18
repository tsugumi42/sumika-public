package turn

// 重建留痕（规格-提示词前缀稳定第八节，P1-6）：每次重建后的第一步，在 model_runs.context_json 里记一个 rebuild 键：
// 为什么重建、带上几条、窗口外丢了几条、有没有摘要覆盖。只记计数和原因标识，不记正文、名字、号码。不改重建行为。

import (
	"math"
	"slices"
	"strings"
	"time"

	"sumika-native/internal/history"
	"sumika-native/internal/prompts"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

// 重建原因的标识（model_runs.context_json 的 rebuild.reason；显示层翻成中文，规格 8.2）。
// 找不出原因的只能记 RebuildUnknown，不归到别的里凑数
const (
	RebuildStale          = "stale"                // MarkStale：别的线往会话写了话
	RebuildSpeaker        = "speaker"              // 说话人（ActorID、ActorKind）变了
	RebuildScope          = "scope"                // 身份范围的别的字段变了（授权版本等）
	RebuildWorkspace      = "workspace"            // 工作目录变了
	RebuildModel          = "model"                // 换了模型
	RebuildPersona        = "persona"              // 固定前缀里角色设定段变了（改卡，或私下段随听众里有没有外人变）
	RebuildScene          = "scene"                // 场合提示词变了
	RebuildPrompt         = "prompt"               // 别的提示词段变了
	RebuildTools          = "tools"                // 工具清单变了（插件启停、权限）
	RebuildCompacted      = "compacted"            // 有了新的压缩快照
	RebuildIdle           = "idle"                 // 闲置超时
	RebuildEvicted        = "evicted"              // 实例数超限被挤掉
	RebuildRestartIn      = "restart_in"           // RestartIn：新话题、检查点回退、渠道交回要求
	RebuildRestart        = "restart"              // 本进程第一次见这个会话，库里已有旧消息
	RebuildFirst          = "first"                // 本进程第一次见，库里没有旧消息
	RebuildNewAudience    = "new_audience"         // 本进程见过这个会话，没见过这一档
	RebuildOverLimitSnap  = "over_limit_compacted" // 回复前过了硬门槛，压缩成了
	RebuildOverLimitTrim  = "over_limit_trimmed"   // 回复前过了硬门槛，没压成或不压，只留最近的
	RebuildProviderExceed = "provider_overflow"    // 服务商报上下文超长后重建
	RebuildToolPair       = "tool_pair"            // 工具调用和结果对不上
	RebuildWorkRoot       = "work_root"            // 工作记录对不上，退回整个重建
	RebuildUnknown        = "unknown"
)

// rebuildOrder 是同时有几个原因时谁当 reason 的固定优先级（规格 8.2：先排具体改动，闲置排最后，来历不明垫底）
var rebuildOrder = []string{RebuildStale, RebuildSpeaker, RebuildScope, RebuildWorkspace, RebuildModel, RebuildPersona, RebuildScene, RebuildPrompt,
	RebuildTools, RebuildCompacted, RebuildToolPair, RebuildWorkRoot, RebuildProviderExceed, RebuildOverLimitSnap, RebuildOverLimitTrim, RebuildIdle,
	RebuildEvicted, RebuildRestartIn, RebuildRestart, RebuildNewAudience, RebuildFirst, RebuildUnknown}

// RebuildReasons 是全部原因标识（查询、显示层、测试对表用）
func RebuildReasons() []string { return slices.Clone(rebuildOrder) }

// 数条数的结果：exact 数了；failed 这次数出错（Dropped、Covered 记 0，不进平均）
const (
	CountExact  = "exact"
	CountFailed = "failed"
)

// RebuildTrace 是一次重建的留痕（rebuild 键的内容）
type RebuildTrace struct {
	Reason   string   `json:"reason"`
	Also     []string `json:"also,omitempty"`
	Kept     int      `json:"kept"`     // 带上的原文条数（不含快照那一条）
	Dropped  int      `json:"dropped"`  // 算数的消息里 seq 小于带上的第一条的条数
	Covered  int      `json:"covered"`  // 其中被压缩快照覆盖的
	Count    string   `json:"count"`    // CountExact 或 CountFailed
	Snapshot bool     `json:"snapshot"` // 这次重建用上了压缩快照
}

// add 记上这次重建的原因：同时有几个时按 rebuildOrder 排，第一个当 Reason，其余进 Also；已经记过的（上一次重建还没记到
// 请求就又重建）保留在前，新的排在后面
func (t *RebuildTrace) add(reasons []string) {
	if len(reasons) == 0 {
		reasons = []string{RebuildUnknown}
	}
	rank := func(r string) int {
		if i := slices.Index(rebuildOrder, r); i >= 0 {
			return i
		}
		return len(rebuildOrder)
	}
	sorted := slices.Clone(reasons)
	slices.SortStableFunc(sorted, func(a, b string) int { return rank(a) - rank(b) })
	var all []string
	if t.Reason != "" {
		all = append(all, t.Reason)
		all = append(all, t.Also...)
	}
	for _, r := range sorted {
		if !slices.Contains(all, r) {
			all = append(all, r)
		}
	}
	t.Reason, t.Also = all[0], nil
	if len(all) > 1 {
		t.Also = all[1:]
	}
}

// carry 把还没记到请求里的上一次重建的原因带过来（重建后这一轮没发出请求就又重建了：先到的当 Reason，后到的进 Also）
func (t *RebuildTrace) carry(l *Live) {
	if l != nil && l.first && l.b != nil {
		t.Reason, t.Also = l.b.Trace.Reason, slices.Clone(l.b.Trace.Also)
	}
}

// countRebuild 数这次重建的条数（规格 8.1）：hist 是裁完以后带上的原文。
// fromStart 为真时只数最后一个新话题分隔之后的：分隔之前的话是主人主动结束的，不算丢（规格-私聊从上下文起点重建 第七节）。
// 数条数出错不能让重建失败：记 CountFailed，照常重建
func countRebuild(st *store.Store, characterID, conversation string, skip []int64, snapshot bool, epochID string, hist []history.Message, fromStart bool) RebuildTrace {
	t := RebuildTrace{Kept: len(hist), Snapshot: snapshot, Count: CountExact}
	before := int64(math.MaxInt64) // 一条原文都没带上：整个会话里算数的都算丢
	if len(hist) > 0 {
		before = hist[0].Seq
	} else if last := lastOf(skip); last > 0 {
		before = last + 1 // 这一批之后新来的、排着队没轮到的不算丢
	}
	fail := func() RebuildTrace {
		t.Count, t.Dropped, t.Covered = CountFailed, 0, 0
		return t
	}
	floor := int64(0)
	if fromStart {
		brk, err := history.LastBreak(st.Events, conversation)
		if err != nil {
			return fail()
		}
		if brk > 0 {
			floor = brk + 1
		}
	}
	n, err := history.CountBetween(st.Events, conversation, floor, before, skip)
	if err != nil {
		return fail()
	}
	t.Dropped = n
	if epochID != "" {
		from, err := history.EpochFromSeq(st.Events, characterID, conversation, epochID)
		if err != nil {
			return fail()
		}
		c, err := history.CountBetween(st.Events, conversation, floor, from, skip)
		if err != nil {
			return fail()
		}
		t.Covered = min(c, n)
	}
	return t
}

// sectionChanges 比新旧固定前缀的段：哪一类段变了。按段 ID 的前缀分，和 contextFor 拆 persona、scene 用同一套
func sectionChanges(old, cur []prompts.Section) (persona, scene, prompt bool) {
	text := func(l []prompts.Section) map[string]string {
		m := make(map[string]string, len(l))
		for _, s := range l {
			m[s.ID] = s.Text
		}
		return m
	}
	mark := func(id string) {
		switch {
		case strings.HasPrefix(id, "persona"):
			persona = true
		case strings.HasPrefix(id, "scene-") || strings.HasPrefix(id, "platform-"):
			scene = true
		default:
			prompt = true
		}
	}
	o, c := text(old), text(cur)
	for id, t := range c {
		if ot, ok := o[id]; !ok || ot != t {
			mark(id)
		}
	}
	for id := range o {
		if _, ok := c[id]; !ok {
			mark(id)
		}
	}
	return
}

// reuseBlockers 是已有实例不能接着用的原因（acquire 的复用条件逐项拆开；条件本身仍在 acquire 里，这里只负责说出是哪几项）。
// 一项都对不上返回空：调用方记 unknown，不猜
func reuseBlockers(l *Live, in Input, m string, sections []prompts.Section, system string, decls []tools.Declaration, latestEpoch string, stale uint64, now time.Time, idleAfter time.Duration) []string {
	var out []string
	if l.staleGen != stale {
		out = append(out, RebuildStale)
	}
	if a, b := l.in.Identity.Scope, in.Identity.Scope; a != b {
		if a.ActorID != b.ActorID || a.ActorKind != b.ActorKind {
			out = append(out, RebuildSpeaker)
		} else {
			out = append(out, RebuildScope)
		}
	}
	if l.in.Workspace != in.Workspace {
		out = append(out, RebuildWorkspace)
	}
	if l.model != m {
		out = append(out, RebuildModel)
	}
	if l.system != system {
		persona, scene, prompt := sectionChanges(l.b.Sections, sections)
		if persona {
			out = append(out, RebuildPersona)
		}
		if scene {
			out = append(out, RebuildScene)
		}
		if prompt || (!persona && !scene) {
			out = append(out, RebuildPrompt)
		}
	}
	if tools.FixedJSON(l.b.Decls) != tools.FixedJSON(decls) {
		out = append(out, RebuildTools)
	}
	if l.b.EpochSeen != latestEpoch {
		out = append(out, RebuildCompacted)
	}
	if now.Sub(l.lastUsed) >= idleAfter {
		out = append(out, RebuildIdle)
	}
	return out
}

// noteGone 记下这一档的实例为什么被放掉（下次 acquire 取走）。调用方拿着 h.mu
func (h *Hub) noteGone(key, reason string) {
	if h.gone == nil {
		h.gone = map[string]string{}
	}
	h.gone[key] = reason
}

// missingReason 是这一档没有实例时的原因（调用方拿着 h.mu，取完就清）：有放掉的记号用记号；
// 没有记号时：本进程见过这一档却没记号 = unknown；见过这个会话没见过这一档 = new_audience；
// 都没见过 = 程序重启后第一次或这个会话第一次，等读完库里有没有旧消息再分（留空，由 settleFirst 补）
func (h *Hub) missingReason(key, conv string) string {
	reason, ok := h.gone[key]
	delete(h.gone, key)
	switch {
	case ok:
		return reason
	case h.seenKeys[key]:
		return RebuildUnknown
	case h.seenConvs[conv]:
		return RebuildNewAudience
	}
	return ""
}

// markSeen 记这一档、这个会话本进程建过实例。调用方拿着 h.mu
func (h *Hub) markSeen(key, conv string) {
	if h.seenKeys == nil {
		h.seenKeys, h.seenConvs = map[string]bool{}, map[string]bool{}
	}
	h.seenKeys[key], h.seenConvs[conv] = true, true
}

// settleFirst 补上“本进程头一回见”的原因：库里什么都没带上也没丢（没有旧消息）是 first，不然是 restart
func settleFirst(t *RebuildTrace) string {
	switch {
	case t.Count != CountExact:
		return RebuildUnknown // 这次没数出来，分不清有没有旧消息，不猜
	case t.Kept == 0 && t.Dropped == 0 && !t.Snapshot:
		return RebuildFirst
	}
	return RebuildRestart
}
