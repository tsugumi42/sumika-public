package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/store"
)

// 生命周期锁只接受请求；慢查询、Ticket 和停止收尾均由原工人负责。
type backfillTasks struct {
	sync.Mutex
	jobs                               map[string]api.BackfillJob
	mediaEndSeq                        map[string]int64
	queue                              []string
	current, accountJob, last, lastGap string
	wake                               chan struct{}
	notify                             *time.Timer
	lastNotify                         time.Time
	online                             bool
}

func (b *qqBackfill) initTasksLocked() {
	if b.tasks.jobs == nil {
		b.tasks.jobs = map[string]api.BackfillJob{}
		b.tasks.mediaEndSeq = map[string]int64{}
		b.tasks.wake = make(chan struct{}, 1)
	}
}
func newBackfillJob(scope, trigger string, ref *api.GapRef) (api.BackfillJob, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return api.BackfillJob{}, err
	}
	return api.BackfillJob{ID: hex.EncodeToString(id[:]), Scope: scope, Trigger: trigger, State: "queued", Gap: ref, Messages: "queued", Media: api.AlignmentMedia{Scope: "connection-7d+round-new", State: "unchecked"}}, nil
}
func cloneJob(j api.BackfillJob) api.BackfillJob {
	if j.Gap != nil {
		j.Gap = new(*j.Gap)
	}
	return j
}
func (b *qqBackfill) job(id string) (api.BackfillJob, error) {
	b.tasks.Lock()
	defer b.tasks.Unlock()
	j, ok := b.tasks.jobs[id]
	if !ok {
		return j, api.BackfillFailure("stale-gap")
	}
	return cloneJob(j), nil
}
func (b *qqBackfill) notifyLocked(immediate bool) {
	if immediate {
		if b.tasks.notify != nil {
			b.tasks.notify.Stop()
			b.tasks.notify = nil
		}
		b.tasks.lastNotify = time.Now()
		b.st.NotifyBackfill()
		return
	}
	if b.tasks.notify != nil {
		return
	}
	wait := max(time.Duration(0), 200*time.Millisecond-time.Since(b.tasks.lastNotify))
	b.tasks.notify = time.AfterFunc(wait, func() {
		b.tasks.Lock()
		defer b.tasks.Unlock()
		b.tasks.notify = nil
		b.tasks.lastNotify = time.Now()
		b.st.NotifyBackfill()
	})
}
func (b *qqBackfill) progress(fn func(*api.BackfillJob)) {
	b.tasks.Lock()
	defer b.tasks.Unlock()
	id := b.tasks.current
	if id == "" {
		return
	}
	j := b.tasks.jobs[id]
	fn(&j)
	b.tasks.jobs[id] = j
	b.notifyLocked(false)
}
func (b *qqBackfill) persist(ctx context.Context, j api.BackfillJob) error {
	media, err := json.Marshal(j.Media)
	if err != nil {
		return err
	}
	var gap int64
	if j.Gap != nil {
		gap = j.Gap.ID
	}
	return b.st.SaveAlignment(ctx, b.account, store.AlignmentRecord{JobID: j.ID, Scope: j.Scope, Trigger: j.Trigger, State: j.State, GapID: gap, StartedMS: j.StartedMS, EndedMS: j.EndedMS, Inserted: j.Inserted, Duplicates: j.Duplicates, Messages: j.Messages, MediaJSON: string(media), Reason: j.ReasonCode, Covered: j.CompleteCoverage})
}
func (b *qqBackfill) capability() string {
	if !b.source.Ready() {
		return "unavailable"
	}
	if !b.source.Caps().History {
		return "unsupported"
	}
	return ""
}
func validGapRef(ref api.GapRef) bool {
	c, err := types.ParseConversation(ref.ConversationKey.Conversation)
	return err == nil && c.Channel == "qq" && ref.ConversationKey == (api.ConversationKey{Platform: "qq", Channel: "qq", Scene: c.Scene, Conversation: c.String()}) && ref.ID > 0 && ref.FromMS >= 0 && ref.ToMS >= ref.FromMS
}
func (b *qqBackfill) readRef(ctx context.Context, ref api.GapRef) (store.ChannelGap, error) {
	if !b.allowed(ref.ConversationKey.Conversation) {
		return store.ChannelGap{}, api.BackfillFailure("invalid")
	}
	return readGapRef(ctx, b.st, ref)
}

