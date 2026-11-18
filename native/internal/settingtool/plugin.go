// Package settingtool 是“设置走工具”（规格-客户端自知与设置工具 第十一节，ADR-330 第 4、5 条）：
// 她可以提议改设置，主人在审批卡上看到旧值、新值、影响范围，确认后才写入；每次改动留记录，能回滚。
//
// 这个包只管流程：提案、卡片、记录、回滚。谁能用、哪些键能改、怎么校验、怎么写，都由宿主（Host）给：
// 写入走设置页同一套服务，模型没有任何文件权限，工具参数里没有路径。
// 批准只认审批卡的结局；提案号只是指针，不接受任何模型自称“已批准”的参数。
package settingtool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// ID 是插件在内核里的名字
const ID = "setting-tool"

// 五个工具名
const (
	ToolPreview  = "preview_setting_change"
	ToolApply    = "apply_setting_change"
	ToolRollback = "rollback_setting_change"
	ToolList     = "list_setting_changes"
	ToolCancel   = "cancel_setting_change"
)

// 确认等级。现在所有项都是 LevelEvery（每次让主人确认）；以后某一项想不用审批，在宿主的键表里换一档、
// apply 里的 switch 加一支。模型看不到这张表，也改不了它；switch 对不认识的等级一律拒绝。
const (
	LevelEvery = "every"
)

// 参数
const (
	ProposalTTL   = 5 * time.Minute // 从 preview 算，不续期
	MaxProposals  = 16              // 同一个会话没用掉的提案上限
	SourceRunes   = 200             // 记录里的来源原话
	ReasonRunes   = 200
	CandidateShow = 5
	ListDefault   = 10
	ListMax       = 30
)

// Spec 是一个能改的键。宿主的白名单表给出，工具里不抄数字范围
type Spec struct {
	Key    string
	Name   string // 给人看的名字：所在地
	Kind   string // location / bool / int / text
	Level  string
	Impact string // 影响范围一句话
	Effect string // 生效时机
}

// Candidate 是所在地这类要先搜的值的一个候选
type Candidate struct {
	ID    string
	Label string
	Value json.RawMessage // 选中后写进配置的完整值
}

// Snapshot 是某个键现在的样子
type Snapshot struct {
	Value   json.RawMessage // 可以直接再写回去的 JSON
	Display string          // 给人看的
	Version string          // 配置文件的版本
}

// Written 是写入的结果
type Written struct{ Version string }

// Card 是审批卡的内容
type Card struct {
	ID, Title, Subject, Text string
	ExpiresAt                time.Time
	Notes                    []string
}

// Who 是认证过的调用者。Place 是给人看的场合名
type Who struct {
	Character, Conversation, Place string
}

// Host 是宿主要给的东西。所有方法的调用者都已经过 Authorize
type Host interface {
	// Visible 说这个听众看不看得见这五个工具。执行时 Authorize 会再核，不能只靠它
	Visible(aud audience.Audience) bool
	// Authorize 核这一次调用确实是宿主正在跑的那一次，身份是主人、场合是本地私聊或主人 QQ 私聊
	Authorize(ctx context.Context, in kernel.Invocation, aud audience.Audience) (Who, error)
	Spec(key string) (Spec, bool)
	Keys() []Spec
	// Current 读保存的值和配置版本
	Current(ctx context.Context, key string) (Snapshot, error)
	// Draft 按配置校验正本空跑一遍，不写盘；返回规范化后的新值和显示。校验不过返回的错误文字会原样给她看
	Draft(ctx context.Context, key string, value json.RawMessage) (Snapshot, error)
	// Places 搜所在地候选（只有 location 用）
	Places(ctx context.Context, query string) ([]Candidate, error)
	// Write 走设置页同一套服务写入，base 是 preview 时的版本
	Write(ctx context.Context, key string, value json.RawMessage, base string) (Written, error)
	// IsConflict 说 Write 返回的错误是不是配置版本冲突
	IsConflict(err error) bool
	// Ask 发审批卡并等回答；approved 才返回 nil
	Ask(ctx context.Context, in kernel.Invocation, who Who, c Card) (Decision, error)
	// Source 取这一轮绑定的主人原话（宿主绑的输入，不是模型填的），取不到返回空
	Source(ctx context.Context, c turn.ToolCall) string
	Now() time.Time
	NewID() string
}

