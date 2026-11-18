package kernel

import "context"

// Activity 在真实执行及受管回调退出后结束；对模型返回超时不代表退出。
type Activity interface {
	End(error)
}

// Lifetime 的 Begin 和 Seal 必须共用准入锁，避免封入口后新增活动。
// Drain 等真实退出，不把取消信号或前台结局当成排空。
type Lifetime interface {
	Begin(context.Context, Invocation) (context.Context, Activity, error)
	Seal()
	Drain(context.Context) error
}
