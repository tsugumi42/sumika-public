package turn

// 工具历史跨实例恢复（规格-工具历史恢复和压缩）：工作记录按 root 存，每个新输入一个新 root；重建实例时按 owner 把旧 root 的
// 调用和结果接回聊天历史，只在本地私聊开。全程只读，不执行工具，不补写旧 root。

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"sumika-native/internal/audience"
	"sumika-native/internal/history"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/store"
)

const (
	workHistoryMaxRoots  = 2000 // 重建时最多回看多少个 root
	workRecordExtraRoots = 50   // 历史窗口之外再回看多少个 root，只为累计工作记录
	restoredStubTokens   = 4000 // 换成占位的那部分最多占多少
	restoredKeepChars    = 200  // 不到这么长的结果和参数不换（结果未知那句也在这里面）
	maxOverflowRecovery  = 6    // 本地私聊一轮最多恢复几次溢出
	overflowKeepFloor    = 1000 // 折叠本轮时最少保留多少 token 的最近调用组
)

const workUnknownMarker = "legacy_unavailable"

// workUnknownText 是缺结局的调用给模型看的那句，旧 root、当前 root 同一句，也是"不重放"的提醒
const workUnknownText = "结果未知或证据未保存（" + workUnknownMarker + "）；不要重新执行副作用来补结果。"

// 工作相关的工具：压缩选场景时只数它们，查记忆、联网不算在干活
var workTools = map[string]bool{"read": true, "ls": true, "grep": true, "find": true, "write": true, "edit": true, "powershell": true, "process_output": true, "process_stop": true, "todo_write": true}

// workHistoryEnabled 是这条机制的唯一门槛：主人本人、主窗口、前台运行的本地私聊。只认宿主给的身份，不看名字和正文。
// 本地群聊、QQ 私聊、QQ 群、话题线、子任务一律不走（ADR-305、ADR-269）
func workHistoryEnabled(cfg Config, in Input) bool {
	inv := in.Identity
	s := inv.Scope
	return workEnabled(cfg, inv) && inv.Kind == kernel.KindForeground && ownerConsoleAudience(in.Audience) && in.Scope == ConsoleScope &&
		s.ScopeID == ConsoleScope && s.ActorKind == "owner" && s.Channel == "console" && s.Platform == "local" && s.Scene == "private" &&
		s.Conversation == in.Conversation && !audience.IsThread(in.Conversation)
}

// projectWork 把一个 root 的消息投影成合法序列：结果按宿主 ToolID 归入原调用组，缺结局的调用补一条"结果未知"，不重放执行。
// missing 在补出来时回调（当前 root 写回库、记进状态）；旧 root 传 nil，只读，补出来的字一样，重建多少次都逐字节相同
func projectWork(msgs []Msg, scopeID string, missing func(step Msg, c Call, result Msg) error) ([]Msg, error) {
	results := map[string]Msg{}
	for _, m := range msgs {
		if m.Role == "toolResult" {
			if _, seen := results[m.ToolID]; !seen {
				results[m.ToolID] = m
			}
		}
	}
	projected := make([]Msg, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "toolResult" {
			continue
		}
		if m.Stop != "" {
			m.Calls = nil
		}
		projected = append(projected, m)
		for i, c := range m.Calls {
			result, ok := results[c.ToolID]
			if !ok {
				result = Msg{Role: "toolResult", CallID: c.ID, ToolID: c.ToolID, WorkStepID: m.WorkStepID, ToolName: c.Name, Kind: KindTool, Scope: scopeID, IsError: true, GroupEnd: i == len(m.Calls)-1, Text: workUnknownText}
				if missing != nil {
					if err := missing(m, c, result); err != nil {
						return nil, err
					}
				}
				results[c.ToolID] = result
			}
			projected = append(projected, result)
		}
	}
	return projected, nil
}

// restoredGroup 是一步工具调用：assistant 那条和它的结果，不拆开
type restoredGroup struct {
	call    Msg
	results []Msg
	dropped bool
}

