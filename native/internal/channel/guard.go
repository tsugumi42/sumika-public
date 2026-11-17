package channel

// 回合定了以后要用的几样外面的东西（规格-渠道中间件 第十节 10.1、第十五节第 3 步）。
//   - Guard：记忆包的 CheckOutbound（规格-记忆-v2 13.2.8），装配按角色接入；
//   - 出站记数（第 5 步）：客服腔、"作为 AI"、分点列举这类，只记数不拦（ADR-110 第 2 条、ADR-114）。分类器还没搬到 Go（Bun 版靠 lookbehind 的正则，
//     Go 的正则没有），这里只留接口和调用的位置，Config.Audit 是 nil 就不记。

import (
	"context"

	"sumika-native/internal/audience"
)

// Guard 是发送前的「有没有说漏不该让对方听见的事」检查（规格-记忆-v2 13.2.8）：返回拦下的记忆编号，空就是放行。
// 判的是改写以后要发出去的那段字（不是原文）；中间件把这一回合前面已经放行的句子接在前面一起交给它，
// 因为一条私事拆成两句，单看第二句不拦（13.2.8"判断的单位"）。
// 嵌入挂了、小模型挂了的退路（ADR-186 第 4 条）在守卫自己里面：嵌入挂了退回只比原文，小模型挂了按相似度单独判，都照常返回结果；
// 返回 error 是连退路也没判成（库出错、整体超时），中间件按「出错不放行」拦下这一句，reason 记「守卫没判成」。
// ctx 带着整体时限（10 秒）；守卫要守这个 ctx
type Guard interface {
	CheckOutbound(ctx context.Context, text string, a audience.Audience) (blocked []string, err error)
}

// GuardRouter 是守卫可以多实现的一个方法：它刚才那次判断走的是哪条路。中间件每句话判完问一次，路变了才调 Config.OnGuardRoute
// （切到退路时提醒主人一次、恢复时再提醒一次，不每句提醒；提醒本身等 native/internal/alert，渠道线第 6 步写）
type GuardRouter interface {
	GuardRoute() string
}

// 守卫走的路（GuardRouter.GuardRoute 的值）。空串按正常算
const (
	GuardRouteNormal    = "normal"     // 字面 + 嵌入相似度 + 小模型确认
	GuardRouteEmbedDown = "embed-down" // 嵌入挂了或超时：只比原文，没比中的照常发
	GuardRouteModelDown = "model-down" // 确认用的小模型挂了或超时：按相似度单独判，超过门槛的拦下
)

// AuditHit 是出站记数命中的一处：哪一类（self-ai、refusal、service、essay）、命中的那几个字（最多 40 字）
type AuditHit struct {
	Kind string
	Hit  string
}

// AuditRecord 是记数的一笔（规格 10.1 第 5 步）：哪个会话、哪一条、命中哪类；Dry 是试运行想说的（分开记）
type AuditRecord struct {
	Character string
	Conv      string
	EventSeq  int64
	Hit       AuditHit
	Dry       bool
}