func readGapRef(ctx context.Context, st *store.Store, ref api.GapRef) (store.ChannelGap, error) {
	if !validGapRef(ref) {
		return store.ChannelGap{}, api.BackfillFailure("invalid")
	}
	g, err := st.ChannelGap(ctx, ref.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return g, api.BackfillFailure("stale-gap")
	}
	if err != nil {
		return g, err
	}
	if g.Channel != "qq" || g.Conversation != ref.ConversationKey.Conversation || g.FromMS != ref.FromMS || g.ToMS != ref.ToMS {
		return g, api.BackfillFailure("stale-gap")
	}
	return g, nil
}
func (b *qqBackfill) requestGap(ctx context.Context, ref api.GapRef) (api.BackfillJob, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return api.BackfillJob{}, err
	}
	g, err := b.readRef(ctx, ref)
	if err != nil {
		return api.BackfillJob{}, err
	}
	b.tasks.Lock()
	defer b.tasks.Unlock()
	b.initTasksLocked()
	for _, j := range b.tasks.jobs {
		if j.Gap != nil && *j.Gap == ref && (j.State == "queued" || j.State == "running") {
			return cloneJob(j), nil
		}
	}
	if g.State == "filled" {
		return b.completedGapLocked(ref)
	}
	if code := b.capability(); code != "" {
		return api.BackfillJob{}, api.BackfillFailure(code)
	}
	if !b.aliveLocked() || !b.tasks.online {
		return api.BackfillJob{}, api.BackfillFailure("unavailable")
	}
	n := 0
	for _, id := range b.tasks.queue {
		if b.tasks.jobs[id].Scope == "gap" {
			n++
		}
	}
	if n >= 16 {
		return api.BackfillJob{}, api.BackfillFailure("busy")
	}
	if g.LastManualMS != 0 && b.clock.Now().UnixMilli()-g.LastManualMS < backfillRetry.Milliseconds() {
		return api.BackfillJob{}, api.BackfillFailure("cooldown")
	}
	j, err := newBackfillJob("gap", "retry", &ref)
	if err != nil {
		return j, err
	}
	if err = b.st.StartManualGap(ctx, g, b.clock.Now().UnixMilli(), backfillRetry.Milliseconds()); err != nil {
		switch {
		case errors.Is(err, store.ErrGapCooldown):
			err = api.BackfillFailure("cooldown")
		case errors.Is(err, store.ErrStaleGap):
			err = api.BackfillFailure("stale-gap")
		case errors.Is(err, store.ErrGapFilled):
			return b.completedGapLocked(ref)
		}
		return api.BackfillJob{}, err
	}
	b.tasks.jobs[j.ID] = j
	b.tasks.queue = append(b.tasks.queue, j.ID)
	select {
	case b.tasks.wake <- struct{}{}:
	default:
	}
	b.notifyLocked(true)
	return cloneJob(j), nil
}
func (b *qqBackfill) requestAccount(ctx context.Context) (api.BackfillJob, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return api.BackfillJob{}, err
	}
	if code := b.capability(); code != "" {
		return api.BackfillJob{}, api.BackfillFailure(code)
	}
	if !b.aliveLocked() {
		return api.BackfillJob{}, api.BackfillFailure("unavailable")
	}
	b.tasks.Lock()
	defer b.tasks.Unlock()
	b.initTasksLocked()
	if !b.tasks.online {
		return api.BackfillJob{}, api.BackfillFailure("unavailable")
	}
	if id := b.tasks.accountJob; id != "" {
		return cloneJob(b.tasks.jobs[id]), nil
	}
	j, err := newBackfillJob("account", "manual", nil)
	if err != nil {
		return j, err
	}
	b.tasks.jobs[j.ID] = j
	b.tasks.accountJob = j.ID
	b.tasks.queue = append(b.tasks.queue, j.ID)
	select {
	case b.tasks.wake <- struct{}{}:
	default:
	}
	b.notifyLocked(true)
	return cloneJob(j), nil
}
func (b *qqBackfill) projectGap(g store.ChannelGap) api.GapView {
	v := desktopread.GapProjection(g)
	v.ReasonCode = b.capability()
	if !b.allowed(g.Conversation) {
		v.ReasonCode = "excluded"
	}
	v.RetryAfterMS = g.LastManualMS + backfillRetry.Milliseconds()
	if g.LastManualMS == 0 || v.RetryAfterMS <= b.clock.Now().UnixMilli() {
		v.RetryAfterMS = 0
	}
	b.tasks.Lock()
	defer b.tasks.Unlock()
	if !b.tasks.online {
		v.ReasonCode = "unavailable"
	}
	for _, j := range b.tasks.jobs {
		if j.Gap != nil && j.Gap.ID == g.ID && j.Gap.FromMS == g.FromMS && j.Gap.ToMS == g.ToMS && (j.State == "running" || j.State == "queued") {
			v.JobID = j.ID
		}
	}
	v.CanRetry = v.ReasonCode == "" && v.RetryAfterMS == 0 && v.JobID == ""
	return v
}
func (b *qqBackfill) startJob(ctx context.Context, j api.BackfillJob) error {
	b.beginQueries()
	j.State, j.StartedMS, j.Messages = "running", b.clock.Now().UnixMilli(), "running"
	b.tasks.Lock()
	b.initTasksLocked()
	b.tasks.current = j.ID
	b.tasks.jobs[j.ID] = j
	b.notifyLocked(true)
	b.tasks.Unlock()
	if err := b.persist(ctx, j); err != nil {
		return err
	}
	return b.beginMedia(ctx, j)
}
func (b *qqBackfill) finishJob(ctx context.Context, id string, taskErr error) error {
	final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	through, readErr := b.st.BackfillEventHighWater(final)
	taskErr = errors.Join(taskErr, readErr)
	b.tasks.Lock()
	defer b.tasks.Unlock()
	j := b.tasks.jobs[id]
	j.State, j.EndedMS = "completed", b.clock.Now().UnixMilli()
	if taskErr != nil {
		j.State, j.ReasonCode = "failed", "io"
		if j.Messages == "queued" || j.Messages == "running" {
			j.Messages = "failed"
		}
		var be *api.BackfillError
		if errors.As(taskErr, &be) {
			j.ReasonCode = be.Code
		}
		if errors.Is(taskErr, context.Canceled) {
			j.State, j.ReasonCode = "canceled", "interrupted"
			if j.Messages == "failed" {
				j.Messages = "canceled"
			}
			cancelAlignmentMedia(&j.Media)
			j.Media.AsOfMS = j.EndedMS
		}
	}
	j.CurrentConversation = api.ConversationKey{}
	// 先提交终局再发布，媒体工人不能抢在 last 替换前写完并退出。
	persistErr := b.persist(final, j)
	b.tasks.jobs[id] = j
	b.tasks.mediaEndSeq[id] = through
	b.retainFinishedLocked(j)
	b.tasks.current = ""
	if b.tasks.accountJob == id {
		b.tasks.accountJob = ""
	}
	b.notifyLocked(true)
	return errors.Join(taskErr, persistErr)
}
func (b *qqBackfill) executeGap(ctx context.Context, j api.BackfillJob) error {
	g, err := b.readRef(ctx, *j.Gap)
	if err != nil {
		return err
	}
	if code := b.capability(); code != "" {
		return api.BackfillFailure(code)
	}
	if g.State == "filled" {
		b.progress(func(j *api.BackfillJob) { j.Messages = "completed" })
		return nil
	}
	c, _ := types.ParseConversation(g.Conversation)
	coverage, err := b.conversation(ctx, backfillTarget{conv: c, manual: true}, g.FromMS, g.ToMS)
	if err != nil {
		return err
	}
	b.tasks.Lock()
	inserted := b.tasks.jobs[b.tasks.current].Inserted
	b.tasks.Unlock()
	complete := inserted > 0 && coverage.covered && coverage.coveredFrom <= g.FromMS && coverage.coveredTo >= g.ToMS
	if !complete && coverage.reason == "" {
		coverage.reason = "unverified-history"
	}
	if err = b.st.FinishManualGap(ctx, g, complete, coverage.reason, b.clock.Now().UnixMilli()); err != nil {
		return err
	}
	b.progress(func(j *api.BackfillJob) {
		j.Messages = "completed"
		j.CompleteCoverage = complete
		j.ReasonCode = coverage.reason
		j.Checked = 1
		j.Pending = 0
	})
	return nil
}
func (b *qqBackfill) popJob(gapOnly bool) (api.BackfillJob, bool) {
	b.tasks.Lock()
	defer b.tasks.Unlock()
	for i, id := range b.tasks.queue {
		j := b.tasks.jobs[id]
		if gapOnly && j.Scope != "gap" {
			continue
		}
		b.tasks.queue = append(b.tasks.queue[:i], b.tasks.queue[i+1:]...)
		return j, true
	}
	return api.BackfillJob{}, false
}