func (g *restoredGroup) tokens() int {
	n := sumikaMessages(g.call.sumikaText())
	for _, r := range g.results {
		n += sumikaMessages(r.Text)
	}
	return n
}

// stubbed 是这一组的占位版：长结果换引用，长参数省略；路径和 purpose 这种短的留着，还看得出做过什么
func (g *restoredGroup) stubbed() *restoredGroup {
	out := &restoredGroup{call: stubCalls(g.call), results: append([]Msg(nil), g.results...)}
	for i := range out.results {
		stubResult(&out.results[i])
	}
	return out
}

func stubResult(m *Msg) {
	m.Images = nil
	chars := store.JSLen(m.Text)
	if chars < restoredKeepChars || m.Controlled {
		return
	}
	if m.Ref != nil {
		m.Text = fmt.Sprintf("（较早的工具结果已省略；用 result_read 读取 id=%s，完整性=%t）", m.Ref.ID, m.Ref.Complete)
		return
	}
	m.Text = fmt.Sprintf("（较早的工具结果，约 %d 字，已省略；要用就再读一次文件或查一下现状，不要重复做有副作用的操作）", chars)
}

func stubCalls(m Msg) Msg {
	m.Calls = append([]Call(nil), m.Calls...)
	for i := range m.Calls {
		m.Calls[i].Args = abbreviateArgs(m.Calls[i].Args)
	}
	return m
}

func abbreviateArgs(v jsjson.Value) jsjson.Value {
	switch v.Kind {
	case jsjson.String:
		if n := store.JSLen(v.Str); n >= restoredKeepChars {
			v.Str = fmt.Sprintf("（约 %d 字，已省略）", n)
		}
	case jsjson.Array:
		arr := make([]jsjson.Value, len(v.Arr))
		for i, x := range v.Arr {
			arr[i] = abbreviateArgs(x)
		}
		v.Arr = arr
	case jsjson.Object:
		obj := make(map[string]jsjson.Value, len(v.Obj))
		for k, x := range v.Obj {
			obj[k] = abbreviateArgs(x)
		}
		v.Obj = obj
	}
	return v
}

// weavePlan 记每一组挂在哪条事件上：merge 是回复既有字又有调用（调用合到历史那条上），after 排在那条事件后面，
// before 排在前面（这个 root 开头没有事件的那几组，/goal 那种不存事件的输入才有）
type weavePlan struct {
	merge         map[int64]*restoredGroup
	after, before map[int64][]*restoredGroup
}

func (p *weavePlan) add(projected []Msg) {
	var anchor int64
	var leading []*restoredGroup
	place := func(g *restoredGroup) {
		if anchor == 0 {
			leading = append(leading, g)
			return
		}
		if g.call.Seq == anchor && p.merge[anchor] == nil {
			p.merge[anchor] = g
			return
		}
		p.after[anchor] = append(p.after[anchor], g)
	}
	settle := func() {
		if anchor != 0 && len(leading) > 0 {
			p.before[anchor] = append(p.before[anchor], leading...)
			leading = nil
		}
	}
	for i := 0; i < len(projected); i++ {
		m := projected[i]
		if m.Seq != 0 {
			anchor = m.Seq
			settle()
		}
		if m.Role != "assistant" || len(m.Calls) == 0 {
			continue
		}
		g := &restoredGroup{call: m}
		for i+1 < len(projected) && projected[i+1].Role == "toolResult" {
			i++
			g.results = append(g.results, projected[i])
		}
		place(g)
	}
}

// groupOrder 按最终放进实例的先后列出所有放得进去的组（挂靠点不在窗口里的不算）
func (p *weavePlan) groupOrder(msgs []Msg) []*restoredGroup {
	var out []*restoredGroup
	for _, m := range msgs {
		if m.Seq <= 0 {
			continue
		}
		out = append(out, p.before[m.Seq]...)
		if g := p.merge[m.Seq]; g != nil {
			if m.Role == "assistant" {
				out = append(out, g)
			} else {
				p.after[m.Seq] = append([]*restoredGroup{g}, p.after[m.Seq]...)
				delete(p.merge, m.Seq)
			}
		}
		out = append(out, p.after[m.Seq]...)
	}
	return out
}

