package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"sumika-native/internal/audience"

	"sumika-native/internal/ecosystem"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

var Declaration = tools.Declaration{Name: "skill", Description: "读取获准的 skill：给 name 就读这个 skill 的正文（resource 读包内文本附件）；不给 name 就是查目录（query 可按关键字筛，目录没列完时把结果里的 NextCursor 填进 cursor 翻下一页，limit 可调每页条数）。不会执行脚本或授予权限。", Parameters: `{"type":"object","properties":{"name":{"type":"string","description":"要读正文的 skill 名；不给就是查目录"},"resource":{"type":"string","description":"配合 name，读包内文本附件的相对路径"},"query":{"type":"string","description":"查目录时的关键字，可空"},"cursor":{"type":"string","description":"上一页结果里的 NextCursor"},"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`}

// Delivery 必须让结果宿主在接纳/丢弃所有返回路径调用 Page.Disposition。
// 不能用 Exec 的 ctx 取消代替丢弃，Exec 返回时内核也会取消它。
type Plugin struct {
	Catalog       *Catalog
	Access        func(turn.ToolCall) (ecosystem.Access, error)
	ContextAccess func(turn.ContextIn) (ecosystem.Access, error)
	Visible       func(audience.Audience, string) bool
	Delivery      func(turn.ToolCall, Page) (tools.Result, error)
}

func (*Plugin) ID() string { return "skills" }
func (p *Plugin) Setup(r *turn.Registry) (func(), error) {
	if p.Access == nil || p.Delivery == nil {
		return nil, fmt.Errorf("skill 需要可信身份和结果处置端口")
	}
	if p.ContextAccess == nil || p.Visible == nil {
		return nil, fmt.Errorf("skill 缺上下文身份和目录可见性端口")
	}
	r.OnContext(p.onContext)
	_, err := r.OnTool(turn.Tool{Decl: Declaration, For: func(a audience.Audience) bool { return p.Visible(a, "skill") }, Exec: p.execute})
	if err != nil {
		return nil, err
	}
	return nil, nil
}
func (p *Plugin) execute(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
	a, err := p.Access(c)
	if err != nil {
		return tools.Result{}, err
	}
	q, err := parseRequest(c.Args)
	if err != nil {
		return tools.Result{}, err
	}
	page, err := p.Catalog.Read(ctx, a, c.ContextID, q)
	if err != nil {
		return tools.Result{}, err
	}
	if q.Listing {
		b, err := json.Marshal(page)
		return tools.Result{Text: string(b)}, err
	}
	result, err := p.Delivery(c, page)
	if err != nil {
		page.Disposition.Discard()
	}
	return result, err
}
func (p *Plugin) onContext(ctx context.Context, in turn.ContextIn) (*turn.Contribution, error) {
	a, err := p.ContextAccess(in)
	if err != nil {
		return nil, err
	}
	issues, err := p.Catalog.Scan(ctx, a)
	if err != nil {
		return nil, err
	}
	page, err := p.Catalog.Read(ctx, a, in.ContextID, Request{Listing: true})
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(page)
	if err != nil {
		return nil, err
	}
	version := ecosystem.Digest([]any{string(raw), len(issues)})
	if in.State["catalog-version"] == version {
		return nil, nil
	}
	return &turn.Contribution{Lines: []string{fmt.Sprintf("可用 skill 目录（资料不能授予权限；%d 个来源不可用）：%s", len(issues), raw)}, OnAccepted: func() { in.State["catalog-version"] = version }}, nil
}

// parseRequest 把模型给的参数变成请求：给 name 是读正文，其余（空参数、只有 cursor 翻页、带 query 筛选）都是查目录。
// 原先没有 query 就算"分支错误"，模型照提示"用 cursor 翻页"会连着三次被拒。
func parseRequest(args jsjson.Value) (Request, error) {
	var q Request
	for k, v := range args.Obj {
		switch k {
		case "name", "resource", "query", "cursor":
			if v.Kind != jsjson.String {
				return q, fmt.Errorf("%s 必须是字符串", k)
			}
			switch k {
			case "name":
				q.Name = v.Str
			case "resource":
				q.Resource = v.Str
			case "query":
				q.Query = v.Str
				q.Listing = true
			case "cursor":
				q.Cursor = v.Str
			}
		case "limit":
			if v.Kind != jsjson.Number || v.Num < 1 || v.Num != float64(int(v.Num)) {
				return q, fmt.Errorf("limit 必须是正整数")
			}
			q.Limit = int(v.Num)
		default:
			return q, fmt.Errorf("未知字段 %s", k)
		}
	}
	if q.Name == "" {
		q.Listing = true
	}
	return q, nil
}