// drainGaps 在当前会话完成后让单段插队，沿用外层查询轮次，不能通过连点重置预算。
func (b *qqBackfill) drainGaps(ctx context.Context) error {
	b.tasks.Lock()
	parent := b.tasks.current
	b.tasks.Unlock()
	for {
		j, ok := b.popJob(true)
		if !ok {
			break
		}
		err := b.startJob(ctx, j)
		if err == nil {
			err = b.executeGap(ctx, j)
		}
		err = b.finishJob(ctx, j.ID, err)
		if ctx.Err() != nil {
			return err
		}
		if err != nil {
			b.logf("QQ 单段补录结束：%s", j.ID)
		}
	}
	b.tasks.Lock()
	b.tasks.current = parent
	j, ok := b.tasks.jobs[parent]
	b.tasks.Unlock()
	if ok {
		return b.persist(ctx, j)
	}
	return nil
}
func (b *qqBackfill) executeAccount(ctx context.Context, trigger string, queued *api.BackfillJob) error {
	b.tasks.Lock()
	b.initTasksLocked()
	var j api.BackfillJob
	var err error
	if queued != nil {
		j = *queued
	} else if existing, ok := b.tasks.jobs[b.tasks.accountJob]; ok && existing.State == "queued" {
		j = existing
		for i, id := range b.tasks.queue {
			if id == j.ID {
				b.tasks.queue = append(b.tasks.queue[:i], b.tasks.queue[i+1:]...)
				break
			}
		}
	} else {
		j, err = newBackfillJob("account", trigger, nil)
		if err != nil {
			b.tasks.Unlock()
			return err
		}
	}
	b.tasks.accountJob = j.ID
	b.tasks.jobs[j.ID] = j
	b.tasks.Unlock()
	if err = b.startJob(ctx, j); err == nil {
		err = b.round(ctx)
	}
	return b.finishJob(ctx, j.ID, err)
}
func (b *qqBackfill) cancelQueued(ctx context.Context) {
	b.tasks.Lock()
	b.tasks.online = false
	b.tasks.Unlock()
	for {
		j, ok := b.popJob(false)
		if !ok {
			break
		}
		if err := b.finishJob(ctx, j.ID, context.Canceled); err != nil && !errors.Is(err, context.Canceled) {
			b.logf("QQ 补录收尾失败：%v", err)
		}
	}
	b.media.wg.Wait()
	b.tasks.Lock()
	if b.tasks.notify != nil {
		b.tasks.notify.Stop()
		b.tasks.notify = nil
	}
	b.tasks.Unlock()
}