// Decision 是主人在卡片上的回答
type Decision struct {
	Approved bool
	Kind     string // approved / denied / expired / cancelled
	Reason   string
}

// Plugin 实现 turn.Plugin
type Plugin struct {
	Host Host
	Log  *Log

	mu        sync.Mutex
	proposals map[string]*proposal
}

type proposal struct {
	ID, Key, Name    string
	Old, New         json.RawMessage
	OldText, NewText string
	Base             string
	Who              Who
	Created, Expires time.Time
	Reason, Source   string
	Seqs             []int64
	RollbackOf, Note string
	State            string // open / applying / applied / dead
	Change, Result   string
}

func (*Plugin) ID() string { return ID }

var (
	previewDecl = tools.Declaration{Name: ToolPreview, Description: "提议改一项设置，只预览不写入。返回提案号、旧值、新值、影响范围、生效时机；之后用 apply_setting_change 提交，所有者会在审批卡上确认。只在所有者明确要你改某项设置时用，问“能不能改”“会怎样”只查询。所在地的 value 写地名（可以写中文、英文或拼音，写空字符串是清空），有多个候选时会列出来，选定后带 candidate_id 再预览一次。不认识的键会被拒绝。",
		Parameters: `{"type":"object","additionalProperties":false,"required":["key","value"],"properties":{"key":{"type":"string","description":"设置键，比如 location；可改的键见 inspect_client"},"value":{"type":["string","number","boolean"],"description":"新值。开关写 true/false，数字写整数，所在地写地名（空字符串清空）"},"candidate_id":{"type":"string","description":"所在地有多个候选时，上一次预览列出的候选编号"},"reason":{"type":"string","description":"一句话说明为什么要改（只给所有者看）"}}}`}
	applyDecl = tools.Declaration{Name: ToolApply, Description: "提交一个已预览的提案：所有者在审批卡上确认后才写入，没确认、拒绝、过期都不会改。提案 5 分钟内有效；配置在预览之后被别处改过会报冲突，要重新预览。",
		Parameters: `{"type":"object","additionalProperties":false,"required":["proposal_id"],"properties":{"proposal_id":{"type":"string"}}}`}
	rollbackDecl = tools.Declaration{Name: ToolRollback, Description: "回滚一次改动：只把那一项恢复到那次改动之前的值，生成新的提案，之后照样用 apply_setting_change 提交、由所有者确认。那一项在那次之后又被改过的话，预览里会写明。",
		Parameters: `{"type":"object","additionalProperties":false,"required":["change_id"],"properties":{"change_id":{"type":"string","description":"list_setting_changes 里的改动编号"}}}`}
	listDecl = tools.Declaration{Name: ToolList, Description: "看设置改动记录（谁、什么时候、旧值、新值、因为哪句话、结果）和还没用掉的提案。",
		Parameters: `{"type":"object","additionalProperties":false,"properties":{"limit":{"type":"integer","minimum":1,"maximum":30},"key":{"type":"string","description":"只看这一项"}}}`}
	cancelDecl = tools.Declaration{Name: ToolCancel, Description: "收回一个还没用掉的提案。",
		Parameters: `{"type":"object","additionalProperties":false,"required":["proposal_id"],"properties":{"proposal_id":{"type":"string"}}}`}
)

// Decls 给测试和宿主核对
func Decls() []tools.Declaration {
	return []tools.Declaration{previewDecl, applyDecl, rollbackDecl, listDecl, cancelDecl}
}

// Setup 实现 turn.Plugin
func (p *Plugin) Setup(r *turn.Registry) (func(), error) {
	if p.Host == nil || p.Log == nil {
		return nil, fmt.Errorf("设置工具缺少宿主或记录")
	}
	p.mu.Lock()
	p.proposals = map[string]*proposal{}
	p.mu.Unlock()
	var undos []func()
	stop := func() {
		for i := len(undos) - 1; i >= 0; i-- {
			undos[i]()
		}
	}
	for _, t := range []turn.Tool{
		{Decl: previewDecl, For: p.Host.Visible, Exec: p.preview},
		{Decl: applyDecl, For: p.Host.Visible, Exec: p.apply},
		{Decl: rollbackDecl, For: p.Host.Visible, Exec: p.rollback},
		{Decl: listDecl, For: p.Host.Visible, Exec: p.list},
		{Decl: cancelDecl, For: p.Host.Visible, Exec: p.cancel},
	} {
		undo, err := r.OnTool(t)
		if err != nil {
			stop()
			return nil, err
		}
		undos = append(undos, undo)
	}
	return stop, nil
}

