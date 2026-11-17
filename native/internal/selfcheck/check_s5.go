package selfcheck

import (
	"context"
	"strconv"
	"time"
)

// reconnectStorm 是 S5 channel.reconnect_storm：窗口内协议端断开次数达到阈值。
// 一次断线里的多次退避只算一次（适配器那边按"连接真的断了"和"从在线变成掉线"记，不按重试次数记）。
type reconnectStorm struct {
	count  int
	window time.Duration
}

func (reconnectStorm) ID() string   { return "channel.reconnect_storm" }
func (reconnectStorm) Name() string { return "协议端反复断开重连" }
func (c reconnectStorm) Eval(_ context.Context, e *Env) Result {
	n := int64(e.Live.DisconnectsSince(e.Now.Add(-c.window)))
	r := Result{State: Ok, Value: n, Threshold: int64(c.count)}
	if n >= int64(c.count) {
		r.State = Bad
		r.Example = map[string]string{"window": c.window.String(), "total": strconv.FormatInt(e.Live.Get(Reconnects), 10)}
	}
	return r
}
