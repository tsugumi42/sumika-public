package channel

// 计划号 → 它是为哪条源消息发的（Part.SourceSeq）。
//
// 库里没有这个：outbound_plans 只有计划自己那行事件的 seq，reply_to 只在首段是转发或带引用时才写，不能当依据。
// 链接插件撤回“补发的那份”时，宿主要独立核对两段属于同一条源消息，所以出口在入队时顺手记一份，只在内存里、带期限：
// 记不到（进程重启过、过期了）宿主就拒绝撤回；插件的看守本来也只在内存里，重启后一起丢（规格-链接发送改版 B15 已知的坑）。
// 不加库列：加列要占迁移号，而这份信息只有撤回这一道门要用，丢了的后果是少撤一次（群里多一份），不是撤错。

import (
	"sync"
	"time"

	"sumika-native/internal/channel/types"
)

const (
	// planSourceTTL 比看守的 5 分钟、回声认领的窗口都长：撤回补发份时，原段那一份可能是几分钟前发的
	planSourceTTL = 15 * time.Minute
	planSourceMax = 2048
)

type planSource struct {
	seq   int64
	until time.Time
}

type planSources struct {
	mu sync.Mutex
	m  map[string]planSource
}

// sourceSeqOf 是这批段共同的源消息 seq；有的段没写、或几段写的不一样都返回 0（不记）
func sourceSeqOf(parts []types.Part) int64 {
	var seq int64
	for i, p := range parts {
		if i == 0 {
			seq = p.SourceSeq
		} else if p.SourceSeq != seq {
			return 0
		}
	}
	return seq
}

func (b *planSources) put(planID string, seq int64, now time.Time) {
	if seq <= 0 || planID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]planSource{}
	}
	for k, v := range b.m {
		if !now.Before(v.until) {
			delete(b.m, k)
		}
	}
	if len(b.m) >= planSourceMax {
		var oldest string
		for k, v := range b.m {
			if oldest == "" || v.until.Before(b.m[oldest].until) {
				oldest = k
			}
		}
		delete(b.m, oldest)
	}
	b.m[planID] = planSource{seq: seq, until: now.Add(planSourceTTL)}
}

// PlanSource 返回这条计划是为哪条源消息（本地 seq）发的；没记、过期了、几段的源不一致都返回 false
func (o *Outlet) PlanSource(planID string) (int64, bool) {
	o.sources.mu.Lock()
	defer o.sources.mu.Unlock()
	v, ok := o.sources.m[planID]
	if !ok || !o.clock.Now().Before(v.until) {
		return 0, false
	}
	return v.seq, true
}