func fail(s string) (tools.Result, error) { return tools.Result{Text: s, IsError: true}, nil }
func ok(s string) (tools.Result, error)   { return tools.Result{Text: s}, nil }

// 不认识的键、没权限、读不出，都不透露细节：同一句
const denied = "当前身份不能改设置。"

// who 核身份：工具收到的会话要和身份自洽，再问宿主
func (p *Plugin) who(ctx context.Context, c turn.ToolCall) (Who, bool) {
	s := c.Identity.Scope
	if c.CharacterID == "" || c.CharacterID != s.CharacterID || c.Conversation == "" || c.Conversation != s.Conversation || c.Audience.Conversation != c.Conversation {
		return Who{}, false
	}
	if !p.Host.Visible(c.Audience) {
		return Who{}, false
	}
	w, err := p.Host.Authorize(ctx, c.Identity, c.Audience)
	if err != nil || w.Character != c.CharacterID || w.Conversation != c.Conversation {
		return Who{}, false
	}
	return w, true
}

// args 取参数：只收声明过的名字，值只收文字、数字、布尔
type args struct {
	strs  map[string]string
	value jsjson.Value
	has   map[string]bool
	num   map[string]float64
}

func parseArgs(v jsjson.Value, allowed ...string) (args, string) {
	a := args{strs: map[string]string{}, has: map[string]bool{}, num: map[string]float64{}}
	if v.Kind != jsjson.Object {
		return a, "参数要写成对象。"
	}
	for k, x := range v.Obj {
		if !contains(allowed, k) {
			return a, "不支持的参数：" + cleanTail(k)
		}
		if x.Kind == jsjson.Null {
			continue
		}
		a.has[k] = true
		switch {
		case k == "value":
			if x.Kind != jsjson.String && x.Kind != jsjson.Number && x.Kind != jsjson.Bool {
				return a, "value 要写成文字、数字或 true/false。"
			}
			a.value = x
		case k == "limit":
			if x.Kind != jsjson.Number {
				return a, "limit 要写成整数。"
			}
			a.num[k] = x.Num
		default:
			if x.Kind != jsjson.String {
				return a, k + " 要写成文字。"
			}
			a.strs[k] = x.Str
		}
	}
	return a, ""
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// cleanTail 把外来的文字收拾成能回给她的短句：去控制字符、限长
func cleanTail(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40]) + "…"
	}
	return s
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (p *Plugin) session(w Who) string { return w.Character + "|" + w.Conversation }

// valueJSON 把参数里的 value 按键的类型整理成 JSON。location 的 value 是地名，不走这里
func valueJSON(kind string, v jsjson.Value) (json.RawMessage, string) {
	switch kind {
	case "bool":
		switch {
		case v.Kind == jsjson.Bool:
			return json.RawMessage(fmt.Sprint(v.Bool)), ""
		case v.Kind == jsjson.String && (v.Str == "true" || v.Str == "false"):
			return json.RawMessage(v.Str), ""
		}
		return nil, "这一项要写 true 或 false。"
	case "int":
		var n float64
		switch v.Kind {
		case jsjson.Number:
			n = v.Num
		case jsjson.String:
			if _, err := fmt.Sscanf(strings.TrimSpace(v.Str), "%f", &n); err != nil {
				return nil, "这一项要写整数。"
			}
		default:
			return nil, "这一项要写整数。"
		}
		if n != float64(int64(n)) || n < -1e9 || n > 1e9 {
			return nil, "这一项要写整数。"
		}
		return json.RawMessage(fmt.Sprint(int64(n))), ""
	case "text":
		if v.Kind != jsjson.String {
			return nil, "这一项要写文字。"
		}
		raw, _ := json.Marshal(v.Str)
		return raw, ""
	}
	return nil, "这一项的类型不对。"
}

