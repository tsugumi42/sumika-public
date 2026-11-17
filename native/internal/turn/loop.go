package turn

// 一轮里的几步（第 2 步 2b）：模型要调工具时跑完工具、把调用和结果接在后面再请求一次，直到它不再调工具。
// 照 pi-agent-core 的 agent-loop（一步一次请求、工具串行还是并行）、pi-ai openai-completions.js 的消息转换（convertMessages），
// 和 agent.ts onAgentEvent（每一步的 message_end：思考那一步、她的话、recordRun；工具开始 / 结束：steps）

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/history"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"

	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
	"sumika-native/internal/textclean"
	"sumika-native/internal/tools"
)

// Msg 是实例里的一条消息（pi agent 的消息）：system、user、assistant（这一轮模型回的，或者从历史重建的）、toolResult
type Msg struct {
	Role string
	// Text：user、toolResult 的字；assistant 的正文（清过句尾的那句，pi 的消息对象被 agent.ts 改在原地）
	Text      string
	Images    []llm.Image `json:"-"`
	imageSeqs []int64
	// HasText：assistant 有没有正文那一块（流里来过 content；清完是空也算有）——估算时块和块之间要加换行
	HasText  bool
	Thinking string
	// Replay 是思考的回放数据（线上字段名、结构化思考收在里面），内核只存和带回、不解析；
	// HasThinking：有一块思考（思考文字或者可回放的数据，pi 的思考块）——Sumika 估算时块和块之间的换行要算它
	Replay      chat.Replay
	HasThinking bool
	Calls       []Call
	CallID      string     // toolResult：对应哪个调用
	Stop        string     // assistant：出错、被停下的是 "error"、"aborted"（pi 的 stopReason），留在实例里、发请求时跳过
	Usage       *llm.Usage // assistant：这一步服务商报的用量（pi 下一步按它估）
	// 这条从哪来（规格-Go后台 8.1、8.3）：Kind 是 said（说出口的，库里有那个事件，Seq 是它）、context（每轮的参考信息）、
	// tool（工具结果）；Scope 是哪一档的回合加进来的。压缩摘要只从不带 context、不带高档 tool 的那份生成（A8），A13 靠它查
	Kind  string
	Seq   int64
	Scope string
	// Controlled 只由宿主设置，不从工具文字或模型 JSON 解码；只保证请求级精简（slim）保留正文。
	// 工作恢复保留调用配对；受控正文与回调不恢复，由来源按新 ContextID 重新接纳。
	Controlled     bool                    `json:"-"`
	ControlledText string                  `json:"-"`
	OnAccepted     func()                  `json:"-"`
	Acceptable     func() bool             `json:"-"`
	Disposition    tools.ResultDisposition `json:"-"`
	// Turn、Step：渠道回合里她说的那一步属于哪个回合、第几步。这一句不由内核写库，等中间件写完库用 Hub.Commit 换成库里那一行（ADR-184 第 3 条）
	Turn               string
	Step               int
	Ref                *kernel.ResultRef
	ToolName, TodoArgs string
	IsError, GroupEnd  bool
	MaterialID         string
	WorkStepID, ToolID string
}

// 实例里一条消息的来源（Msg.Kind）
const (
	KindSaid    = "said"
	KindContext = "context"
	KindTool    = "tool"
	KindSummary = "summary" // 压缩快照那条
	KindWork    = "work"
)

// Call 是一次工具调用：参数是解析好的（pi parseToolCallArguments），解析不了时 ArgsErr 写原因
type Call struct {
	ID, Name string
	Args     jsjson.Value
	ArgsErr  string
	ToolID   string
	GroupEnd bool
}