func (p *weavePlan) render(msgs []Msg) []Msg {
	out := make([]Msg, 0, len(msgs)*2)
	emit := func(groups []*restoredGroup) {
		for _, g := range groups {
			if !g.dropped {
				out = append(out, g.call)
				out = append(out, g.results...)
			}
		}
	}
	for _, m := range msgs {
		if m.Seq <= 0 {
			out = append(out, m)
			continue
		}
		emit(p.before[m.Seq])
		g := p.merge[m.Seq]
		if g != nil && !g.dropped {
			m.Calls, m.WorkStepID = g.call.Calls, g.call.WorkStepID
		}
		out = append(out, m)
		if g != nil && !g.dropped {
			out = append(out, g.results...)
		}
		emit(p.after[m.Seq])
	}
	return out
}

// applyBudget 从最新往回定每一组放全文、放占位还是不放（规格第 2.1 节）。全部由内容决定，同样的数据重建多少次都一样；
// 一组降级了，更早的不再升回去。最新一组总是全文（K2：最少保完整最新组）
func applyBudget(groups []*restoredGroup, full, stub int) (dropped bool) {
	level, usedFull, usedStub := 0, 0, 0
	for i := len(groups) - 1; i >= 0; i-- {
		g := groups[i]
		if level == 0 {
			cost := g.tokens()
			if i == len(groups)-1 || usedFull+cost <= full {
				usedFull += cost
				continue
			}
			level = 1
		}
		if level == 1 {
			s := g.stubbed()
			if cost := s.tokens(); usedStub+cost <= stub {
				*g = *s
				usedStub += cost
				continue
			}
			level = 2
		}
		g.dropped, dropped = true, true
	}
	return dropped
}

// weaveWorkHistory 把这个 owner 旧 root 的工具调用和结果接回 l.msgs（只有说过的话的那份历史）。调用组挂在它前面最近一条
// 说过的话后面；挂靠点不在窗口里（压缩切点之前、裁掉的）就不放，那部分摘要已经覆盖。这里出错只少恢复，不挡这一轮
func (h *Hub) weaveWorkHistory(ctx context.Context, l *Live, in Input) {
	if !workHistoryEnabled(h.Cfg, in) {
		return
	}
	p, ok := h.Cfg.Work.(*workPort)
	if !ok {
		return
	}
	var low int64
	for _, m := range l.msgs {
		if m.Seq > 0 && (low == 0 || m.Seq < low) {
			low = m.Seq
		}
	}
	if low == 0 {
		return
	}
	// 有快照时窗口之前都在摘要里；没有快照时窗口之前还有没进摘要的（recentMessages 裁掉的），累计记录再往回看一点，到"新话题"为止
	floor, extra := low, 0
	if l.b.EpochID == "" {
		st, err := h.storeOf(in.Conversation)
		if err == nil {
			var brk int64
			if brk, err = history.LastBreak(st.Events, in.Conversation); err == nil {
				floor, extra = brk, workRecordExtraRoots
			}
		}
		if err != nil {
			h.plugins.logf("会话 %s 查新话题分隔出错，累计工作记录只算窗口内：%v", in.Conversation, err)
		}
	}
	roots, err := p.service.DB.WorkRootsOf(ctx, in.Identity.Scope, workHistoryMaxRoots)
	if err != nil {
		h.plugins.logf("会话 %s 读工作记录出错，这次只带说过的话：%v", in.Conversation, err)
		return
	}
	var scanned [][]Msg
	for _, root := range roots {
		if root.RootID == in.Identity.RootID {
			continue
		}
		projected, err := p.projectRoot(ctx, in.Identity.Scope, root)
		if err != nil {
			h.plugins.logf("会话 %s 工作记录 %s 读不出来，跳过：%v", in.Conversation, root.RootID, err)
			continue
		}
		last := lastSeq(projected)
		if last != 0 && last < floor {
			break
		}
		if last != 0 && last < low {
			if extra == 0 {
				break
			}
			extra--
		}
		scanned = append(scanned, projected)
	}
	plan := &weavePlan{merge: map[int64]*restoredGroup{}, after: map[int64][]*restoredGroup{}, before: map[int64][]*restoredGroup{}}
	var record workRecord
	hiddenOps := false
	for i := len(scanned) - 1; i >= 0; i-- { // 倒回时间正序
		projected := scanned[i]
		plan.add(projected)
		record.noteAll(projected)
		if last := lastSeq(projected); last != 0 && last < low {
			var part workRecord
			part.noteAll(projected)
			hiddenOps = hiddenOps || !part.empty()
		}
	}
	base := conversationTokens(l.msgs)
	groups := plan.groupOrder(l.msgs)
	room := max(int(l.b.Soft)-l.b.Fixed-base, 0)
	full := min(l.cfg.Context.keepTokens(), room)
	dropped := applyBudget(groups, full, min(restoredStubTokens, room-full))
	woven := plan.render(l.msgs)
	if toolPairProblems(woven) {
		h.plugins.logf("会话 %s 接回工作记录后调用和结果对不上，这次只带说过的话", in.Conversation)
		return
	}
	if (dropped || hiddenOps) && !record.empty() {
		woven = append(woven, Msg{Role: "user", Kind: KindWork, Scope: in.Scope, Text: "（这个会话更早的工具步骤没有放进来，下面是宿主记的累计工作记录；不是公开聊天）\n" + record.render()})
	}
	l.msgs, l.workRecord = woven, record
}

