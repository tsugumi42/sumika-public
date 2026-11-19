package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"sumika-native/internal/llm/chat"
	"sumika-native/internal/modelref"
)

// Router 按模型引用里的服务商 ID 把请求交给那家的 Client（规格-模型配置 3.6）。
// 请求的 Model 是规范引用 `服务商ID/模型ID`（裸字符串按 defaultProvider 解析，同 modelref），
// 发出去的 model 字段是不带服务商前缀的模型 ID：只有一个服务商时请求的字节和直接用 Client 一样。
// 找不到那家的客户端（没配、没接上）或那家已停用，报本地错误，不拿别家顶替。
type Router struct {
	refs     modelref.Resolver
	clients  map[string]*Client
	disabled map[string]bool
}

// NewRouter 建路由。clients 是能发请求的服务商（键是服务商 ID）；disabled 是已声明但停用的服务商 ID，
// 引用到它们时报"已停用"而不是"没有客户端"
func NewRouter(refs modelref.Resolver, clients map[string]*Client, disabled ...string) *Router {
	r := &Router{refs: refs, clients: make(map[string]*Client, len(clients)), disabled: make(map[string]bool, len(disabled))}
	for id, c := range clients {
		r.clients[id] = c
	}
	for _, id := range disabled {
		r.disabled[id] = true
	}
	return r
}

// RefsProvider 是带着引用解析器的传输层（Router 是）：modelcall、turn 从它取同一份解析器，
// 路由、冷却、trace 对"哪个引用是哪家的哪个模型"不会各有各的理解
type RefsProvider interface{ Refs() modelref.Resolver }

// RefsOf 取传输层的引用解析器；传输层不带（直接用 Client、测试的假传输）就是零值，模型串原样放行
func RefsOf(transport any) modelref.Resolver {
	if p, ok := transport.(RefsProvider); ok {
		return p.Refs()
	}
	return modelref.Resolver{}
}

// Refs 是路由用的引用解析器（和装配处给 turn、modelcall 的是同一份）
func (r *Router) Refs() modelref.Resolver { return r.refs }

// Client 取某个服务商的客户端
func (r *Router) Client(provider string) (*Client, bool) {
	c, ok := r.clients[provider]
	return c, ok
}

// pick 按引用定客户端和不带前缀的模型 ID
func (r *Router) pick(model string) (*Client, string, error) {
	ref, err := r.refs.Parse(model)
	if err != nil {
		return nil, "", err
	}
	if r.disabled[ref.Provider] {
		return nil, "", fmt.Errorf("服务商 %s 已停用（模型 %s）：到设置里启用它，或给这件事换一个模型", ref.Provider, ref.Model)
	}
	c, ok := r.clients[ref.Provider]
	if !ok {
		have := make([]string, 0, len(r.clients))
		for id := range r.clients {
			have = append(have, id)
		}
		sort.Strings(have)
		return nil, "", fmt.Errorf("服务商 %q 还没有可用的连接（模型 %s；现在能用的：%s）", ref.Provider, ref.Model, strings.Join(have, "、"))
	}
	return c, ref.Model, nil
}

// StreamChat 发请求：定服务商、把 model 换成不带前缀的 ID、交给那家的 Client
func (r *Router) StreamChat(ctx context.Context, req chat.Request, onEvent func(Event)) (Result, error) {
	c, model, err := r.pick(req.Model)
	if err != nil {
		return Result{}, LocalError(err)
	}
	req.Model = model
	return c.StreamChat(ctx, req, onEvent)
}