// parseArgs 照 json-parse.js parseToolCallArguments：空的当 {}；解析不了先修一下再解析（parseJsonWithRepair）；不是合法 JSON、不是对象都算错。
// 错误原文是 Go 的，和 JS 引擎的不一样（规格第七节 T9，给模型看的那一句开头照 pi）
func parseArgs(raw string) (jsjson.Value, string) {
	if textclean.Trim(raw) == "" {
		return jsjson.Value{Kind: jsjson.Object, Obj: map[string]jsjson.Value{}}, ""
	}
	v, err := jsjson.Parse(raw)
	if err != nil {
		// 修过和原来一样就照原来的错误报；修过不一样就用修过的再解析一次，错了报那一次的
		if fixed := repairJSON(raw); fixed != raw {
			v, err = jsjson.Parse(fixed)
		}
	}
	if err == nil && v.Kind != jsjson.Object {
		err = errors.New("Tool arguments must be a JSON object")
	}
	if err != nil {
		return jsjson.Value{Kind: jsjson.Object, Obj: map[string]jsjson.Value{}}, err.Error() + "\n\nReceived arguments:\n" + raw
	}
	return v, ""
}

// repairJSON 照 json-parse.js repairJson：只改字符串里面的——裸控制字符换成转义（\b \f \n \r \t，其余 \u00XX），
// 不合法的转义（\ 后面不是 " \ / b f n r t，也不是 \u 加四个十六进制）把反斜杠变成两个，结尾落单的反斜杠也是。
// JS 按 UTF-16 码元走、这里按 rune 走：要看的只有 ASCII 字符，结果一样
func repairJSON(s string) string {
	var b strings.Builder
	in := false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if !in {
			b.WriteRune(c)
			if c == '"' {
				in = true
			}
			continue
		}
		switch {
		case c == '"':
			b.WriteRune(c)
			in = false
		case c == '\\':
			if i+1 >= len(rs) {
				b.WriteString(`\\`)
				continue
			}
			next := rs[i+1]
			if next == 'u' && i+5 < len(rs) && isHex4(rs[i+2:i+6]) {
				b.WriteString(`\u` + string(rs[i+2:i+6]))
				i += 5
				continue
			}
			if strings.ContainsRune(`"\/bfnrt`, next) {
				b.WriteRune('\\')
				b.WriteRune(next)
				i++
				continue
			}
			b.WriteString(`\\`)
		case c <= 0x1f:
			switch c {
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				fmt.Fprintf(&b, `\u%04x`, c)
			}
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

func isHex4(rs []rune) bool {
	for _, r := range rs {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f' || 'A' <= r && r <= 'F') {
			return false
		}
	}
	return len(rs) == 4
}

// ───────────── 两套估算 ─────────────

func ceilDiv35(n int) int { return int(math.Ceil(float64(n) / 3.5)) }

// pi 估一条消息（estimate.js estimateMessageTokens）：系统消息另加工具声明；assistant 是正文 + 思考 + 每个调用的名字和参数 JSON
func (m Msg) piTokens(toolsJSON string) int {
	switch m.Role {
	case "system":
		return ceilDiv35(store.JSLen(m.Text)) + ceilDiv35(store.JSLen(toolsJSON))
	case "assistant":
		n := store.JSLen(m.Text) + store.JSLen(m.Thinking)
		for _, c := range m.Calls {
			n += store.JSLen(c.Name) + store.JSLen(jsjson.Stringify(c.Args))
		}
		return ceilDiv35(n)
	}
	return ceilDiv35(store.JSLen(m.Text) + 3000*len(m.Images))
}

func usageTotal(u *llm.Usage) int64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

// piEstimate（estimateContextTokens）：有一条带用量的 assistant 就按它最后那条报的总数，加上它后面的消息；没有就每条估了加起来
func piEstimate(msgs []Msg, toolsJSON string) int {
	last := -1
	for i, m := range msgs {
		if m.Role == "assistant" && m.Stop == "" && m.Usage != nil && usageTotal(m.Usage) > 0 {
			last = i
		}
	}
	n := 0
	if last >= 0 {
		n = int(usageTotal(msgs[last].Usage))
		for _, m := range msgs[last+1:] {
			n += m.piTokens(toolsJSON)
		}
		return n
	}
	for _, m := range msgs {
		n += m.piTokens(toolsJSON)
	}
	return n
}

// sumikaText 是 util/context.ts 估一条消息时拿的字：assistant 的各块（思考 → 空、正文、每个调用的参数 JSON）用换行连起来
func (m Msg) sumikaText() string {
	if m.Role != "assistant" {
		return m.Text
	}
	var parts []string
	if m.HasThinking {
		parts = append(parts, "")
	}
	if m.HasText {
		parts = append(parts, m.Text)
	}
	for _, c := range m.Calls {
		parts = append(parts, jsjson.Stringify(c.Args))
	}
	return strings.Join(parts, "\n")
}

// ───────────── 一次请求 ─────────────

// requestFor 是按现在的消息拼的这一次请求（pi buildParams）
func (b *Built) requestFor(msgs []Msg) chat.Request {
	toolsJSON := tools.PiJSON(b.Decls)
	// simple-options.js clampMaxTokensToContext：留 4096 的余量，至少 1
	maxTokens := min(b.MaxOut, max(1, b.Window-piEstimate(msgs, toolsJSON)-4096))
	decls := b.Decls
	if decls == nil {
		// 旧请求总带 tools（没有是 []），不能因为 nil 变成不发
		decls = []tools.Declaration{}
	}
	return chat.Request{
		Model: b.Model.ID, SystemRole: b.SystemRole, Messages: chatMessages(msgs), Tools: decls,
		MaxOutputTokens: int64(maxTokens), Thinking: b.Effort, CacheKey: b.CacheKey, LongCache: true, NoStore: true,
		SeeImages: b.Model.SupportsImages(), Style: chat.StyleForeground,
	}
}

// chatMessages 把实例里的消息转成中立对话（openai-completions.js convertMessages 的输入侧）：
// 停掉的 assistant 整条 Omit，图、工具调用、工具结果都按块放；思考只带 Replay，内核不解析
func chatMessages(msgs []Msg) []chat.Message {
	out := make([]chat.Message, 0, len(msgs))
	for _, m := range msgs {
		var role chat.Role
		switch m.Role {
		case "system":
			role = chat.RoleSystem
		case "user":
			role = chat.RoleUser
		case "assistant":
			role = chat.RoleAssistant
		case "toolResult":
			role = chat.RoleTool
		default:
			continue
		}
		msg := chat.Message{Role: role, Omit: m.Role == "assistant" && m.Stop != ""}
		switch m.Role {
		case "system":
			msg.Blocks = append(msg.Blocks, chat.Text(m.Text))
		case "user":
			msg.Blocks = append(msg.Blocks, chat.Text(m.Text))
			for _, image := range m.Images {
				msg.Blocks = append(msg.Blocks, chat.Image{DataURL: image.DataURL})
			}
		case "assistant":
			msg.Blocks = append(msg.Blocks, chat.Text(m.Text))
			if m.HasThinking || m.Thinking != "" || len(m.Replay.Data) > 0 {
				msg.Blocks = append(msg.Blocks, chat.Thinking{Text: m.Thinking, Replay: m.Replay})
			}
			for _, c := range m.Calls {
				msg.Blocks = append(msg.Blocks, chat.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Args})
			}
		case "toolResult":
			images := make([]chat.Image, len(m.Images))
			for i, image := range m.Images {
				images[i] = chat.Image{DataURL: image.DataURL}
			}
			msg.Blocks = append(msg.Blocks, chat.ToolResult{CallID: m.CallID, Text: m.Text, IsError: m.IsError, Images: images})
		default:
			continue
		}
		out = append(out, msg)
	}
	return out
}