// projectRoot 读一个旧 root 的消息（折叠线之后），投影成合法序列；只读
func (p *workPort) projectRoot(ctx context.Context, owner kernel.Scope, root store.WorkRoot) ([]Msg, error) {
	entries, err := p.service.RootEntries(ctx, owner, root)
	if err != nil {
		return nil, err
	}
	var msgs []Msg
	for _, e := range entries {
		if len(e.Message) == 0 || string(e.Message) == "null" {
			continue
		}
		var m Msg
		if err := json.Unmarshal(e.Message, &m); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return projectWork(msgs, owner.ScopeID, nil)
}

func lastSeq(msgs []Msg) int64 {
	var last int64
	for _, m := range msgs {
		last = max(last, m.Seq)
	}
	return last
}

// ───────────── 累计工作记录 ─────────────

// workRecord 是宿主从工具调用算出来的累计记录，追加在摘要末尾，不让模型写（pi utils.ts 的做法）。
// 只记做过什么，不当事实依据（K2：不拿 summary 当事实数据库），要原文走 result_read
type workRecord struct {
	Modified []string `json:"modified,omitempty"`
	Read     []string `json:"read,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Pending  []string `json:"pending,omitempty"`
	Unknown  []string `json:"unknown,omitempty"`
}

const (
	recordMaxModified = 200
	recordMaxRead     = 100
	recordMaxCommands = 20
	recordMaxPending  = 30
	recordMaxUnknown  = 10
	recordCommandSize = 120
	recordUnknownSize = 200
	workRecordOpen    = "<work-record>"
	workRecordClose   = "</work-record>"
)

// 模型文字里的块一律删掉：工具输出可能伪造，只留宿主自己追加的。没闭合的删到结尾
var workRecordRE = regexp.MustCompile(`(?s)` + workRecordOpen + `.*?(?:` + workRecordClose + `|$)`)

func (r workRecord) empty() bool {
	return len(r.Modified)+len(r.Read)+len(r.Commands)+len(r.Pending)+len(r.Unknown) == 0
}

// render 用 JSON：路径和命令里的换行、闭合标签都转义了（json.Marshal 默认把 < > 写成 <），解析回来不会错位
func (r workRecord) render() string {
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return workRecordOpen + "\n" + string(b) + "\n" + workRecordClose
}

func stripWorkRecord(text string) string {
	if !strings.Contains(text, workRecordOpen) {
		return text
	}
	return strings.TrimSpace(workRecordRE.ReplaceAllString(text, ""))
}

// parseWorkRecord 从摘要里读回上一份；只有摘要末尾由宿主追加的那一块会是合法 JSON，读不出来当没有
func parseWorkRecord(text string) workRecord {
	var r workRecord
	i := strings.LastIndex(text, workRecordOpen)
	if i < 0 {
		return r
	}
	rest := text[i+len(workRecordOpen):]
	j := strings.Index(rest, workRecordClose)
	if j < 0 {
		return r
	}
	if json.Unmarshal([]byte(rest[:j]), &r) != nil {
		return workRecord{}
	}
	return r
}

// touch 记一项：已有的挪到最后，超过上限丢最旧的
func touch(list []string, item string, limit int) []string {
	if item == "" {
		return list
	}
	if i := slices.Index(list, item); i >= 0 {
		list = slices.Delete(list, i, i+1)
	}
	list = append(list, item)
	if len(list) > limit {
		list = list[len(list)-limit:]
	}
	return list
}

func (r *workRecord) merge(o workRecord) {
	for _, x := range o.Modified {
		r.Modified = touch(r.Modified, x, recordMaxModified)
		if i := slices.Index(r.Read, x); i >= 0 {
			r.Read = slices.Delete(r.Read, i, i+1)
		}
	}
	for _, x := range o.Read {
		if !slices.Contains(r.Modified, x) {
			r.Read = touch(r.Read, x, recordMaxRead)
		}
	}
	for _, x := range o.Commands {
		r.Commands = touch(r.Commands, x, recordMaxCommands)
	}
	for _, x := range o.Unknown {
		r.Unknown = touch(r.Unknown, x, recordMaxUnknown)
	}
}

func argString(v jsjson.Value, key string) string {
	if v.Kind != jsjson.Object {
		return ""
	}
	if x, ok := v.Obj[key]; ok && x.Kind == jsjson.String {
		return x.Str
	}
	return ""
}

// noteAll 记下一串消息里的工具调用：写失败的不算改过，结果未知的单独列出来
func (r *workRecord) noteAll(msgs []Msg) {
	for i, m := range msgs {
		if m.Role != "assistant" || len(m.Calls) == 0 {
			continue
		}
		byCall := map[string]Msg{}
		for _, x := range msgs[i+1:] {
			if x.Role != "toolResult" {
				break
			}
			byCall[x.CallID] = x
		}
		for _, c := range m.Calls {
			result, known := byCall[c.ID]
			if known && result.IsError && strings.Contains(result.Text, workUnknownMarker) {
				r.Unknown = touch(r.Unknown, clip(c.Name+"："+workUnknownText, recordUnknownSize), recordMaxUnknown)
				continue
			}
			if known && result.IsError {
				continue
			}
			switch c.Name {
			case "write", "edit":
				one := workRecord{Modified: []string{argString(c.Args, "path")}}
				r.merge(one)
			case "read":
				r.merge(workRecord{Read: []string{argString(c.Args, "path")}})
			case "powershell":
				what := argString(c.Args, "purpose")
				if what == "" {
					what = argString(c.Args, "command")
				}
				r.Commands = touch(r.Commands, clip(what, recordCommandSize), recordMaxCommands)
			}
		}
	}
}

// workActivity：这段上下文算不算在干活。改过文件、跑过命令、清单没做完，或者工作工具调过至少 3 次；
// 只读过一两个文件的闲聊还是闲聊，压缩仍用聊天的提示词
func workActivity(msgs []Msg, state kernel.WorkState, seen workRecord) bool {
	if len(state.Pending) > 0 || len(seen.Modified) > 0 || len(seen.Commands) > 0 {
		return true
	}
	calls := 0
	for _, m := range msgs {
		for _, c := range m.Calls {
			if workTools[c.Name] {
				calls++
			}
		}
	}
	return calls >= 3
}

// ───────────── 溢出时折叠本轮 ─────────────

const foldHandoffPrefix = "（这一轮前面的工具步骤已经折叠，下面是宿主记的累计工作记录；不是公开聊天）\n"

// foldTail 折叠本轮尾部（l.msgs[from:]）更早的工具步骤，返回省下多少 token。前面的轮次压不动、本轮工具结果把窗口撑满时走这里：
// 第 try 次保留最近 keepTokens >> (try-1) 的完整调用组；更早的先换占位，省不到一成就整组拿掉、留一条累计工作记录。
// 只改实例，不动工作记录，下次重建按预算重新算。只有本地回合走到这里（turnFrom、turnCtx 只有渠道回合才读，不用跟着挪）
func (l *Live) foldTail(from, try int) int {
	if from >= len(l.msgs) {
		return 0
	}
	before := conversationTokens(l.msgs[from:])
	keep := max(l.cfg.Context.keepTokens()>>max(try-1, 0), overflowKeepFloor)
	type span struct{ start, end int }
	var groups []span
	for i := from; i < len(l.msgs); i++ {
		if l.msgs[i].Role != "assistant" || len(l.msgs[i].Calls) == 0 || l.msgs[i].Stop != "" {
			continue
		}
		end := i + 1
		for end < len(l.msgs) && l.msgs[end].Role == "toolResult" {
			end++
		}
		groups = append(groups, span{i, end})
		i = end - 1
	}
	used, oldest := 0, len(groups)
	for i := len(groups) - 1; i >= 0; i-- {
		used += conversationTokens(l.msgs[groups[i].start:groups[i].end])
		if used > keep && i != len(groups)-1 {
			break
		}
		oldest = i
	}
	if oldest == 0 {
		return 0
	}
	older := groups[:oldest]
	stubbed := append([]Msg(nil), l.msgs...)
	for _, g := range older {
		stubbed[g.start] = stubCalls(stubbed[g.start])
		for i := g.start + 1; i < g.end; i++ {
			stubResult(&stubbed[i])
		}
	}
	saved := before - conversationTokens(stubbed[from:])
	if float64(saved) >= float64(before)*0.1 {
		l.msgs = stubbed
		return saved
	}
	// 占位省不下多少（结果本来就短、都是调用）：更早的整组拿掉，回复的字留着，宿主记一笔
	var record workRecord
	record.merge(l.workRecord)
	record.noteAll(l.msgs[from:])
	out := append([]Msg(nil), l.msgs[:from]...)
	handoff := Msg{Role: "user", Kind: KindWork, Scope: l.in.Scope, Text: foldHandoffPrefix + record.render()}
	placed := false
	next := 0
	for i := from; i < len(l.msgs); i++ {
		if next < len(older) && i == older[next].start {
			if !placed {
				out = append(out, handoff)
				placed = true
			}
			if m := l.msgs[i]; textOnly(&m) {
				out = append(out, m)
			}
			i = older[next].end - 1
			next++
			continue
		}
		if l.msgs[i].Kind == KindWork && strings.HasPrefix(l.msgs[i].Text, foldHandoffPrefix) {
			continue // 上一次折叠留的，这次换新的
		}
		out = append(out, l.msgs[i])
	}
	saved = before - conversationTokens(out[from:])
	if saved <= 0 {
		return 0
	}
	l.msgs, l.workRecord = out, record
	return saved
}

// textOnly 去掉调用，只留说的话；什么字都没有返回 false
func textOnly(m *Msg) bool {
	m.Calls, m.Thinking, m.Replay, m.HasThinking = nil, "", chat.Replay{}, false
	return strings.TrimSpace(m.Text) != ""
}