func (p *Plugin) preview(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
	who, good := p.who(ctx, c)
	if !good {
		return fail(denied)
	}
	a, bad := parseArgs(c.Args, "key", "value", "candidate_id", "reason")
	if bad != "" {
		return fail(bad)
	}
	key := strings.TrimSpace(a.strs["key"])
	spec, known := p.Host.Spec(key)
	if !known || !a.has["value"] {
		return fail("这一项现在不能通过工具改，或没写 value。可改的项见 inspect_client。")
	}
	var raw json.RawMessage
	if spec.Kind == "location" {
		var res tools.Result
		var picked bool
		raw, res, picked = p.pickPlace(ctx, a)
		if !picked {
			return res, nil
		}
	} else {
		var msg string
		if raw, msg = valueJSON(spec.Kind, a.value); msg != "" {
			return fail(spec.Name + "：" + msg)
		}
	}
	return p.propose(ctx, c, who, spec, raw, "", a.strs["reason"])
}

// pickPlace 把所在地的地名变成要写的值：空字符串清空；一个候选直接用；多个候选要她带 candidate_id 选
func (p *Plugin) pickPlace(ctx context.Context, a args) (json.RawMessage, tools.Result, bool) {
	if a.value.Kind != jsjson.String {
		r, _ := fail("所在地要写地名（文字）；清空写空字符串。")
		return nil, r, false
	}
	text := strings.TrimSpace(a.value.Str)
	if text == "" {
		return json.RawMessage(`{}`), tools.Result{}, true
	}
	if len([]rune(text)) > 64 {
		r, _ := fail("地名太长了，写城市或区县的名字就行。")
		return nil, r, false
	}
	cands, err := p.Host.Places(ctx, text)
	if err != nil {
		r, _ := fail("这次没能查到候选地点（网络不通或对方没回），稍后再试，或请所有者到设置里选。")
		return nil, r, false
	}
	if len(cands) == 0 {
		r, _ := ok("没查到叫「" + cleanTail(text) + "」的地方。换个写法再试：城市名、区县名，或英文、拼音；也可以请所有者到设置里搜索选择。")
		return nil, r, false
	}
	if len(cands) > CandidateShow {
		cands = cands[:CandidateShow]
	}
	if id := strings.TrimSpace(a.strs["candidate_id"]); id != "" {
		for _, cd := range cands {
			if cd.ID == id {
				return cd.Value, tools.Result{}, true
			}
		}
		r, _ := fail("候选编号不对：地名要和上一次预览写的一样，编号取上一次列出来的。")
		return nil, r, false
	}
	if len(cands) == 1 {
		return cands[0].Value, tools.Result{}, true
	}
	var b strings.Builder
	b.WriteString("「" + cleanTail(text) + "」有几个候选，还没有生成提案。用户说得明确就选一个，带上 candidate_id 再预览一次（value 保持原样）；拿不准就问用户在哪个：\n")
	for _, cd := range cands {
		b.WriteString("- candidate_id=" + cd.ID + "：" + cleanTail(cd.Label) + "\n")
	}
	r, _ := ok(strings.TrimRight(b.String(), "\n"))
	return nil, r, false
}

// propose 校验、记提案、回预览。rollbackOf 非空是回滚生成的
func (p *Plugin) propose(ctx context.Context, c turn.ToolCall, who Who, spec Spec, raw json.RawMessage, rollbackOf, reason string) (tools.Result, error) {
	cur, err := p.Host.Current(ctx, spec.Key)
	if err != nil {
		return fail("现在读不到这一项的设置，先不改。")
	}
	draft, err := p.Host.Draft(ctx, spec.Key, raw)
	if err != nil {
		return fail(spec.Name + "：" + cleanTail2(err.Error()))
	}
	if jsonEqual(cur.Value, draft.Value) {
		return ok(spec.Name + "现在已经是这个值（" + cur.Display + "），不用改。")
	}
	now := p.Host.Now()
	src := clip(p.Host.Source(ctx, c), SourceRunes)
	pr := &proposal{ID: "p-" + p.Host.NewID(), Key: spec.Key, Name: spec.Name, Old: cur.Value, New: draft.Value, OldText: cur.Display, NewText: draft.Display,
		Base: cur.Version, Who: who, Created: now, Expires: now.Add(ProposalTTL), Reason: clip(reason, ReasonRunes), Source: src,
		Seqs: append([]int64(nil), c.InputSeqs...), RollbackOf: rollbackOf, State: "open"}
	if rollbackOf != "" {
		pr.Note = p.rollbackNote(ctx, rollbackOf, cur)
	}
	p.mu.Lock()
	p.sweep(now)
	s := p.session(who)
	// 同一个键再 preview 就顶掉旧提案；一个会话的提案有上限
	open := 0
	for id, o := range p.proposals {
		if p.session(o.Who) != s || o.State != "open" {
			continue
		}
		if o.Key == spec.Key {
			o.State = "dead"
			delete(p.proposals, id)
			continue
		}
		open++
	}
	if open >= MaxProposals {
		p.mu.Unlock()
		return fail("没用掉的提案太多了，先收回几个（cancel_setting_change）。")
	}
	p.proposals[pr.ID] = pr
	p.mu.Unlock()
	return ok(p.previewText(pr, spec))
}