// contextFor 是这一次请求的 model_runs.context_json（agent.ts recordRun 的 context + util/context.ts contextBreakdown）：
// estimate = 固定前缀 + 实例里消息的 Sumika 估算（精简之前的）；soft / hard 四舍五入；slimmed 是这一次精简了几个旧工具结果；
// reset 是裁剪时留了几条、丢了几条（只在实例重建后的第一步报）；breakdown 按真正发出去的（sent）按类拆，
// 这一轮追加的参考信息（block）算 turn，以前几轮的算 context
func (b *Built) contextFor(msgs, sent []Msg, block string, slimmed int, firstStep bool) string {
	type toolItem struct {
		Name   string `json:"name"`
		Tokens int    `json:"tokens"`
	}
	type histItem struct {
		Kind   string `json:"kind"`
		Count  int    `json:"count"`
		Tokens int    `json:"tokens"`
	}
	sec := func(test func(id string) bool) int {
		n := 0
		for _, s := range b.Sections {
			if test(s.ID) {
				n += sumikaTokens(s.Text)
			}
		}
		return n
	}
	isPersona := func(id string) bool { return strings.HasPrefix(id, "persona") }
	isScene := func(id string) bool { return strings.HasPrefix(id, "scene-") || strings.HasPrefix(id, "platform-") }
	toolItems := make([]toolItem, len(b.Decls))
	toolTokens := 0
	for i, d := range b.Decls {
		toolItems[i] = toolItem{d.Name, sumikaTokens(tools.Item(d))}
		toolTokens += toolItems[i].Tokens
	}
	sort.SliceStable(toolItems, func(i, j int) bool { return toolItems[i].Tokens > toolItems[j].Tokens })
	order := []string{"summary", "user", "assistant", "toolResult", "context"}
	counts, toks := map[string]int{}, map[string]int{}
	turn := 0
	msgTokens := conversationTokens(msgs)
	for _, m := range sent {
		if m.Role == "system" {
			continue
		}
		text := m.sumikaText()
		n := sumikaMessages(text) + imageTokens(m)
		switch {
		case m.Role == "user" && strings.HasPrefix(text, history.SummaryHeader):
			counts["summary"]++
			toks["summary"] += n
		case m.Role == "user" && strings.HasPrefix(text, ContextHeader):
			if block != "" && text == block {
				turn += n
				continue
			}
			counts["context"]++
			toks["context"] += n
		default:
			counts[m.Role]++
			toks[m.Role] += n
		}
	}
	items := []histItem{}
	histTokens := 0
	for _, k := range order {
		if counts[k] > 0 {
			items = append(items, histItem{k, counts[k], toks[k]})
			histTokens += toks[k]
		}
	}
	core, per, scene := sec(func(id string) bool { return !isPersona(id) && !isScene(id) }), sec(isPersona), sec(isScene)
	type resetT struct {
		Kept    int `json:"kept"`
		Dropped int `json:"dropped"`
	}
	var rs *resetT
	if b.Reset && firstStep {
		rs = &resetT{b.Kept, b.Dropped}
	}
	out := map[string]any{
		"estimate": b.Fixed + msgTokens,
		"soft":     int(math.Round(b.Soft)),
		"hard":     int(math.Round(b.Hard)),
		"slimmed":  slimmed,
		"reset":    rs,
		"breakdown": map[string]any{
			"window": b.Window, "reserve": b.Reserve, "compactAt": int(math.Round(b.Hard)), "compactKind": b.CompactKind,
			"estimate": core + per + scene + toolTokens + histTokens + turn,
			"parts": map[string]any{
				"core": core, "persona": per, "scene": scene,
				"tools":   map[string]any{"tokens": toolTokens, "items": toolItems},
				"history": map[string]any{"tokens": histTokens, "items": items},
				"turn":    turn,
			},
		},
	}
	// 重建留痕（规格-提示词前缀稳定第八节）：只在重建后的第一步写，不是第一步整个省掉、不写 null：
	// AddRun 冲突时按 json_patch 合并，null 会把已经记下的删掉。没经过 buildWith 的 Built（Count 为空）也不写
	if firstStep && b.Trace.Count != "" {
		tr := b.Trace
		if tr.Reason == "" {
			tr.Reason = RebuildUnknown
		}
		out["rebuild"] = tr
	}
	// 这一次请求里真的带了几张图（用户消息自带的、read 读来的）；没带图不写，记录保持原样
	userImages, toolImages := 0, 0
	for _, m := range sent {
		if m.Role == "user" {
			userImages += len(m.Images)
		} else if m.Role == "toolResult" && b.Model.SupportsImages() {
			toolImages += len(m.Images)
		}
	}
	if userImages+toolImages > 0 {
		out["images"] = map[string]int{"user": userImages, "tool": toolImages}
	}
	j, _ := store.JSString(out)
	return j
}

