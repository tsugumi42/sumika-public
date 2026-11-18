package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"sumika-native/internal/audience"
	"sumika-native/internal/pyspace"
)

// 记忆工具的实现函数（规格-记忆-v2 13.2.3）；由 memplugin 注册进内核。
// error 只留给内部错误（库打不开、没配端口、ctx 取消），工具层把它转成出错的文字；没做成但有话可说的（权限、格式、编号、忙、只读）走 ToolResult.IsError。

// ToolResult 是一次工具调用给她看的结果
type ToolResult struct {
	Text    string // 给她看的文字
	IsError bool   // 没做成：标成出错，文字写清楚下一步怎么办（ADR-094）
}

func toolOK(s string) ToolResult   { return ToolResult{Text: s} }
func toolFail(s string) ToolResult { return ToolResult{Text: s, IsError: true} }

const (
	// 一条 remember 的正文最多多少字，按码点（和整理的 300 字对齐；Bun 的 remember 没有上限）
	maxFactChars = 300
	// recall_raw：最多取几个词、最多给几条、每条原话最多给多少字（500 是起步值，Bun 没有；一条长消息配 10 条命中会把上下文撑爆）
	rawMaxWords = 4
	rawMaxHits  = 10
	rawMaxChars = 500
)

// now 是 Memory 的时钟：Options.Now，没给用 time.Now
func (m *Memory) now() time.Time {
	if m.opt.Now != nil {
		return m.opt.Now()
	}
	return time.Now()
}

// toolAgo 是工具里"多久以前"的写法：分钟、小时、天（注入用的是"今天 / 昨天 / N 天前"，见 render.go 的 ago）。
// 照 daemon/tools/memory-tools.ts 的 ago，四舍五入照 JS 的 Math.round（.5 往大里进）；时间在未来（两台机器的钟对不上）当 0 分钟
func toolAgo(ms int64, now time.Time) string {
	round := func(x float64) int { return int(math.Floor(x + 0.5)) }
	minutes := round(float64(now.UnixMilli()-ms) / 60_000)
	switch {
	case minutes < 60:
		return fmt.Sprintf("%d 分钟前", max(minutes, 0))
	case minutes < 60*24:
		return fmt.Sprintf("%d 小时前", round(float64(minutes)/60))
	}
	return fmt.Sprintf("%d 天前", round(float64(minutes)/1440))
}

// ───────────── remember ─────────────

// RememberArgs 是 remember 的参数（工具层从模型给的 JSON 解出来，这里再校验内容）
type RememberArgs struct {
	InputSeqs   []int64 // 宿主绑定的本次请求输入，不是模型可填写的参数。
	Kind        string  // fact / promise / preference / boundary，其余报错
	Text        string  // 第三人称一句话；空或超过 300 字报错（字 = Unicode 码点）
	Subject     string  // 空当 owner；只认 owner、self
	Replaces    string  // 空，或 6 位短码（memory_search 给的 #XXXXXX，# 可有可无，大小写不拘）
	Temporal    string
	ValidUntil  string
	SourceQuote string // 必须是可信主人原话的连续片段，不作为确认授权。
	OnlyHer     *bool  // 明确不是专属才可能共享；缺失留在角色库。
	// Whisper 是宿主按悄悄话服务里这一条的来历给的来源（批号、来源会话、被引事件）；不是模型可填写的参数，不在悄悄话那一轮就是 nil。
	Whisper *WhisperSource
}

// rememberReadOnly 是写入没打开时 remember 给她看的话（规格-记忆写入上线 W4）。
// 要点：说清这句话没存下来、要如实告诉对方；不说"过一会儿再说"（只读期说多少次都存不下来）；
// 不出现"记住了"这类成功的字样（失败的话里带着它，会被当成成功的回答）；不带库路径、配置键名
const rememberReadOnly = "现在记忆是只读的，这句话没有存下来。如实告诉对方这次没记上，别说成已经存好了；等记忆写入打开以后，再请对方说一次。"

// replaceNotFound 是 B19 的原文。code 是她填的原样
func replaceNotFound(code string) string {
	return fmt.Sprintf("没找到编号 %s 的记忆（可能已经被改过、撤销了，或者编号抄错了）。这次没有记：先用 memory_search 查到现在的编号，再记一次。", code)
}

