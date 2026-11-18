package app

// 守卫走退路的提醒（ADR-186 第 4 条）：嵌入挂了只比原文、确认模型挂了只按相似度，消息照常判、照常发，
// 但要提醒用户一次，恢复了再提醒一次，不每句提醒。
//
// 路线记在 App 这一层、按角色分开，不放在 channel.Responder 里：回合、复读、引用 / 转发动作三处检查都走 guardFor，
// 包一层以后三处同时覆盖，也不用改渠道包和记忆包。

import (
	"context"
	"sync"

	"sumika-native/internal/alert"
	"sumika-native/internal/audience"
	"sumika-native/internal/channel"
)

// guardCore 是被包的守卫：*memory.Memory 的三个方法。单独成接口是为了单元层能换一个假的。
type guardCore interface {
	CheckOutbound(ctx context.Context, text string, a audience.Audience) ([]string, error)
	CheckRepeat(ctx context.Context, text string, a audience.Audience) ([]string, error)
	GuardRoute() string
}

// guardRoutes 记每个角色上一次的路线。零值可用；按角色 id 分开，一个角色的退路不影响另一个角色的提醒。
type guardRoutes struct {
	mu   sync.Mutex
	last map[string]string
}

func guardRouteAlertKey(id string) string { return "guard:" + id + ":route" }

// note 在路线变了的时候提醒：正常 → 退路 Raise；退路 → 正常 Clear；两条退路之间切换标题不同，Hub 会再发一次 Raised。
// 路线没变什么也不做，不靠 Hub 去重（同键同标题重复 Raise 会发 Updated，订阅者每句都收到）。
func (r *guardRoutes) note(hub *alert.Hub, id, cur string) {
	if cur == "" {
		cur = channel.GuardRouteNormal
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.last[id]
	if prev == "" {
		prev = channel.GuardRouteNormal
	}
	if prev == cur {
		return
	}
	if r.last == nil {
		r.last = map[string]string{}
	}
	r.last[id] = cur
	if hub == nil {
		return
	}
	key := guardRouteAlertKey(id)
	if cur == channel.GuardRouteNormal {
		hub.Clear(key, alert.Note{
			Title: "发送前检查恢复正常",
			Body:  "角色 " + id + " 的发送前检查重新用上了相似度和确认模型。",
		})
		return
	}
	title, body := "发送前检查走了退路", "角色 "+id+" 的发送前检查没有走完整的三步，退到了备用判断；换了说法的私事可能拦不住。消息仍照常发送。"
	switch cur {
	case channel.GuardRouteEmbedDown:
		title = "发送前检查暂时只比对原文"
		body = "角色 " + id + " 的嵌入这一次没用上（没装、没响应、出错，或向量维度对不上），发送前检查只比对了原文、没比相似度；换了说法的私事可能拦不住。消息仍照常发送，恢复以后会再提醒一次。"
	case channel.GuardRouteModelDown:
		title = "发送前检查的确认模型没用上"
		body = "角色 " + id + " 的发送前检查里，确认用的后台模型这一次没用上（没配，或没有回应），改为只按相似度判断：过了相似度门槛的话一律拦下，可能多拦几句正常的话。恢复以后会再提醒一次。"
	}
	hub.Raise(alert.Alert{Key: key, Class: "guard", Title: title, Body: body})
}

// routedGuard 包在一个角色的记忆守卫外面，判完读一次路线交给 guardRoutes。
// 同时满足 channel.Guard、repeat.Guard、repeat.RepeatGuard、channel.GuardRouter：Responder 的"守卫走的是 X"日志和复读的免确认检查照旧。
type routedGuard struct {
	id     string
	core   guardCore
	routes *guardRoutes
	hub    *alert.Hub
}

func (g *routedGuard) CheckOutbound(ctx context.Context, text string, a audience.Audience) ([]string, error) {
	refs, err := g.core.CheckOutbound(ctx, text, a)
	g.observe(refs, err)
	return refs, err
}

func (g *routedGuard) CheckRepeat(ctx context.Context, text string, a audience.Audience) ([]string, error) {
	refs, err := g.core.CheckRepeat(ctx, text, a)
	g.observe(refs, err)
	return refs, err
}

func (g *routedGuard) GuardRoute() string { return g.core.GuardRoute() }

// observe 读这次判断走的路。出错的那次、以及字面命中拦下的那次，记忆里存的都是"正常"，其实没走到嵌入，
// 不能当作"恢复了"：这两种不更新路线，等下一次真正走了嵌入的判断。
func (g *routedGuard) observe(refs []string, err error) {
	if err != nil {
		return
	}
	cur := g.core.GuardRoute()
	if (cur == "" || cur == channel.GuardRouteNormal) && len(refs) > 0 {
		return
	}
	g.routes.note(g.hub, g.id, cur)
}