// ───────────── 一轮 ─────────────

// Result 是一轮的结果
type Result struct {
	Built           *Built
	TurnID          string
	Reply           string       // 最后一句存进库的话（这一轮没说就是空）
	Steps           []llm.Result // 每一步模型调用的结果
	LLM             llm.Result   // 第一步的（turn-check 拿它看缓存）
	Stopped         bool         // 说到一半被停下、被打断了
	BudgetExhausted bool
	Said            []Said        // 每一步说出口的那句（随"回合定了"交出）
	Tools           []ToolSummary // 这一轮调过的工具和结果摘要（宿主的 /goal 裁判要看）
}

// ToolSummary 是一次工具调用的摘要：名字、成没成、结果的前一小段
type ToolSummary struct {
	Name   string
	OK     bool
	Result string
}

// 一条工具结果摘要最多留多少个字
const toolSummaryRunes = 400

// Run 跑一轮：主人在主窗口对角色 characterID 说了 text。每次新建一个 Hub，所以总是冷启动（turn-check、第 1 步的对拍用）。
// 回完就收掉这个 Hub：后台要是起了提前压缩，取消它、等它收尾（不写库），调用方接着关库也碰不到它。
// stores 至少要有本地库（主窗口只用它，store.OpenGo(p, "local")）
func Run(ctx context.Context, cfg Config, stores store.Stores, client *llm.Client, models []llm.Model, characterID, text string) (*Result, error) {
	hctx, cancel := context.WithCancel(ctx)
	hub := NewHub(hctx, cfg, stores, client, models)
	o := <-hub.Send(characterID, text)
	cancel()
	hub.Idle()
	return o.Result, o.Err
}

