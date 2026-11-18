package approval

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
)

// MCPApprovalTarget 是解析、冻结好的一次 MCP 调用。
type MCPApprovalTarget interface {
	Valid() bool
	Target() (string, string, string, string)
	Arguments() json.RawMessage
	Binding() string
	Fingerprint() string
	ReadOnlyClaim() bool
}

// CheckMCP 判定冻结目标的一次调用：和别的工具同一套顺序（场合、不是主人发起的、授权、范围档）。
// 同一类是服务器加原始工具名；规则认的目标版本是服务连接指纹（跨重启稳定，不是进程内的 Binding）加参数 schema，服务换了、参数定义变了都要再问（规格-审批分级第七节）。
// 服务器自称只读只写在卡片上，不拿来放宽：它是对方自己说的（规格-MCP客户端 2.4）。
// 批准之后目标变了不能拿旧批准换目标，也没有可以再问的同一个目标，所以告诉模型重新搜索，下一次调用会重新问。
func (g *Gate) CheckMCP(ctx context.Context, character, conversation, toolID string, target MCPApprovalTarget) Clearance {
	if !target.Valid() {
		return Clearance{Blocked: true, Reason: whyMCPStale}
	}
	server, raw, public, digest := target.Target()
	in := kernel.InvocationFromContext(ctx)
	s := in.Scope
	group := strings.Contains(s.Conversation, ":group:") || strings.Contains(conversation, ":group:")
	aud := policy.Audience{Channel: s.Channel, PrivateOk: s.ActorKind == "owner" && s.Scene == "private" && !group}
	var kind *policy.Kind
	guard := target.Fingerprint() + ":" + digest
	granted, hadOlder := "", false
	if conversation != "" {
		kind = &policy.Kind{Key: "mcp:" + server + "/" + raw, Label: "「" + server + " 的 " + raw + "」"}
		_, hasConv := g.Conv.Match(conversation, "", kind.Key, guard)
		switch {
		case hasConv:
			granted = policy.ByConversation
		case g.Grants.Match(kind.Key, guard):
			granted = policy.ByAlways
		default:
			hadOlder = g.Conv.Has(conversation, kind.Key) || g.Grants.Has(kind.Key)
		}
	}
	spec := policy.Specs["mcp_call"]
	v := policy.Decide("mcp_call", spec, jsjson.Value{Kind: jsjson.Object}, policy.Env{Mode: g.ModeFor(conversation), Audience: aud, Declared: true, Granted: granted, NotOwner: NotOwner(s, conversation), Rules: g.userRules(), Conversation: conversation, Ancestors: g.ancestors(conversation), MCPServer: server, MCPTool: raw})
	if v.Kind == "ask" && v.RuleKey != "" {
		g.Grants.Touch(v.RuleKey, g.Now().UnixMilli())
	}
	now := g.Now()
	minutes := g.ApprovalMinutes
	if minutes <= 0 {
		minutes = 10
	}
	req := Request{Identity: in, ID: toolID, CharacterID: character, Conversation: conversation, ToolCallID: toolID, Tool: "mcp_call", Title: "调用 MCP 工具", Subject: strings.Join([]string{server, raw, public}, " / "), Preview: &Preview{Kind: "mcp", Text: "schema=" + digest + "\n" + string(target.Arguments())}, Why: v.Reason, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Duration(minutes * float64(time.Minute))).UnixMilli()}
	switch v.Kind {
	case "deny":
		return Clearance{Blocked: true, Reason: v.Reason}
	case "allow":
		cl := g.allowed(v, kind, guard, "", conversation)
		cl.Traced = true
		if g.OnAuto != nil {
			g.OnAuto(req, cl)
		}
		return cl
	}
	if g.Ask == nil {
		return Clearance{Blocked: true, Reason: whyCancelled}
	}
	if target.ReadOnlyClaim() {
		req.Notes = append(req.Notes, "服务器自称这个工具只读（没有核实）")
	}
	if hadOlder {
		req.Notes = append(req.Notes, "这个工具的服务或参数定义变了，之前的放行不算了，再看一眼")
	}
	var pending *Pending
	if kind != nil && v.Limit != "once" {
		req.Options, req.KindLabel = []string{policy.ByConversation, policy.ByAlways}, kind.Label
		pending = &Pending{Key: kind.Key, Label: kind.Label, Tool: "mcp_call", Conversation: conversation, Guard: guard}
	}
	d := g.Ask(ctx, req, pending)
	if d.Kind != "approved" {
		return blockedBy(d)
	}
	if ctx.Err() != nil {
		return blockedBy(Decision{Kind: "cancelled"})
	}
	if !target.Valid() {
		return Clearance{Blocked: true, Reason: whyMCPStale}
	}
	return g.answered(d, kind, guard, "", conversation)
}