func cleanTail2(s string) string {
	s = cleanTail(s)
	return strings.TrimSpace(s)
}

func (p *Plugin) previewText(pr *proposal, spec Spec) string {
	var b strings.Builder
	b.WriteString("提案 " + pr.ID + "（还没有写入，5 分钟内有效）\n")
	b.WriteString("设置：" + spec.Name + "\n旧值：" + pr.OldText + "\n新值：" + pr.NewText + "\n")
	b.WriteString("影响：" + spec.Impact + "\n生效：" + spec.Effect + "\n确认：" + levelText(spec.Level) + "\n")
	if pr.Note != "" {
		b.WriteString("注意：" + pr.Note + "\n")
	}
	b.WriteString("下一步：用 apply_setting_change（proposal_id=" + pr.ID + "）提交；不用了就 cancel_setting_change。")
	return b.String()
}

func levelText(l string) string {
	if l == LevelEvery {
		return "每次都要所有者在审批卡上确认"
	}
	return "按宿主规定"
}

func (p *Plugin) rollbackNote(ctx context.Context, changeID string, cur Snapshot) string {
	changes, _, err := p.Log.All()
	if err != nil {
		return ""
	}
	for _, ch := range changes {
		if ch.Change == changeID && (ch.Outcome == OutcomeApplied || ch.Outcome == OutcomeUnverified || ch.Outcome == OutcomeUnknown) && !jsonEqual(cur.Value, ch.New) {
			return "这一项在那次改动之后又被改过（现在是「" + cur.Display + "」，不是那次写入的「" + ch.NewText + "」），恢复会覆盖现在的值。"
		}
	}
	return ""
}

// sweep 清掉过期和已了结的提案（调用方持锁）。已应用的留着，重复 apply 要能回同一个结果；到期 10 分钟后才清
func (p *Plugin) sweep(now time.Time) {
	for id, o := range p.proposals {
		if o.State == "dead" || (now.After(o.Expires.Add(5*time.Minute)) && o.State != "applying") {
			delete(p.proposals, id)
		}
	}
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return string(ax) == string(by)
}

func (p *Plugin) find(who Who, id string) (*proposal, bool) {
	o := p.proposals[strings.TrimSpace(id)]
	if o == nil || p.session(o.Who) != p.session(who) {
		return nil, false
	}
	return o, true
}