// 连续手动请求共享十分钟窗口；父账号轮次未结束时，插队不能按墙钟续领额度。
func (b *qqBackfill) beginQueries() {
	if b.queryStarted.IsZero() || !b.inQueryRound && b.clock.Now().Sub(b.queryStarted) >= backfillRetry {
		b.queryRemaining = backfillBudget
		b.queryStarted = b.clock.Now()
		b.queryActive = false
	}
	if !b.queryActive {
		b.source.BeginQueryRound(b.queryRemaining)
		b.queryActive = true
	}
}

func (b *qqBackfill) endQueries() {
	if b.queryActive {
		b.queryRemaining = b.source.RemainingQueryBudget()
		b.source.EndQueryRound()
		b.queryActive = false
	}
}

func (b *qqBackfill) runQueued(ctx context.Context) error {
	for {
		j, ok := b.popJob(false)
		if !ok {
			return nil
		}
		var err error
		if j.Scope == "account" {
			err = b.executeAccount(ctx, "manual", &j)
		} else {
			b.beginQueries()
			err = b.startJob(ctx, j)
			if err == nil {
				err = b.executeGap(ctx, j)
			}
			err = b.finishJob(ctx, j.ID, err)
		}
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
		if err != nil {
			b.logf("QQ 手动补录任务失败：%s", j.ID)
		}
	}
}

func (b *qqBackfill) completedGapLocked(ref api.GapRef) (api.BackfillJob, error) {
	for _, j := range b.tasks.jobs {
		if j.Gap != nil && *j.Gap == ref && j.State == "completed" && j.CompleteCoverage {
			return cloneJob(j), nil
		}
	}
	j, err := newBackfillJob("gap", "retry", new(ref))
	if err != nil {
		return j, err
	}
	j.State, j.Messages = "completed", "completed"
	b.tasks.jobs[j.ID] = j
	b.retainFinishedLocked(j)
	return cloneJob(j), nil
}

// retainFinishedLocked 保留最近单段，父账号轮次结束不能吃掉刚发布的单段终局；内存至多留两种终局。
func (b *qqBackfill) retainFinishedLocked(j api.BackfillJob) {
	oldLast, oldGap := b.tasks.last, b.tasks.lastGap
	if j.Scope == "gap" {
		b.tasks.lastGap = j.ID
	}
	if oldLast != "" && oldLast != j.ID && oldLast != b.tasks.lastGap {
		delete(b.tasks.jobs, oldLast)
		delete(b.tasks.mediaEndSeq, oldLast)
	}
	if oldGap != "" && oldGap != oldLast && oldGap != j.ID && oldGap != b.tasks.lastGap {
		delete(b.tasks.jobs, oldGap)
		delete(b.tasks.mediaEndSeq, oldGap)
	}
	b.tasks.last = j.ID
}
