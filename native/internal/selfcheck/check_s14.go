package selfcheck

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

// StoreUnavailable 是一个打不开、已经整个停掉的平台库（App 从 store.Set.Unavailable 转过来；自检包不 import store）。
type StoreUnavailable struct {
	Platform string
	Track    string
	Reason   string // 固定词：too_new、unknown_schema、probe_failed、create_failed、migrate_failed、restore_failed、no_space、snapshot_failed
	Detail   string // 给人看的说明
	Snapshot string // 这次迁移前的快照（有就给）
}

// storeUnavailable 是 S14 store.unavailable：有平台库打不开，这个平台整个停着（不收、不发、不补录、通道不起），
// 自己不会好，要主人处理。判定：没接 → unknown；没有 → ok；否则 bad，例子里放平台、轨、固定原因词和快照路径。
// 解除办法写在备注里：换上能读这个库的 Sumika 或修好问题后重启，启动时会自动再试；sumika-ctl db status 看详情。
type storeUnavailable struct{}

func (storeUnavailable) ID() string   { return "store.unavailable" }
func (storeUnavailable) Name() string { return "有平台库没打开、这个平台停着" }

func (storeUnavailable) Eval(_ context.Context, e *Env) Result {
	r := Result{Threshold: 1}
	if e.StoreUnavailable == nil {
		r.State, r.Note = Unknown, "没有接平台库状态"
		return r
	}
	list := e.StoreUnavailable()
	if len(list) == 0 {
		r.State = Ok
		return r
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Platform != list[j].Platform {
			return list[i].Platform < list[j].Platform
		}
		return list[i].Track < list[j].Track
	})
	names := make([]string, 0, len(list))
	for _, u := range list {
		names = append(names, u.Platform)
	}
	first := list[0]
	r.State, r.Value = Bad, int64(len(list))
	r.Example = map[string]string{"platforms": strings.Join(names, ","), "platform": first.Platform}
	if first.Track != "" {
		r.Example["track"] = first.Track
	}
	if first.Reason != "" {
		r.Example["reason"] = first.Reason
	}
	if first.Snapshot != "" {
		r.Example["snapshot"] = first.Snapshot
	}
	r.Example["count"] = strconv.Itoa(len(list))
	r.Note = "有平台库打不开，这个平台已经整个停掉，别的平台照常"
	r.Next = "换上能读这个库的 Sumika，或修好问题后重启 Sumika，启动时会自动再试；sumika-ctl db status 看详情"
	return r
}