// 停在工具上以后下一次请求的错误原文（Bun 版记的就是这句，规格第七节 A36）
var errAbortedBeforeRequest = errors.New("The operation was aborted.")

// endStep 是一步的 message_end（agent.ts onAgentEvent）：思考那一步 → 她的话（清过的，不是空才存，存了把这一轮还没挂上的步骤挂上去）→ recordRun
// aborted：说到一半被停下，存进去的话带 segments.aborted（agent.ts message_end）
// first：建好实例以后的第一步（记为什么重建）；block：这一轮追加的参考信息（没有就不记 memory 那一段）。
// 返回她这句话存进库的那条（没说是 0）。渠道的回合不存（ADR-184 第 3 条）：中间件裁决、改写以后自己写，返回 0
func (b *Built) endStep(cfg Config, st *store.Store, a Msg, res *llm.Result, turnID string, startedAt, firstText, firstThinking time.Time, ctxJSON string, errMsg *string, errClass string, aborted, first bool, block, replyOrigin string, out *Result) (int64, error) {
	conversation, characterID := b.Conversation, b.CharacterID
	if a.Thinking != "" {
		if err := st.StepStart(store.Step{ID: "think:" + store.NewID(cfg.Now()), CharacterID: characterID, Conversation: conversation, TurnID: turnID, Kind: "thinking", Status: "done",
			Preview: clip(a.Thinking, thinkingMax), StartedAt: firstThinking.UnixMilli()}); err != nil {
			return 0, err
		}
	}
	var seq int64
	if textclean.Trim(a.Text) != "" && b.Local {
		var segs any
		if aborted {
			segs = map[string]any{"aborted": true}
		}
		segs = selftestSegments(conversation, segs)
		var err error
		observers := b.Observers
		if len(observers) == 0 {
			observers = []string{characterID}
		}
		origin := "character"
		if replyOrigin != "" {
			origin = replyOrigin
		}
		seq, err = st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conversation, ActorKind: "character", ActorID: characterID, Origin: origin, Kind: "message", Text: a.Text, Segments: segs, CreatedAt: cfg.Now().UnixMilli()}, observers)
		if err != nil {
			return 0, err
		}
		if err := st.AttachSteps(turnID, seq); err != nil {
			return 0, err
		}
		out.Reply = a.Text
	}
	return seq, st.AddRun(b.runRow(cfg, res, turnID, startedAt, firstText, ctxJSON, errMsg, errClass, first, block))
}