// validCode：6 位，只有数字和大写字母（短码就是 ULID 编号的末 6 位）
func validCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for i := 0; i < len(code); i++ {
		if c := code[i]; !(c >= '0' && c <= '9') && !(c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// Remember 保存有可信主人原话的模型提炼（ADR-314），不代表主人确认。
// 两种主人私聊可用；无来源、冒充、撤回、引用不匹配均拒写。
// 新事实 confirmed_by=owner_said（来源绑定了主人原话，但不是主人放行）：整理动不了，remember 自己能改口；同文不升级 owner，替换服从主体和保护规则。
func (m *Memory) Remember(ctx context.Context, a Audience, in RememberArgs) (ToolResult, error) {
	if place := PlaceOf(a.Channel, a.Conversation, a.Scene); !a.PrivateOK || (place != LocalPrivate && place != PlatformPrivate) {
		return toolFail("这句话只能在所有者的私聊里记。"), nil
	}
	w, err := m.writer()
	if errors.Is(err, ErrNotWritable) {
		return toolFail(rememberReadOnly), nil
	}
	if err != nil {
		return ToolResult{}, err
	}
	events, err := m.events()
	if err != nil {
		return ToolResult{}, err
	}

	if !slices.Contains(factKinds, in.Kind) {
		return toolFail("kind 只能是 fact、promise、preference、boundary 之一，这次没有记。"), nil
	}
	// 判空和嵌入服务是同一套空白（pyspace）：只用 TrimSpace 的话 "" 这类字符算内容，写得进库、到嵌入那边却是空白，整批补不出向量
	text := pyspace.Trim(in.Text)
	switch n := utf8.RuneCountInString(text); {
	case n == 0:
		return toolFail("要记的内容是空的，这次没有记：用第三人称一句话写清楚。"), nil
	case n > maxFactChars:
		return toolFail(fmt.Sprintf("这句话太长了（超过 %d 字），这次没有记：拆成几条，每条用第三人称一句话。", maxFactChars)), nil
	}
	subject := strings.TrimSpace(in.Subject)
	switch subject {
	case "":
		subject = "owner"
	case "owner", "self":
	default:
		return toolFail("subject 只能写 owner（所有者）或 self（你自己），这次没有记。"), nil
	}
	code := ""
	if raw := strings.TrimSpace(in.Replaces); raw != "" {
		if code = strings.ToUpper(strings.TrimPrefix(raw, "#")); !validCode(code) {
			return toolFail(replaceNotFound(raw)), nil
		}
	}

	if in.Temporal != "" && in.Temporal != "stable" && in.Temporal != "temporary" {
		return toolFail("temporal 只能是 stable 或 temporary，这次没有记。"), nil
	}
	temporal, until, err := memoryLifetime(in.Kind, in.Temporal, in.ValidUntil, m.now())
	if err != nil {
		return toolFail("记忆期限不合法：使用 stable，或 temporary 加未来的带时区 valid_until；这次没有记。"), nil
	}
	src := m.boundSource(ctx, events, a.Conversation, in.InputSeqs, in.SourceQuote)
	if src == nil {
		return toolFail("没有找到对应的所有者原话，这次没有记：source_quote 必须完整引用本轮已接纳的所有者消息，不能有歧义，不要编造来源。"), nil
	}
	// 悄悄话那一轮：认不出是哪一批悄悄话（来源没给全）就不写，不能写下一条没有来历的记忆
	if audience.IsWhisper(a.Conversation) && !in.Whisper.Valid() {
		return toolFail("这次没有记：认不出这条悄悄话的来历。"), nil
	}
	op := FactOp{Kind: in.Kind, Text: text, Subject: subject, Status: "active", ConfirmedBy: ConfirmedOwnerSaid, Temporal: temporal, ValidTo: until,
		Scopes: []string{"owner"}, SupersedesCode: code, Conversation: a.Conversation}
	op.SourceSeqs = []int64{src.Seq}
	op.Origin, op.EventAt = src.Origin, src.At
	op.OwnerSaid = true // 只用于主人自身事实共享，不授予确认权限。
	// 只在悄悄话会话里认宿主给的来源
	if in.Whisper.Valid() && audience.IsWhisper(a.Conversation) {
		op.Whisper = in.Whisper
	}
	op.Share = shareableFact(in.Kind, subject, in.OnlyHer, true, src.Text) && !audience.IsWhisper(a.Conversation) // 悄悄话处理里记的不进共用池：文字是模型凭被引的别人的话提炼的
	op.AudienceAtOrigin = "owner"

	w, op, err = m.rememberTarget(ctx, op)
	var res Applied
	if err == nil {
		res, err = w.apply(ctx, Batch{By: ByRememberAI, OnDup: DupReport, Ops: []Op{op}, Wait: m.rememberWait})
	}
	switch {
	case errors.Is(err, ErrBusy):
		return toolFail("记忆库正忙，这次没记上，过一会儿再说一次。"), nil
	case errors.Is(err, ErrRememberExpired):
		return toolFail("相同的记忆仍已过期，当前原话不能更新它；没有恢复，也没有替换其他记忆。"), nil
	case errors.Is(err, ErrRememberRevoked):
		return toolFail("这条共用记忆曾被撤销或拒绝，不能通过改成专属重新保存。"), nil
	case errors.Is(err, ErrReplaceProtected):
		return toolFail("这条记忆受所有者确认或历史导入保护，模型不能自行替换，这次没有修改。"), nil
	case errors.Is(err, ErrReplaceGone):
		return toolFail(replaceNotFound(strings.TrimSpace(in.Replaces))), nil
	case errors.Is(err, ErrReplaceAmbiguous):
		return toolFail(fmt.Sprintf("编号 %s 对上了不止一条记忆，这次没有记：先用 memory_search 看清是哪一条，再记一次。", strings.TrimSpace(in.Replaces))), nil
	case err != nil:
		return ToolResult{}, err
	}
	o := res.Outcomes[0]
	switch {
	case o.Result == ResultRefused:
		return toolFail("这条记忆曾被撤销或拒绝，模型不能自行恢复，这次没有记。"), nil
	case o.Replaced != nil:
		return toolOK(fmt.Sprintf("记住了，原来那条（\"%s\"）不再用了。", o.Replaced.Text)), nil
	case o.Promoted:
		return toolOK("记住了。"), nil
	case o.Result == ResultExists:
		return toolOK("这件事已经记着了。"), nil
	}
	return toolOK("记住了。"), nil
}

// ───────────── memory_forget ─────────────

// ForgetArgs 是 memory_forget 的参数
type ForgetArgs struct {
	InputSeqs   []int64 // 宿主绑定的本次请求输入，不是模型可填写的参数。
	ID          string  // memory_search 给的 #XXXXXX（# 可有可无，大小写不拘）
	Reason      string  // 为什么要忘掉，一句话；记进撤销的修订
	SourceQuote string  // 必须是可信主人原话的连续片段，和 remember 一样
}

const (
	// forgetMaxReason：原因最多多少字（码点）
	forgetMaxReason = 100
	forgetReadOnly  = "现在记忆是只读的，这条没有忘掉。如实告诉对方这次没忘成，别说成已经忘掉了；等记忆写入打开以后，再请对方说一次。"
)

func forgetNotFound(code string) string {
	return fmt.Sprintf("没找到编号 %s 的有效记忆（可能已经忘掉了、被改过，或者编号抄错了）。这次没有忘：先用 memory_search 查到现在的编号。", code)
}

// Forget 把一条有效的记忆标成"忘掉"（软删除：status = revoked，不再被想起、也不再被整理或 remember 复活，库里留着这条和修订，不物理删）。
// 门槛和 Remember 完全一样，只会更窄，不会更宽：
//   - 只有主人的私聊（本地私聊、QQ 私聊）；本地群聊、QQ 群、陌生人私聊一律不行；
//   - 要有宿主绑定的主人原话作来源（source_quote），不凭模型自己的意思；
//   - 用的是同一个写入身份（ByRememberAI）：confirmed_by 是 ai、owner_said 的动得了，owner（主人放行过）、历史导入、不认识的值受保护；
//   - 只动关于主人（owner）或她自己（self）的事实，别人的事实动不了；只动有效的事实：片段、待确认的、已经忘掉的、被取代的、编号不存在的都不行。
//
// 编号先在自己的库里找；找不到再找主人共用池（共用的主人事实 remember 本来就能取代）。
func (m *Memory) Forget(ctx context.Context, a Audience, in ForgetArgs) (ToolResult, error) {
	if place := PlaceOf(a.Channel, a.Conversation, a.Scene); !a.PrivateOK || (place != LocalPrivate && place != PlatformPrivate) {
		return toolFail("忘掉记忆只能在所有者的私聊里做。"), nil
	}
	w, err := m.writer()
	if errors.Is(err, ErrNotWritable) {
		return toolFail(forgetReadOnly), nil
	}
	if err != nil {
		return ToolResult{}, err
	}
	events, err := m.events()
	if err != nil {
		return ToolResult{}, err
	}
	raw := strings.TrimSpace(in.ID)
	if raw == "" {
		return toolFail("没写要忘掉哪一条，这次没有忘：先用 memory_search 查到那条的编号（# 开头的 6 位），填在 id 里。"), nil
	}
	code := strings.ToUpper(strings.TrimPrefix(raw, "#"))
	if !validCode(code) {
		return toolFail(forgetNotFound(raw)), nil
	}
	reason := pyspace.Trim(in.Reason)
	switch n := utf8.RuneCountInString(reason); {
	case n == 0:
		return toolFail("没写原因，这次没有忘：用一句话写清楚为什么要忘掉它。"), nil
	case n > forgetMaxReason:
		return toolFail(fmt.Sprintf("原因太长了（超过 %d 字），这次没有忘：一句话说清楚就行。", forgetMaxReason)), nil
	}
	if m.boundSource(ctx, events, a.Conversation, in.InputSeqs, in.SourceQuote) == nil {
		return toolFail("没有找到对应的所有者原话，这次没有忘：source_quote 必须完整引用本轮已接纳的所有者消息，不能有歧义，不要编造来源。"), nil
	}
	batch := Batch{By: ByRememberAI, OnDup: DupReport, Ops: []Op{RevokeOp{Code: code, Reason: CleanLine(reason, forgetMaxReason)}}, Wait: m.rememberWait}
	res, err := w.apply(ctx, batch)
	if errors.Is(err, ErrReplaceGone) && m.opt.Shared != nil {
		res, err = m.opt.Shared.apply(ctx, batch)
	}
	switch {
	case errors.Is(err, ErrNotWritable):
		return toolFail(forgetReadOnly), nil
	case errors.Is(err, ErrBusy):
		return toolFail("记忆库正忙，这次没忘掉，过一会儿再说一次。"), nil
	case errors.Is(err, ErrReplaceGone):
		return toolFail(forgetNotFound(raw)), nil
	case errors.Is(err, ErrReplaceAmbiguous):
		return toolFail(fmt.Sprintf("编号 %s 对上了不止一条记忆，这次没有忘：先用 memory_search 看清是哪一条。", raw)), nil
	case err != nil:
		return ToolResult{}, err
	}
	switch o := res.Outcomes[0]; {
	case o.Result == ResultRevoked:
		return toolOK(fmt.Sprintf("已经忘掉 #%s 这条记忆：以后不会再想起它，库里没有真删，留着记录。", code)), nil
	case res.Counts[CountRevokeProtected] > 0:
		return toolFail("这条记忆受所有者确认或历史导入保护，模型不能自行忘掉，这次没有忘。"), nil
	case res.Counts[CountRevokeWrongSubject] > 0:
		return toolFail("只能忘掉关于所有者或你自己的记忆，这条不是，这次没有忘。"), nil
	}
	return toolFail(forgetNotFound(raw)), nil
}

// boundSource 只读取宿主绑定的输入。新消息落库但尚未进入模型请求，不会被猜成来源。
func (m *Memory) boundSource(ctx context.Context, events EventSource, conv string, seqs []int64, quote string) *Event {
	quote = strings.TrimSpace(quote)
	if quote == "" || len(seqs) == 0 {
		return nil
	}
	seen := map[int64]bool{}
	var found *Event
	for _, seq := range seqs {
		if seq <= 0 {
			return nil
		}
		if seen[seq] {
			continue
		}
		seen[seq] = true
		rows, err := events.Events(ctx, conv, seq-1, 1)
		if err != nil {
			m.w.logf("记忆[%s]：绑定原话读取失败（原因 events_error）", m.opt.Character)
			return nil
		}
		if len(rows) != 1 {
			continue
		}
		ev := rows[0]
		if audience.IsWhisper(conv) && strings.TrimSpace(ev.Text) == audience.WhisperNoSay {
			continue // 主人只引用、没写字：占位不是主人说的话，被引的是别人的话
		}
		if ev.Seq != seq || ev.Conversation != conv || ev.ActorKind != "owner" || ev.Origin != "owner" || !ev.Seen || ev.Revoked || ev.Unsent || ev.Kind != "message" || ev.Backfill || (ev.Selftest && !audience.IsWhisper(conv)) || !strings.Contains(ev.Text, quote) {
			continue
		}
		if found != nil && found.Text != ev.Text {
			return nil
		}
		if found == nil || ev.Seq > found.Seq {
			copy := ev
			found = &copy
		}
	}
	return found
}

// Search 明确查当前角色全部记忆及主人共用事实，不受注入预算和已给过的限制。
// QQ 群主人问到的私事仍带披露提醒；每条来源经原始事件核验。
func (m *Memory) Search(ctx context.Context, a Audience, query string) (ToolResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return toolFail("要找什么，用一句话写。"), nil
	}
	now := m.now()
	r := m.Recall(ctx, a, Query{Text: query, Mode: ModeExplicit, TopK: 12, Now: now})
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	if len(r.Hits) == 0 {
		return toolOK("没有找到相关的记忆。可以换个说法、用更短的关键词再查一次；要确认对方当时的原话，用 recall_raw 找聊天记录。"), nil
	}
	lines := make([]string, 0, len(r.Hits))
	for _, h := range r.Hits {
		prefix := "（片段）"
		if h.Type == "fact" {
			id := strings.TrimPrefix(h.Ref, "f:")
			if len(id) > 6 {
				id = id[len(id)-6:]
			}
			prefix = "#" + id + " "
		}
		text := escapeUserText(h.Text)
		if h.Hearsay {
			text = "听说：" + text // 待确认的照每轮注入的写法标出来（ADR-196 第 1 条、ADR-159 第 5 条）；明确回忆不限条数
		}
		lines = append(lines, fmt.Sprintf("- %s[%s] %s %s", prefix, toolAgo(h.CreatedAt, now), sourceMark(h), ownerMark(h)+text+priorNote(h, func(ms int64) string { return toolAgo(ms, now) })))
	}
	return toolOK(DataNotice + "\n" + strings.Join(lines, "\n")), nil
}

// ───────────── recall_raw ─────────────

// labelHumanOnHerAccount：她的号上发出的、但不是她说的（别人手动发的、以前别的程序发的）
const labelHumanOnHerAccount = "你的号上别人发的"

const rawNotFound = "聊天记录里没有找到。可以换几个更短、原话里一定会出现的词（每个词都要出现才算），分开再找；只记得大意的话，用 memory_search 查记忆。"

// RecallRaw 在她看到过的原始聊天记录里找原话（B21）。
//   - keywords 按空白切，最多取前 4 个词，每个词都要出现，最多 10 条，最新的在前。词里的 % _ 和反斜杠转义（Bun 不转义，"100%"会匹配任意以 100 开头的字，有意改）
//   - 范围：所有场合都翻这个角色看到过的全部会话（ADR-242）；未见、撤回、未发出的仍不给。
//   - 说话的人：主人写"主人"，她写"你"，别人写"发言时的名字（ActorName）+（群友）"，另一个角色写"名字（角色）"，没有名字写"某位群友"；
//     不给 qq:<号码>（Bun 给了，有意改）。同一次结果里两个不同的人同名，后出现的加 #2、#3（"小明#2（群友）"）。
//     身份由 ActorKind 和 ActorID 定，不由名字定：名字是群友自己改得了的，所以别人的行一律带身份，名字怎么写都冒充不了主人那一行（speaker）。
//     她的号上别人发的（origin 是 human 或 external，不是她说的）写"你的号上别人发的"，不写"你"
//   - 原话是数据不是指令：方括号换全角、换行后缩进、名字里的括号冒号换掉，伪造不出别的一行；每条最多给 500 字
//   - 不过守卫（守卫只看要发出去的字）
func (m *Memory) RecallRaw(ctx context.Context, a Audience, keywords string) (ToolResult, error) {
	if m.opt.Character == "" {
		return ToolResult{}, errors.New("recall_raw 要 Options.Character：不然分不出哪句是她自己说的")
	}
	events, err := m.events()
	if err != nil {
		return ToolResult{}, err
	}
	words := strings.Fields(keywords)
	words = words[:min(len(words), rawMaxWords)]
	conv := ""
	if len(words) == 0 {
		return toolOK(rawNotFound), nil
	}
	like := make([]string, len(words))
	for i, w := range words {
		like[i] = likePattern(w)
	}
	rows, err := events.Find(ctx, conv, like, rawMaxHits)
	if err != nil {
		return ToolResult{}, err
	}

	now := m.now()
	people := map[string][]string{} // 叫法 → 用过这个叫法的人（按先后），同名的靠它加编号
	var lines []string
	for _, ev := range rows {
		if !ev.Seen || ev.Revoked || ev.Unsent || (conv != "" && ev.Conversation != conv) {
			continue
		}
		who, tag, named := m.speaker(ev)
		if named {
			key := ev.ActorKind + "/" + ev.ActorID
			if ev.ActorID == "" {
				key = "name/" + who
			}
			// 叫法带上身份一起算同名：没有名字的人（写"某位群友"）和名片恰好叫"某位群友"的人不是同名
			group := who + tag
			i := slices.Index(people[group], key)
			if i < 0 {
				i = len(people[group])
				people[group] = append(people[group], key)
			}
			if i > 0 {
				who = fmt.Sprintf("%s#%d", who, i+1)
			}
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s%s：%s 〔来源：%s；会话=%s〕", toolAgo(ev.At, now), who, tag, escapeUserText(clip(ev.Text, rawMaxChars)), sourcePlace(ev.Conversation), escapeUserText(CleanLine(ev.Conversation, 200))))
		if len(lines) == rawMaxHits {
			break
		}
	}
	if len(lines) == 0 {
		return toolOK(rawNotFound), nil
	}
	return toolOK(DataNotice + "\n" + strings.Join(lines, "\n")), nil
}

// 别人那一行的身份，跟在名字后面
const (
	tagMember    = "（群友）"
	tagCharacter = "（角色）"
)

// speaker 是这一句的说话人怎么叫：who 是叫法，tag 是跟在后面的身份。named 为真表示 who 是发言时的名字（同名的要加编号），
// 为假是"主人""你"这类固定叫法。
//
// 固定叫法只有系统写得出来：别人的行一律是"名字 + 身份"，不管名字写成什么（带零宽字符、标点、括号，或者就叫"主人"），
// 说话人那一段都不会和主人、她自己那一行一样。不拿名字去比"像不像主人"：名字是群友自己改得了的，
// 零宽字符、全角半角、标点能变出无数种写法，挡不完。身份只由 ActorKind、ActorID 定（13.2.4 第 5 步）。
// 没有名字的写"某位群友"或"某位角色"，这个叫法自己就带着身份，不再重复加。
// ActorKind 没填的按群友算：不当主人，也不当她自己
func (m *Memory) speaker(ev Event) (who, tag string, named bool) {
	switch {
	case ev.ActorKind == "owner":
		return "所有者", "", false
	case ev.ActorKind == "character" && ev.ActorID == m.opt.Character:
		if ev.Origin == "human" || ev.Origin == "external" {
			return labelHumanOnHerAccount, "", false
		}
		return "你", "", false
	}
	tag, anonymous := tagMember, "某位群友"
	if ev.ActorKind == "character" {
		tag, anonymous = tagCharacter, "某位角色"
	}
	if name := escapeName(ev.ActorName); name != "" {
		return name, tag, true
	}
	return anonymous, "", true
}

// clip 按码点截到 n 个字，截了就加省略号
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