func (p *Plugin) cancel(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
	who, good := p.who(ctx, c)
	if !good {
		return fail(denied)
	}
	a, bad := parseArgs(c.Args, "proposal_id")
	if bad != "" {
		return fail(bad)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	o, found := p.find(who, a.strs["proposal_id"])
	if !found {
		return fail("没有这个提案（编号不对、已过期或不是这个会话的）。")
	}
	switch o.State {
	case "applying":
		return fail("这个提案正在等所有者确认，不能收回；所有者点了拒绝或到期它就作废。")
	case "applied":
		return fail("这个提案已经应用了，要恢复用 rollback_setting_change。")
	}
	o.State = "dead"
	delete(p.proposals, o.ID)
	return ok("提案 " + o.ID + " 已收回，没有改任何设置。")
}

func (p *Plugin) apply(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
	who, good := p.who(ctx, c)
	if !good {
		return fail(denied)
	}
	a, bad := parseArgs(c.Args, "proposal_id")
	if bad != "" {
		return fail(bad)
	}
	p.mu.Lock()
	o, found := p.find(who, a.strs["proposal_id"])
	if !found {
		p.mu.Unlock()
		return fail("没有这个提案（编号不对、已过期或不是这个会话的），重新预览。")
	}
	switch o.State {
	case "applied":
		res := o.Result
		p.mu.Unlock()
		return ok(res + "\n（这个提案之前已经应用过，这次没有再写。）")
	case "applying":
		p.mu.Unlock()
		return fail("这个提案正在等所有者确认，别重复提交。")
	case "dead":
		p.mu.Unlock()
		return fail("这个提案已作废，重新预览。")
	}
	now := p.Host.Now()
	if now.After(o.Expires) {
		o.State = "dead"
		delete(p.proposals, o.ID)
		p.mu.Unlock()
		return fail("这个提案已经过期（5 分钟），重新预览。")
	}
	spec, known := p.Host.Spec(o.Key)
	if !known {
		o.State = "dead"
		p.mu.Unlock()
		return fail("这一项现在不能通过工具改。")
	}
	o.State = "applying"
	p.mu.Unlock()

	res, outcome := p.run(ctx, c, who, o, spec)
	p.mu.Lock()
	switch outcome {
	case OutcomeApplied, OutcomeUnverified:
		o.State, o.Result = "applied", res.Text
	default:
		o.State = "dead"
	}
	p.mu.Unlock()
	return res, nil
}

// run 是 apply 的主体：核版本 → 卡片 → 再核版本 → 记开始 → 写 → 回读 → 记结局
func (p *Plugin) run(ctx context.Context, c turn.ToolCall, who Who, o *proposal, spec Spec) (tools.Result, string) {
	change := "c-" + p.Host.NewID()
	base := Record{Change: change, Proposal: o.ID, Key: o.Key, Place: who.Place, Character: who.Character, Conversation: who.Conversation,
		Old: o.Old, New: o.New, OldText: o.OldText, NewText: o.NewText, Source: o.Source, SourceSeqs: o.Seqs, Reason: o.Reason, RollbackOf: o.RollbackOf, BaseVersion: o.Base}
	end := func(outcome, detail string, written Written) {
		r := base
		r.Phase, r.At, r.Outcome, r.Detail, r.NewVersion = PhaseEnd, p.Host.Now().UnixMilli(), outcome, detail, written.Version
		if err := p.Log.Append(r); err != nil {
			// 结局写不进记录：设置已经有了定论，只能如实告诉她；开始行已在，读出来会标“结局不明”再按当前值判
			_ = err
		}
	}
	stale := func(stage string) (tools.Result, string) {
		end(OutcomeConflict, stage, Written{})
		r, _ := fail("配置在预览之后被改过了（" + stage + "），这次没有写。重新预览，所有者看到最新的旧值再确认。")
		return r, OutcomeConflict
	}
	check := func() (bool, error) {
		cur, err := p.Host.Current(ctx, o.Key)
		if err != nil {
			return false, err
		}
		return cur.Version == o.Base && jsonEqual(cur.Value, o.Old), nil
	}
	if same, err := check(); err != nil {
		r, _ := fail("现在读不到这一项的设置，先不改。")
		return r, OutcomeFailed
	} else if !same {
		return stale("发卡前")
	}
	// 确认等级：现在只有“每次确认”。以后加“不用审批”的档在这里加一支，别处不动
	switch spec.Level {
	case LevelEvery:
		card := Card{ID: "setting:" + o.ID, Title: "修改设置：" + spec.Name, Subject: spec.Name, ExpiresAt: o.Expires, Text: cardText(o, spec, who)}
		if o.Note != "" {
			card.Notes = append(card.Notes, o.Note)
		}
		d, err := p.Host.Ask(ctx, c.Identity, who, card)
		if err != nil {
			end(OutcomeFailed, "发不出确认卡", Written{})
			r, _ := fail("现在发不出确认卡，没有改。")
			return r, OutcomeFailed
		}
		if !d.Approved {
			outcome, text := OutcomeDenied, "所有者在卡片上拒绝了，没有改。"
			switch d.Kind {
			case "expired":
				outcome, text = OutcomeExpired, "确认卡到期没人点，没有改。"
			case "cancelled":
				outcome, text = OutcomeCancelled, "这一轮被停掉了，没有改。"
			default:
				if d.Reason != "" {
					text += "原因：" + clip(d.Reason, 100)
				}
			}
			end(outcome, "", Written{})
			r, _ := fail(text)
			return r, outcome
		}
	default:
		end(OutcomeFailed, "没有这一档的处理", Written{})
		r, _ := fail("这一项的确认方式宿主不认识，没有改。")
		return r, OutcomeFailed
	}
	// 主人点卡的这几分钟里，设置页可能改过：批准后再核一遍
	if same, err := check(); err != nil {
		end(OutcomeFailed, "批准后读不到设置", Written{})
		r, _ := fail("批准后读不到这一项的设置，没有改。")
		return r, OutcomeFailed
	} else if !same {
		return stale("批准之后")
	}
	// 写不进记录就不改设置
	begin := base
	begin.Phase, begin.At = PhaseBegin, p.Host.Now().UnixMilli()
	if err := p.Log.Append(begin); err != nil {
		r, _ := fail("改动记录写不进去，为了能回滚，这次没有改。")
		return r, OutcomeFailed
	}
	w, err := p.Host.Write(ctx, o.Key, o.New, o.Base)
	if err != nil {
		if p.Host.IsConflict(err) {
			end(OutcomeConflict, "写入时版本已变", Written{})
			r, _ := fail("配置在写入的瞬间被改过了，没有写。重新预览再试。")
			return r, OutcomeConflict
		}
		end(OutcomeFailed, clip(err.Error(), 200), Written{})
		r, _ := fail("写入没成功，设置没有变：" + cleanTail2(err.Error()))
		return r, OutcomeFailed
	}
	cur, rerr := p.Host.Current(ctx, o.Key)
	if rerr != nil || !jsonEqual(cur.Value, o.New) {
		end(OutcomeUnverified, "写入后回读对不上", w)
		return tools.Result{Text: "写入已提交，但回读的值和预期对不上，请所有者在设置页确认。改动编号 " + change + "。", IsError: true}, OutcomeUnverified
	}
	end(OutcomeApplied, "", w)
	var b strings.Builder
	b.WriteString("已改：" + spec.Name + "，" + o.OldText + " → " + o.NewText + "。改动编号 " + change + "，生效：" + spec.Effect + "。")
	b.WriteString("\n要恢复用 rollback_setting_change（change_id=" + change + "）。")
	return tools.Result{Text: b.String()}, OutcomeApplied
}

func cardText(o *proposal, spec Spec, who Who) string {
	var b strings.Builder
	// 新旧值可能带着外来的文字（模型传的 value）：折成一行再拼，卡上的“来自”“你说的”只能是我们自己写的行
	b.WriteString("设置：" + spec.Name + "（" + spec.Key + "）\n旧值：" + oneLine(o.OldText, 200) + "\n新值：" + oneLine(o.NewText, 200) + "\n")
	b.WriteString("影响：" + spec.Impact + "\n生效：" + spec.Effect + "\n来自：" + who.Place)
	if o.Source != "" {
		b.WriteString("\n你说的：" + o.Source)
	}
	if o.Reason != "" {
		b.WriteString("\n她的理由：" + o.Reason)
	}
	if o.RollbackOf != "" {
		b.WriteString("\n这是回滚：恢复改动 " + o.RollbackOf + " 之前的值")
	}
	return b.String()
}

// oneLine 把换行等控制字符和连续空白折成一个空格，并限长
func oneLine(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return clip(s, n)
}

func (p *Plugin) rollback(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
	who, good := p.who(ctx, c)
	if !good {
		return fail(denied)
	}
	a, bad := parseArgs(c.Args, "change_id")
	if bad != "" {
		return fail(bad)
	}
	id := strings.TrimSpace(a.strs["change_id"])
	changes, _, err := p.Log.All()
	if err != nil {
		return fail("改动记录读不出来，没法回滚。")
	}
	for _, ch := range changes {
		if ch.Change != id {
			continue
		}
		if ch.Character != who.Character {
			break // 别的角色的改动当没有
		}
		spec, known := p.Host.Spec(ch.Key)
		if !known {
			return fail("那一项现在不能通过工具改，回滚请所有者在设置页做。")
		}
		cur, err := p.Host.Current(ctx, ch.Key)
		if err != nil {
			return fail("现在读不到这一项的设置，先不改。")
		}
		switch ch.Outcome {
		case OutcomeApplied, OutcomeUnverified:
		case OutcomeUnknown:
			// 开始写了没有结局：当前值是新值才有东西可回滚，是旧值说明没写进去
			if !jsonEqual(cur.Value, ch.New) {
				return ok("那次改动结局不明，而且现在的值不是它要写的新值，当作没写进去，不用回滚。")
			}
		default:
			return ok("那次改动没有生效（" + outcomeText(ch.Outcome) + "），不用回滚。")
		}
		return p.propose(ctx, c, who, spec, ch.Old, id, "回滚改动 "+id)
	}
	return fail("没有这个改动编号。用 list_setting_changes 看。")
}

func outcomeText(o string) string {
	switch o {
	case OutcomeApplied:
		return "已写入"
	case OutcomeUnverified:
		return "已写入，回读对不上"
	case OutcomeFailed:
		return "写入失败"
	case OutcomeDenied:
		return "所有者拒绝"
	case OutcomeExpired:
		return "确认卡到期"
	case OutcomeCancelled:
		return "被停掉"
	case OutcomeConflict:
		return "版本冲突"
	}
	return "结局不明"
}

func (p *Plugin) list(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
	who, good := p.who(ctx, c)
	if !good {
		return fail(denied)
	}
	a, bad := parseArgs(c.Args, "limit", "key")
	if bad != "" {
		return fail(bad)
	}
	limit := ListDefault
	if a.has["limit"] {
		if a.num["limit"] != float64(int(a.num["limit"])) || a.num["limit"] < 1 || a.num["limit"] > ListMax {
			return fail("limit 要写 1 到 30 的整数。")
		}
		limit = int(a.num["limit"])
	}
	key := strings.TrimSpace(a.strs["key"])
	changes, bad2, err := p.Log.All()
	if err != nil {
		return fail("改动记录读不出来。")
	}
	var b strings.Builder
	n := 0
	for i := len(changes) - 1; i >= 0 && n < limit; i-- {
		ch := changes[i]
		if ch.Character != who.Character || (key != "" && ch.Key != key) {
			continue
		}
		if _, known := p.Host.Spec(ch.Key); !known && ch.Key != "" {
			// 键后来不在白名单了：记录仍给主人看，但不给值
		}
		if n == 0 {
			b.WriteString("改动记录（新的在前）：\n")
		}
		n++
		outcome := outcomeText(ch.Outcome)
		if ch.Outcome == OutcomeUnknown {
			if cur, err := p.Host.Current(ctx, ch.Key); err == nil {
				if jsonEqual(cur.Value, ch.New) {
					outcome += "，现在的值是新值"
				} else {
					outcome += "，现在的值不是新值，当作没写进去"
				}
			}
		}
		b.WriteString(fmt.Sprintf("- %s 改动 %s：%s，%s → %s；%s；%s", timeText(ch.At), ch.Change, ch.Key, clip(ch.OldText, 80), clip(ch.NewText, 80), ch.Place, outcome))
		if ch.Source != "" {
			b.WriteString("；因为：" + clip(ch.Source, 80))
		}
		if ch.RollbackOf != "" {
			b.WriteString("；回滚了 " + ch.RollbackOf)
		}
		b.WriteString("\n")
	}
	if n == 0 {
		b.WriteString("没有改动记录。\n")
	}
	if bad2 > 0 {
		b.WriteString(fmt.Sprintf("（记录里有 %d 行读不懂，已跳过。）\n", bad2))
	}
	p.mu.Lock()
	now := p.Host.Now()
	var open []string
	for _, o := range p.proposals {
		if p.session(o.Who) == p.session(who) && o.State == "open" && now.Before(o.Expires) && (key == "" || o.Key == key) {
			open = append(open, fmt.Sprintf("- 提案 %s：%s，%s → %s，%s 前有效", o.ID, o.Key, clip(o.OldText, 60), clip(o.NewText, 60), o.Expires.Local().Format("15:04")))
		}
	}
	p.mu.Unlock()
	if len(open) > 0 {
		sortStrings(open)
		b.WriteString("没用掉的提案：\n" + strings.Join(open, "\n") + "\n")
	}
	return ok(strings.TrimRight(b.String(), "\n"))
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func timeText(ms int64) string {
	if ms <= 0 {
		return "时间不明"
	}
	return time.UnixMilli(ms).Local().Format("01-02 15:04")
}
