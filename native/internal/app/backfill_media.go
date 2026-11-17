package app

import (
	"context"
	"sync"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/forwardfetch"
	"sumika-native/internal/mediafetch"
	"sumika-native/internal/store"
)

type backfillMedia struct {
	media    *mediafetch.Fetcher
	forwards *forwardfetch.Fetcher
	wg       sync.WaitGroup
}

func (b *qqBackfill) resweepMedia() {
	if b.media.media != nil {
		b.media.media.Resweep()
	}
	if b.media.forwards != nil {
		b.media.forwards.Resweep()
	}
}

func (b *qqBackfill) beginMedia(ctx context.Context, j api.BackfillJob) error {
	after, err := b.st.BackfillEventHighWater(ctx)
	if err != nil {
		return err
	}
	changes := b.st.SubscribeChanges()
	since := b.clock.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	allowed := b.allowed
	if j.Scope == "gap" {
		since = 1 << 62
		allowed = func(conv string) bool { return conv == j.Gap.ConversationKey.Conversation && b.allowed(conv) }
	} else if j.Trigger == "manual" {
		// 连接触发已在 Ready 后补扫；手动账号轮次才另发一次请求。
		b.resweepMedia()
	}
	b.media.wg.Add(1)
	go func() {
		defer b.media.wg.Done()
		defer changes.Close()
		var previous api.AlignmentMedia
		for {
			changes.Take()
			b.tasks.Lock()
			current, have := b.tasks.jobs[j.ID]
			through, endedRange := b.tasks.mediaEndSeq[j.ID]
			if !endedRange {
				through = 1<<63 - 1
			}
			b.tasks.Unlock()
			if !have {
				return
			}
			targets, err := b.st.AlignmentMediaTargets(ctx, b.account, since, after, through, allowed)
			if err != nil {
				if ctx.Err() == nil {
					b.logf("QQ 对齐媒体统计失败：%v", err)
				}
				return
			}
			var activity mediafetch.BackfillActivity
			if b.media.media != nil {
				activity = b.media.media.BackfillActivity()
			}
			var forwarding map[string]bool
			sweeping := false
			if b.media.forwards != nil {
				forwarding, sweeping = b.media.forwards.BackfillActivity()
			}
			v := api.AlignmentMedia{Scope: "connection-7d+round-new", State: "completed"}
			if j.Scope == "gap" {
				v.Scope = "gap-new"
			}
			for _, t := range targets {
				switch {
				case t.Kind == "forward":
					if b.media.forwards == nil {
						v.Skipped++
					} else if forwarding[t.Forward] {
						v.Running++
					} else {
						v.Unchecked++
					}
				case t.Hash != "" || t.State == store.MediaDone:
					v.Downloaded++
				case b.media.media == nil || t.State == "skipped":
					v.Skipped++
				case t.State == store.MediaExpired:
					v.Expired++
				case t.State == store.MediaManual || t.State == store.MediaTooBig && (t.Kind == "video" || t.Kind == "file"):
					v.AwaitingManual++
				case activity.States[mediafetch.BackfillKey{Seq: t.Seq, Index: t.Index, Forward: t.Forward, Node: t.Node}] == "running":
					v.Running++
				case activity.States[mediafetch.BackfillKey{Seq: t.Seq, Index: t.Index, Forward: t.Forward, Node: t.Node}] == "queued" || t.State == store.MediaPending:
					v.Queued++
				case t.State == "":
					v.Unchecked++
				default:
					v.Failed++
				}
			}
			if b.media.media == nil && b.media.forwards == nil {
				v.State = "skipped"
			} else if v.Queued+v.Running > 0 || activity.Sweeping || sweeping {
				v.State = "waiting"
			} else if v.Unchecked > 0 {
				v.State = "unchecked"
			}
			changed := v != previous
			if changed {
				previous = v
			}
			v.AsOfMS = b.clock.Now().UnixMilli()
			ended := current.State == "completed" || current.State == "failed" || current.State == "canceled"
			if changed {
				b.tasks.Lock()
				next, ok := b.tasks.jobs[j.ID]
				if ok {
					next.Media = v
					b.tasks.jobs[j.ID] = next
					ended = next.State == "completed" || next.State == "failed" || next.State == "canceled"
					b.notifyLocked(ended && v.State != "waiting")
				}
				b.tasks.Unlock()
			}
			if ended && v.State != "waiting" {
				if err = b.st.UpdateAlignmentMedia(ctx, b.account, j.ID, v); err != nil && ctx.Err() == nil {
					b.logf("QQ 对齐媒体快照写入失败：%v", err)
				}
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-changes.Wake():
			}
		}
	}()
	return nil
}

// cancelAlignmentMedia 保留已证实结果，未完成项退回未检查，不把停机冒充仍在下载。
func cancelAlignmentMedia(m *api.AlignmentMedia) {
	if m.State == "waiting" {
		m.Unchecked += m.Queued + m.Running
		m.Queued, m.Running = 0, 0
		m.State = "unchecked"
	}
}