// failedAttempt 记下一次出错、要重试的请求（规格 8.2 A7）：只记 model_runs 那一行（错误后面带上第几次、等多久），
// 不走 endStep——这一次的半句、思考、半截的工具调用都不要了，不存库、不记步骤。stage 记成 reply-retry：模型调用页
// 按岗位数得出重试了几次，也不进回复那条前缀复用的对比链（没有用量的一行夹在中间会被当成缓存掉了），
// 重建的标记照旧留在这一步真正回完的那一行；渠道连续失败照样算它
func (b *Built) failedAttempt(cfg Config, st *store.Store, res *llm.Result, turnID string, startedAt, firstText time.Time, ctxJSON, msg, errClass, block string) error {
	run := b.runRow(cfg, res, turnID, startedAt, firstText, ctxJSON, &msg, errClass, false, block)
	run.Stage = "reply-retry"
	return st.AddRun(run)
}

// runRow 是一次模型请求的 model_runs 那一行（agent.ts recordRun）
func (b *Built) runRow(cfg Config, res *llm.Result, turnID string, startedAt, firstText time.Time, ctxJSON string, errMsg *string, errClass string, first bool, block string) store.Run {
	conversation, characterID := b.Conversation, b.CharacterID
	now := cfg.Now()
	var ttft *int64
	if !firstText.IsZero() {
		v := firstText.Sub(startedAt).Milliseconds()
		ttft = &v
	}
	secs := make([]store.RunSection, 0, len(b.Sections)+1)
	for _, s := range b.Sections {
		secs = append(secs, store.RunSection{ID: s.ID, Source: s.Source, Text: s.Text})
	}
	if block != "" {
		secs = append(secs, store.RunSection{ID: "memory", Source: memorySource, Text: block})
	}
	// 没收到用量那一块时 pi 的 usage.reasoning 是空的（cacheWrite 一开始就是 0）
	cw, rt := res.Usage.CacheWrite, &res.Usage.Reasoning
	if !res.UsageSeen {
		rt = nil
	}
	run := store.Run{
		ID: store.NewID(now), CharacterID: characterID, Conversation: conversation, Stage: "reply", Model: b.Model.ID, TurnID: turnID,
		StartedAt: startedAt.UnixMilli(), TTFTMs: ttft, TotalMs: now.Sub(startedAt).Milliseconds(),
		Input: res.Usage.Input, Output: res.Usage.Output, CacheRead: res.Usage.CacheRead, CacheWrite: &cw, Reasoning: rt,
		RouteProvider: orNil(res.Route.Provider), RouteModel: orNil(res.Route.Model), Sections: secs, Error: errMsg, ErrorClass: errClass, Context: ctxJSON, Now: now.UnixMilli(),
		Identity: b.Invocation, UsageSeen: &res.UsageSeen, Incomplete: !res.UsageSeen, Prompt: res.Prompt,
	}
	// 实例新建以后的第一步记为什么重建（fresh / reset / compact：有了新快照），记完就清掉（agent.ts recordRun）
	if first {
		rebuild := "fresh"
		switch {
		case b.Reset:
			rebuild = "reset"
		case b.Compacted:
			rebuild = "compact"
		}
		run.Rebuild = &rebuild
		run.ResetReason = &rebuild
	}
	if b.EpochID != "" {
		run.EpochID = &b.EpochID
	}
	return run
}

func toolMessage(id string, r tools.Result) Msg {
	return Msg{Role: "toolResult", Text: r.Text, Images: r.Images, CallID: id, Ref: r.FullResult, IsError: r.IsError, Controlled: r.Controlled, ControlledText: r.ControlledText, OnAccepted: r.OnAccepted, Acceptable: r.Acceptable, Disposition: r.Disposition}
}

type toolOutcome struct {
	r   tools.Result
	err error
}

// 到点先收已经完成的结果（保留详情和错误），再等正在写入的 edit 发布成功状态。
func toolResultAtTimeout(out <-chan toolOutcome, committed *tools.EditCommit, cancel context.CancelFunc) (toolOutcome, bool) {
	select {
	case x := <-out:
		return x, true
	default:
	}
	if r, ok := committed.Finish(cancel); ok {
		return toolOutcome{r: r}, true
	}
	return toolOutcome{}, false
}

func known(decls []tools.Declaration, name string) bool {
	for _, d := range decls {
		if d.Name == name {
			return true
		}
	}
	return false
}

// steps.args_json 最多多少（store.ts STEP_ARGS_MAX）
const stepArgsMax = 4 * 1024
