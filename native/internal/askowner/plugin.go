package askowner

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// ID 是插件在内核里的名字
const ID = "ask-owner"

// ToolName 是工具名（policy.Specs 里登记，turn.stopsAtOnce 里点名）
const ToolName = "ask_owner"

// 参数上限（规格第一节）
const (
	maxQuestion = 500
	maxLabel    = 40
	maxDetail   = 120
	minOptions  = 2
	maxOptions  = 4
)

// deadlineMargin 是这一轮总时限前留给她收尾的时间：不够就不等
const deadlineMargin = 45 * time.Second

// Decl 是工具声明
var Decl = tools.Declaration{
	Name: ToolName,
	Description: "向所有者提一个问题，给 2～4 个选项，等他回答后接着做。" +
		"只在几条路后果明显不同、查资料也定不了、猜错代价大的时候用；能自己定的就定下来接着做，别问。" +
		"问题要能单独看懂。所有者也可以不点选项直接打字，所以不用写“其他”。" +
		"回答不是批准：要动手的操作照常过审批，不要用它问“可以执行吗”。",
	Parameters: `{"type":"object","additionalProperties":false,"required":["question","options"],"properties":{` +
		`"question":{"type":"string","description":"一个问题，能单独看懂"},` +
		`"options":{"type":"array","minItems":2,"maxItems":4,"description":"2～4 个互斥的选择","items":{"type":"object","additionalProperties":false,"required":["label"],"properties":{` +
		`"label":{"type":"string","description":"短名字，一行"},"detail":{"type":"string","description":"可选，一句话说明选它会怎样"}}}}}}`,
}

// Options 是装配处给的东西。身份、无人值守都由宿主判，不从参数读
type Options struct {
	Center *Center
	// Allowed 再核一遍身份：工具清单之外，执行时也只认主人在本地私聊的前台回合
	Allowed func(turn.ToolCall) bool
	// Unattended 是这个会话现在没人等着回答（目标、定期任务、自检）
	Unattended func(conversation string) bool
	// SteerPending 是这一轮有插话还没送到她眼前：先看那句话，不再挂卡片。nil = 没有
	SteerPending func(turn.ToolCall) bool
	WaitFor      func() time.Duration
	// Said 把问题存成她说的一句；Answered 把点的选项存成主人说的一句。失败只记日志，不影响这一轮
	Said, Answered func(c turn.ToolCall, text string) error
	// Notify 提醒主人（窗口不在前台才弹，由宿主定）
	Notify func(c turn.ToolCall, text string)
	Now    func() time.Time
	Logf   func(string, ...any)
}

// Plugin 实现 turn.Plugin
type Plugin struct{ o Options }

// New 造插件；Center、Allowed、Unattended、WaitFor 必须给
func New(o Options) *Plugin {
	if o.Said == nil {
		o.Said = func(turn.ToolCall, string) error { return nil }
	}
	if o.Answered == nil {
		o.Answered = func(turn.ToolCall, string) error { return nil }
	}
	if o.Notify == nil {
		o.Notify = func(turn.ToolCall, string) {}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	return &Plugin{o: o}
}

func (p *Plugin) ID() string { return ID }

// Setup 实现 turn.Plugin。可见性由 Visible 和 policy.Specs 的 owner-console 一起管：线、本地群、QQ 都看不见；
// 子任务由 authorizeInternal 挡
func (p *Plugin) Setup(r *turn.Registry) (func(), error) {
	return r.OnTool(turn.Tool{Decl: Decl, For: Visible, Exec: p.exec})
}

// Visible 是谁的工具清单里有 ask_owner：主人在本地私聊（含话题和自检）。policy 的 owner-console 只看渠道和 PrivateOK，
// 本地群聊也过得去，所以这里再挡：群里同时等好几个角色，谁来答没定；线的 PrivateOK 为假
func Visible(a audience.Audience) bool {
	return a.Channel == "console" && a.PrivateOK && a.OwnerAsked && a.Scene != "group" &&
		!strings.Contains(a.Conversation, ":group:") && !audience.IsThread(a.Conversation) && !audience.IsWhisper(a.Conversation)
}

// 给她看的结果。回答一律写明不是批准；自由回答用中性措辞，不替主人说“接着做”
const (
	notGrant    = "这只是回答，不是批准：要动手的操作照常过审批。"
	decideOrEnd = "能自己定就选最稳妥的接着做，说明你选了什么；定不了就说明卡在哪，结束这一轮。"
	inChat      = "问题已经留在聊天里，他回了会开新的一轮。"
)

func fail(s string) (tools.Result, error) { return tools.Result{Text: s, IsError: true}, nil }
func done(s string) (tools.Result, error) { return tools.Result{Text: s}, nil }

func (p *Plugin) exec(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
	if c.Identity.ToolID == "" || !p.o.Allowed(c) {
		return fail("这里不能用 ask_owner：只有所有者在本地私聊里的回合能问。")
	}
	text, options, bad := parseQuestion(c.Args)
	if bad != "" {
		return fail(bad)
	}
	// 她还没看到主人刚插的话：问了就是工具等回答、插话等工具，互相等到超时。这次当没问，不留东西
	if p.o.SteerPending != nil && p.o.SteerPending(c) {
		return done("所有者刚插了一句话，你还没看到（就在这条后面），这次没有问。先看他说什么。")
	}
	// 不挂卡片的几种情况：问题连选项一起留在聊天里，主人回来看得懂
	why := ""
	wait, enough := p.waitFor(ctx)
	switch {
	case p.o.Unattended(c.Conversation):
		why = "现在是自动运行，没人等着回答，这次没有等。问题已经留在聊天里，也提醒了所有者。" + decideOrEnd
	case p.o.Center.Missed(c.Conversation, c.TurnID):
		why = "这一轮上一个问题就没等到回答，所有者现在多半不在，这次没有等。" + inChat + decideOrEnd
	case p.o.Center.Full():
		why = "所有者那边已经有好几个问题在等了，这次没有等。" + inChat + decideOrEnd
	case !enough:
		why = "这一轮快到时限了，来不及等回答。" + inChat + "说明卡在哪，结束这一轮。"
	}
	if why != "" {
		p.say(c, withOptions(text, options))
		p.o.Notify(c, text)
		return done(why)
	}
	p.say(c, text)
	p.o.Notify(c, text)
	now := p.o.Now()
	r := p.o.Center.Ask(ctx, Question{ID: c.Identity.ToolID, CharacterID: c.CharacterID, Conversation: c.Conversation, TurnID: c.TurnID, Text: text, Options: options,
		CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(wait).UnixMilli()}, wait)
	switch r.Kind {
	case Chosen:
		label := options[r.Option].Label
		if err := p.o.Answered(c, label); err != nil {
			p.o.Logf("主人选的答案没写进聊天记录：%v", err)
		}
		return done("所有者选了：「" + label + "」。" + notGrant)
	case Said:
		return done("所有者没有点选项，直接回了一句（就在这条后面）。看他怎么说再定。" + notGrant)
	case Skipped:
		return done("所有者先不答这个问题。别再问同一件事。" + decideOrEnd)
	case Expired:
		minutes := max(1, int(math.Round(wait.Minutes())))
		return done(fmt.Sprintf("所有者 %d 分钟没回答，这不算同意。%s不靠这个答案的活先做完；靠它的别猜，说明卡在哪，结束这一轮。", minutes, inChat))
	}
	return fail("问题收回了：这一轮被停下了。")
}

func (p *Plugin) say(c turn.ToolCall, text string) {
	if err := p.o.Said(c, text); err != nil {
		p.o.Logf("问题没写进聊天记录：%v", err)
	}
}

// waitFor 是这次能等多久：设置的时长，再压到这一轮总时限之内，给她留收尾的时间。
// 这一轮的总时限是固定的（context.WithDeadline），不够就不等，不能让它到点把工具切断
func (p *Plugin) waitFor(ctx context.Context) (time.Duration, bool) {
	wait := p.o.WaitFor()
	if deadline, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(deadline)-deadlineMargin)
	}
	return wait, wait > 0
}

