package selfcheck

import (
	"context"
	"time"
)

// EvalNow 只评估指定 ID 的检查：按 Thresholds 拼出同 Runner 用的一批检查，选中的就地算一遍，按 ID 返回。
// 不写文件、不碰 Runner 的状态，也不改 KV；调用方自己保证不和 Runner 同时调同一条带状态的检查。
// 环境自检用它取 S9～S13 的结果（规格-环境自检 设计第 6 条），所以这里只做“算一条检查”这件事。
func EvalNow(ctx context.Context, env *Env, t Thresholds, ids ...string) map[string]Result {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make(map[string]Result, len(ids))
	for _, c := range Checks(t) {
		if !want[c.ID()] {
			continue
		}
		to := CheckTimeout
		if x, ok := c.(interface{ Timeout() time.Duration }); ok {
			to = x.Timeout()
		}
		cctx, cancel := context.WithTimeout(ctx, to)
		res := safeEval(cctx, c, env)
		if cctx.Err() != nil && res.State != Skip {
			res = Result{State: Unknown, Note: "评估超时或被打断"}
		}
		cancel()
		out[c.ID()] = res
	}
	return out
}