// withOptions 是没有卡片时存进聊天的那句：问题加选项
func withOptions(text string, options []Option) string {
	labels := make([]string, len(options))
	for i, o := range options {
		labels[i] = o.Label
	}
	return text + "\n可选：" + strings.Join(labels, " / ")
}

// parseQuestion 校验参数；bad 非空时是告诉她怎么改的一句话
func parseQuestion(args jsjson.Value) (text string, options []Option, bad string) {
	if args.Kind != jsjson.Object {
		return "", nil, "参数要有 question 和 options。"
	}
	q := args.Obj["question"]
	if q.Kind != jsjson.String {
		return "", nil, "question 要是一段文字。"
	}
	text = strings.TrimSpace(q.Str)
	switch {
	case text == "":
		return "", nil, "question 不能是空的。"
	case utf8.RuneCountInString(text) > maxQuestion:
		return "", nil, fmt.Sprintf("question 太长了，最多 %d 字。问一件事就够。", maxQuestion)
	case !cleanText(text, true):
		return "", nil, "question 里有控制字符，去掉再问。"
	}
	raw := args.Obj["options"]
	if raw.Kind != jsjson.Array || len(raw.Arr) < minOptions || len(raw.Arr) > maxOptions {
		return "", nil, fmt.Sprintf("options 要有 %d～%d 个选项。不用写“其他”，所有者可以直接打字。", minOptions, maxOptions)
	}
	seen := map[string]bool{}
	for _, v := range raw.Arr {
		o, msg := parseOption(v)
		if msg != "" {
			return "", nil, msg
		}
		if seen[o.Label] {
			return "", nil, "选项不能重复。"
		}
		seen[o.Label] = true
		options = append(options, o)
	}
	return text, options, ""
}

func parseOption(v jsjson.Value) (Option, string) {
	var label, detail jsjson.Value
	switch v.Kind {
	case jsjson.String: // 弱模型常把选项直接写成字符串
		label = v
	case jsjson.Object:
		label, detail = v.Obj["label"], v.Obj["detail"]
	}
	if label.Kind != jsjson.String {
		return Option{}, "每个选项要有 label。"
	}
	o := Option{Label: strings.TrimSpace(label.Str)}
	switch {
	case o.Label == "":
		return o, "选项的 label 不能是空的。"
	case utf8.RuneCountInString(o.Label) > maxLabel:
		return o, fmt.Sprintf("选项的 label 太长了，最多 %d 字；说明写进 detail。", maxLabel)
	case !cleanText(o.Label, false):
		return o, "选项的 label 只能一行，不能有控制字符。"
	}
	if detail.Kind == jsjson.Null { // 没写（零值也是 Null）
		return o, ""
	}
	if detail.Kind != jsjson.String {
		return o, "选项的 detail 要是一段文字。"
	}
	o.Detail = strings.TrimSpace(detail.Str)
	switch {
	case utf8.RuneCountInString(o.Detail) > maxDetail:
		return o, fmt.Sprintf("选项的 detail 太长了，最多 %d 字。", maxDetail)
	case !cleanText(o.Detail, false):
		return o, "选项的 detail 只能一行，不能有控制字符。"
	}
	return o, ""
}

// cleanText 是合法 UTF-8、没有控制字符（newline 为真时换行可以）
func cleanText(s string, newline bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '\n' && newline {
			continue
		}
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
