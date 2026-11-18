package turn

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"sumika-native/internal/audience"
	"sumika-native/internal/history"
	"sumika-native/internal/kernel"
	"sumika-native/internal/runstate"
	"sumika-native/internal/store"
)

var ErrInputBusy = errors.New("这个会话正在运行")

type Submission struct {
	Input         Input
	Mode          kernel.InputMode
	ClientKey     string
	ExpectedRunID string
}

var ErrInputAuthority = errors.New("输入没有匹配的可信身份")

type LifecycleConfig struct {
	MaxPendingPerConversation, MaxPendingGlobal int
	DrainTimeout, SnapshotInterval              time.Duration
	StopGrace                                   time.Duration
	StopGraceSet                                bool // 显式零值立即升级；未设置使用默认宽限。
}

func (c LifecycleConfig) defaults() LifecycleConfig {
	if c.MaxPendingPerConversation == 0 {
		c.MaxPendingPerConversation = 32
	}
	if c.MaxPendingGlobal == 0 {
		c.MaxPendingGlobal = 256
	}
	if c.DrainTimeout == 0 {
		c.DrainTimeout = 10 * time.Second
	}
	if !c.StopGraceSet && c.StopGrace == 0 {
		c.StopGrace = min(5*time.Second, c.DrainTimeout/2)
	}
	c.StopGrace = max(0, min(c.StopGrace, c.DrainTimeout-time.Nanosecond))
	if c.SnapshotInterval == 0 {
		c.SnapshotInterval = 50 * time.Millisecond
	}
	return c
}

type inputRun struct {
	input      Input
	record     store.KernelInput
	ctx        context.Context
	activity   kernel.Activity
	outcome    chan Outcome
	cancel     context.CancelFunc
	stopCaller func() bool
	snapshot   RunSnapshot
	wakes      map[*RunSubscription]struct{}
	lastWake   time.Time
	wakeTimer  *time.Timer
	// 子运行准入时固定所属前台域；父运行被回收后，维护屏障仍能找到它。
	maintenanceDomain string
	stop              *stagedStop
}
type hubLifecycle struct {
	mu      sync.Mutex
	sealed  atomic.Bool
	manager *runstate.Manager
	// 故障（fault.go）：会话故障按会话域放，内核故障只有启动时的恢复和清扫没做成时才有；都只在内存里。
	faults        map[string]*faultState
	kernelFault   *faultState
	runs          map[string]*inputRun
	completed     [completedRunLimit]string
	nextCompleted int
	active        map[string]*inputRun
	pending       map[string][]*inputRun
	steers        map[string][]store.KernelInput
	// internal 是还在跑的子运行实例；task_message 的追加指示走实例自己的内存插话队列，不进持久输入箱。
	internal    map[string]*Live
	maintenance map[string]bool
}

func (h *Hub) initLifecycle() {
	var j runstate.Journal
	if st := h.trace(); st != nil {
		j = st
	}
	m := runstate.New(j)
	h.lifecycle = &hubLifecycle{manager: m, runs: map[string]*inputRun{}, active: map[string]*inputRun{}, pending: map[string][]*inputRun{}, steers: map[string][]store.KernelInput{}, internal: map[string]*Live{}, maintenance: map[string]bool{}, faults: map[string]*faultState{}}
	h.Cfg.Lifetime = m
	if st := h.trace(); st != nil {
		// 整库的恢复没做成：哪些会话受影响没法知道，所有输入先拒，等重试（fault.go）
		if err := st.RecoverKernelInputs(context.Background()); err != nil {
			h.lifecycle.raiseLocked(time.Now(), FaultScopeKernel, "", "", FaultStartupRecover, err, repair{kind: repairRecover})
		}
	}
}

func inputDigest(s Submission) (string, error) {
	// 运行号、取消代次由宿主分配，重试不把它们纳入客户端载荷。
	s.Input.Identity = kernel.Invocation{Scope: s.Input.Identity.Scope, Kind: s.Input.Identity.Kind, RootID: s.Input.Identity.RootID, ParentRunID: s.Input.Identity.ParentRunID, Generation: s.Input.Identity.Generation}
	s.Input.requestCtx, s.Input.onDraft = nil, nil
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// MaxInputParts 是一条消息最多几块。
const MaxInputParts = 20

func partValid(p history.Part) bool {
	if !utf8.ValidString(p.Text) || (p.Text == "" && len(p.Attachments) == 0) {
		return false
	}
	if q := p.Quote; q != nil && (q.ID == "" || q.Text == "" || !utf8.ValidString(q.Text) || !utf8.ValidString(q.Name)) {
		return false
	}
	for _, a := range p.Attachments {
		if (a.Kind != "image" && a.Kind != "file") || (a.Path == "" && a.Hash == "") || !utf8.ValidString(a.Name) || !utf8.ValidString(a.Path) {
			return false
		}
	}
	return true
}

func validateInput(in Input) error {
	s := in.Identity.Scope
	// JSON 会替换非法 UTF-8；拒绝它，内存身份比较与持久 Scope 比较才不会合并不同字节。
	for _, value := range []string{s.CharacterID, s.Conversation, s.ScopeID, s.Platform, s.Channel, s.AccountID, s.Scene, s.ActorKind, s.ActorID, s.AuthorityID} {
		if !utf8.ValidString(value) {
			return ErrInputAuthority
		}
	}
	// 会话号和角色号不能空：故障、闸门、持久输入箱都按（角色，会话）认域，空会话号会让不同输入落进同一个域
	if s.Platform == "" || s.Conversation == "" {
		return ErrInputAuthority
	}
	if s.CharacterID != in.CharacterID || s.Conversation != in.Conversation || s.ScopeID != in.Scope || s.Channel != in.Audience.Channel || s.Scene != in.Audience.Scene || s.CharacterID == "" || s.ScopeID == "" || s.ActorID == "" || s.ActorID == "0" {
		return ErrInputAuthority
	}
	if s.Scene != "private" && s.Scene != "group" {
		return ErrInputAuthority
	}
	// 分块只属于本地私聊里主人自己发的话；每块要有文字或附件，引用块的 id 和片段不能为空，附件要有来源。
	if len(in.Parts) > 0 {
		if !in.local() || s.Scene != "private" || s.ActorKind != "owner" || len(in.Parts) > MaxInputParts {
			return ErrInputAuthority
		}
		for _, p := range in.Parts {
			if !partValid(p) {
				return ErrInputAuthority
			}
		}
	}
	switch s.ActorKind {
	case "owner", "human", "character", "mixed":
	case "system":
		// 系统发言人只给主动联系的话题线：会话串、发言人 id、权限档、听众都要对上，宿主预先绑定（第四节），
		// 听众的 PrivateOK 必须为假——否则线里的输入会被当成主人在场
		if !audience.IsThread(s.Conversation) || s.ActorID != audience.ThreadActor || in.Scope != audience.ThreadScope ||
			in.Audience.PrivateOK || in.Audience.OwnerAsked || s.Channel != "console" || s.Scene != "private" || s.Platform != "local" {
			return ErrInputAuthority
		}
	default:
		return ErrInputAuthority
	}
	// 线里的输入只能是系统发言人：线的听众不是主人在场，主人的话由宿主转述成 system 行再交给线
	if audience.IsThread(s.Conversation) && s.ActorKind != "system" {
		return ErrInputAuthority
	}
	// 发言人不能是接收账号自己，不论渠道；除本地主窗口外每个场合都有接收账号，缺了就拒（见 kernel.Scope.AccountRequired）
	if s.AccountMissing() || s.ActorIsAccount() {
		return ErrInputAuthority
	}
	if s.ActorKind == "character" && s.ActorID == s.CharacterID {
		return ErrInputAuthority
	}
	if s.ActorKind == "mixed" && len(in.Seqs) == 0 {
		return ErrInputAuthority
	}
	if ownerConsoleAudience(in.Audience) && s.ActorKind != "owner" {
		return ErrInputAuthority
	}
	if s.AuthorityID != "" && (s.AuthorityRevision == 0 || s.ActorKind == "mixed") {
		return ErrInputAuthority
	}
	if in.Identity.Kind != kernel.KindForeground {
		return ErrInputAuthority
	}
	return nil
}

// Submit 在事件和 inbox 都提交后回执；持久 inbox 是唯一的待处理输入队列。
func (h *Hub) Submit(ctx context.Context, s Submission) (kernel.Receipt, error) {
	ctx, cancelAdmission := context.WithCancel(ctx)
	stopAdmission := context.AfterFunc(h.base, cancelAdmission)
	defer func() { stopAdmission(); cancelAdmission() }()
	if err := validateInput(s.Input); err != nil {
		return kernel.Receipt{}, err
	}
	if h.Cfg.BindInput != nil {
		bound, err := h.Cfg.BindInput(ctx, s.Input.Identity)
		if err != nil {
			return kernel.Receipt{}, err
		}
		s.Input.Identity = bound
	}
	platform, platformErr := store.KernelPlatform(h.Stores, s.Input.Audience.Channel)
	if platformErr != nil {
		return kernel.Receipt{}, platformErr
	}
	if platform != s.Input.Identity.Scope.Platform {
		return kernel.Receipt{}, ErrInputAuthority
	}
	if s.ClientKey == "" {
		return kernel.Receipt{}, errors.New("ClientKey 不能为空")
	}
	switch s.Mode {
	case kernel.InputStart, kernel.InputSteer, kernel.InputQueue:
	default:
		return kernel.Receipt{}, errors.New("未知输入方式")
	}
	requestedMode := s.Mode
	if s.Mode == kernel.InputSteer && s.ExpectedRunID == "" {
		return kernel.Receipt{}, ErrInputAuthority
	}
	digest, err := inputDigest(s)
	if err != nil {
		return kernel.Receipt{}, err
	}
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.sealed.Load() {
		return kernel.Receipt{}, runstate.ErrSealed
	}
	domain := convKey(s.Input.CharacterID, s.Input.Conversation)
	// 故障先于幂等回放：出事的会话在恢复之前，重复提交也不多受理一条
	if fe := lc.faultFor(domain); fe != nil {
		return kernel.Receipt{}, fe
	}
	tr := h.trace()
	if tr == nil {
		return kernel.Receipt{}, errors.New("持久输入没有 trace 库")
	}
	if lc.maintenance[domain] {
		return kernel.Receipt{}, ErrInputBusy
	}
	old, err := tr.KernelInput(ctx, domain, s.ClientKey)
	if err == nil {
		if old.Digest != digest {
			return kernel.Receipt{}, store.ErrInputConflict
		}
		if err := AdmitBudgetRoot(ctx, h.Cfg.Budget, old.Identity, h.Cfg.BudgetConfig.LimitsForInput(old.Identity.Scope, s.Input.Auto, time.UnixMilli(old.AcceptedAt))); err != nil {
			return kernel.Receipt{}, err
		}
		return receipt(old), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return kernel.Receipt{}, err
	}
	active := lc.active[domain]
	if s.Mode == kernel.InputStart && (active != nil || len(lc.pending[domain]) > 0) {
		return kernel.Receipt{}, ErrInputBusy
	}
	if s.Mode == kernel.InputSteer {
		target := lc.runs[s.ExpectedRunID]
		if target == nil || target.input.Identity.Scope != s.Input.Identity.Scope {
			return kernel.Receipt{}, store.ErrInputConflict
		}
		if active != target || target.snapshot.State != kernel.RunRunning {
			s.Mode = kernel.InputQueue
		} else {
			s.Input.Identity = target.input.Identity
		}
	}
	in := s.Input
	if s.Mode != kernel.InputSteer {
		in.Identity = kernel.Invocation{Scope: in.Identity.Scope, Kind: kernel.KindForeground, RootID: in.Identity.RootID, ParentRunID: in.Identity.ParentRunID, Generation: in.Identity.Generation}
	}
	keySum := sha256.Sum256([]byte(domain + "\x00" + s.ClientKey))
	in.Identity.InputID = "input-" + hex.EncodeToString(keySum[:])
	if s.Mode != kernel.InputSteer {
		in.Identity.RunID = store.NewID(h.Cfg.Now())
		if in.Identity.RootID == "" {
			in.Identity.RootID = in.Identity.RunID
		}
		if in.Identity.Generation == 0 {
			in.Identity.Generation = 1
		}
	}
	newLocalEvent := in.local() && len(in.Seqs) == 0
	if in.local() && len(in.Seqs) == 0 {
		st, e := h.storeOf(in.Conversation)
		if e != nil {
			return kernel.Receipt{}, e
		}
		ev := store.NewEvent{Channel: in.Audience.Channel, Conversation: in.Conversation, ActorKind: in.Identity.Scope.ActorKind, ActorID: in.Identity.Scope.ActorID, Origin: in.Identity.Scope.ActorKind, Kind: "message", Text: in.Text, CreatedAt: h.Cfg.Now().UnixMilli(), Tags: selftestTags(in.Conversation)}
		if len(in.Parts) > 0 {
			ev.Segments = map[string][]history.Part{"local_parts": in.Parts}
			for _, p := range in.Parts {
				if p.Quote != nil {
					id := p.Quote.ID
					ev.ReplyTo = &id // 第一个引用块的出处；界面的 Reply 和旧的单引用读法照旧能用
					break
				}
			}
		}
		seq, e := st.AppendInputEvent(ctx, in.Identity.InputID, digest, ev, []string{in.CharacterID})
		if e != nil {
			return kernel.Receipt{}, e
		}
		in.Seqs = []int64{seq}
		if h.testHook != nil {
			h.testHook("inputEventCommitted")
		}
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return kernel.Receipt{}, err
	}
	limits := h.Cfg.Lifecycle.defaults()
	row, fresh, err := tr.AcceptKernelInput(ctx, store.KernelInput{Identity: in.Identity, Domain: domain, ClientKey: s.ClientKey, Digest: digest, Payload: string(payload), Mode: s.Mode, AcceptedAt: time.Now().UnixMilli()}, limits.MaxPendingPerConversation, limits.MaxPendingGlobal)
	if err != nil {
		return kernel.Receipt{}, err
	}
	if err := AdmitBudgetRoot(ctx, h.Cfg.Budget, row.Identity, h.Cfg.BudgetConfig.LimitsForInput(row.Identity.Scope, in.Auto, time.UnixMilli(row.AcceptedAt))); err != nil {
		return kernel.Receipt{}, err
	}
	if !fresh {
		return receipt(row), nil
	}
	if newLocalEvent {
		if h.Cfg.OnLocalStored != nil {
			h.Cfg.OnLocalStored(in.Conversation, in.Seqs[0], in.Text)
		}
		h.plugins.notifySeen(Seen{CharacterID: in.CharacterID, Conversation: in.Conversation, Seq: in.Seqs[0]})
	}
	if s.Mode == kernel.InputSteer {
		lc.steers[in.Identity.RunID] = append(lc.steers[in.Identity.RunID], row)
		return receipt(row), nil
	}
	if requestedMode == kernel.InputSteer && !in.local() {
		return receipt(row), nil
	}
	if err := h.enqueueAcceptedLocked(in, row); err != nil {
		return kernel.Receipt{}, err
	}
	return receipt(row), nil
}
func receipt(r store.KernelInput) kernel.Receipt {
	return kernel.Receipt{InputID: r.Identity.InputID, RunID: r.Identity.RunID, Sequence: r.Sequence, Revision: r.Revision, State: r.State}
}

func (h *Hub) startInputLocked(domain string) {
	lc := h.lifecycle
	if lc.active[domain] != nil || len(lc.pending[domain]) == 0 {
		return
	}
	r := lc.pending[domain][0]
	lc.pending[domain] = lc.pending[domain][1:]
	lc.active[domain] = r
	h.enqueue(domain, func() { h.executeInput(r) })
}
func (h *Hub) executeInput(r *inputRun) {
	lc := h.lifecycle
	lc.mu.Lock()
	err := r.ctx.Err()
	// 排在故障会话里的下一轮不开始：照实报“这个对话暂时没法继续”，话在库里改成 held，等重试（和重启恢复同一个结果）。
	// 用单独的 faulted 标记控制是否调模型，不拿 err 兼作控制变量（它可能带着取消之类的错误）
	faulted := false
	if fe := lc.faultFor(r.record.Domain); fe != nil {
		err, faulted = fe, true
		lc.addRepairsLocked(r.record.Domain, repair{kind: repairHold, input: r.input.Identity.InputID})
	}
	if err == nil && r.input.local() {
		// 受理时开的根按受理时刻算时限；排队等前面的轮次不该吃掉这句话自己的额度时间。
		if rb, ok := h.Cfg.Budget.(deadlineRestarter); ok {
			err = rb.RestartDeadline(context.Background(), r.input.Identity.RootID, h.Cfg.BudgetConfig.Limits(time.Now()).Deadline)
		}
	}
	if err == nil {
		err = h.trace().AdmitKernelInput(context.Background(), r.input.Identity.InputID)
		if err != nil && !errors.Is(err, store.ErrInputConflict) {
			// 这句话在库里还是 accepted，运行却要落定了：记录对不上，这个会话进故障，重试把它改成 held；
			// 同键重发在恢复之前被故障拒掉，不会拿到一张死运行的回执。冲突（这句话已被取消）只拒这一轮
			lc.raiseLocked(time.Now(), FaultScopeConversation, r.input.CharacterID, r.input.Conversation, FaultAdmit, err, repair{kind: repairHold, input: r.input.Identity.InputID})
		}
	}
	if err == nil {
		err = h.trace().KernelRunState(context.Background(), r.input.Identity.RunID, kernel.RunRunning, "", false)
	}
	if err == nil {
		r.snapshot.State = kernel.RunRunning
		h.wakeRunLocked(r, true)
	}
	lc.mu.Unlock()
	var out Outcome
	out.TurnID = r.input.Identity.RunID
	var settled Settled
	in := r.input
	in.onSettled = func(s Settled) { settled = s }
	if !faulted && (err == nil || errors.Is(err, context.Canceled)) {
		out.Result, out.TurnID, out.Err = h.respond(r.ctx, in)
	} else {
		h.unmarkQueued(r.input)
		out.Err = err
	}
	state := kernel.OutcomeDone
	if out.Err != nil {
		state = kernel.OutcomeError
	}
	if r.ctx.Err() != nil || (out.Result != nil && out.Result.Stopped) {
		state = kernel.OutcomeStopped
	}
	if out.Result != nil && out.Result.BudgetExhausted {
		state = kernel.OutcomeBudgetExhausted
	}
	r.stopCaller()
	lc.mu.Lock()
	// 生成结局先固定；后到的工具成功不能把 stopped 改成 done。
	r.snapshot.State, r.snapshot.Outcome = kernel.RunDraining, state
	r.snapshot.Reason = h.reasonOf(state, out.Err, out.Result)
	drainingErr := h.trace().KernelRunState(context.Background(), out.TurnID, kernel.RunDraining, state, false)
	if drainingErr != nil {
		// 只挡这个会话：这一轮在库里的状态和内存对不上
		lc.raiseLocked(time.Now(), FaultScopeConversation, r.input.CharacterID, r.input.Conversation, FaultRunState, drainingErr, repair{kind: repairRunState, run: out.TurnID, state: kernel.RunDraining, outcome: state})
	}
	h.wakeRunLocked(r, true)
	lc.mu.Unlock()
	r.activity.End(out.Err)
	drainErr := lc.manager.WaitRun(context.Background(), out.TurnID)
	r.stop.finish()
	lc.mu.Lock()
	settledErr := h.trace().KernelRunState(context.Background(), out.TurnID, kernel.RunSettled, state, true)
	if drainErr != nil {
		lc.raiseLocked(time.Now(), FaultScopeConversation, r.input.CharacterID, r.input.Conversation, FaultActivityEnd, drainErr, repair{kind: repairActivityEnd, run: out.TurnID})
	}
	if settledErr != nil {
		lc.raiseLocked(time.Now(), FaultScopeConversation, r.input.CharacterID, r.input.Conversation, FaultRunState, settledErr, repair{kind: repairRunState, run: out.TurnID, state: kernel.RunSettled, outcome: state, drained: true})
	}
	r.snapshot.State, r.snapshot.Drained, r.snapshot.Outcome = kernel.RunSettled, true, state // 和库里写的结局一致
	// 先重排收尾时才到的插话，再通知订阅者：终态快照里要带上它们，界面才能跟上新排出的轮。
	requeued := h.requeueSteersLocked(r)
	h.wakeRunLocked(r, true)
	delete(lc.active, r.record.Domain)
	h.startInputLocked(r.record.Domain)
	lc.mu.Unlock()
	for _, event := range requeued {
		if h.Cfg.OnSteer != nil {
			h.Cfg.OnSteer(event)
		}
	}
	// 收尾错误同时交给本轮调用方和 Settled；这个会话已记成故障，恢复之前不再受理它的新输入。
	out.Err = errors.Join(out.Err, drainingErr, drainErr, settledErr)
	if settled.TurnID == "" {
		settled = Settled{Identity: in.Identity, TurnID: out.TurnID, CharacterID: in.CharacterID, Conversation: in.Conversation, Scope: in.Scope, Audience: in.Audience, Scenes: in.Scenes, Outcome: string(state), Err: out.Err, Auto: in.Auto}
	}
	settled.Err = out.Err
	h.plugins.settle(settled)
	lc.mu.Lock()
	lc.retainCompletedRunLocked(r)
	r.outcome <- out
	lc.mu.Unlock()
}

func (h *Hub) takeInputSteers(run string) ([]steer, error) {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	q := lc.steers[run]
	if len(q) == 0 {
		return nil, nil
	}
	out := make([]steer, 0, len(q))
	ids := make([]string, 0, len(q))
	for _, row := range q {
		var in Input
		if err := json.Unmarshal([]byte(row.Payload), &in); err != nil {
			return nil, err
		}
		out = append(out, steer{id: row.Identity.InputID, text: in.Text, seqs: in.Seqs, parts: in.Parts})
		ids = append(ids, row.Identity.InputID)
	}
	if err := h.trace().AdmitKernelInputs(context.Background(), ids); err != nil {
		return nil, err
	}
	delete(lc.steers, run)
	return out, nil
}
func (h *Hub) requeueSteersLocked(r *inputRun) []SteerEvent {
	lc := h.lifecycle
	var events []SteerEvent
	domain := r.record.Domain
	for _, row := range lc.steers[r.input.Identity.RunID] {
		if lc.sealed.Load() {
			continue
		}
		var in Input
		if err := json.Unmarshal([]byte(row.Payload), &in); err != nil {
			// 只拒这一条：载荷解不开，别的输入和这个会话都不受影响
			events = append(events, h.rejectBadInputLocked(r, row, err))
			continue
		}
		if lc.faultFor(domain) != nil {
			// 这个会话已经出故障（刚才收尾写库失败）：不重排，话在库里改成 held，等重试，和重启恢复同一个结果
			lc.addRepairsLocked(domain, repair{kind: repairHold, input: row.Identity.InputID})
			events = append(events, SteerEvent{Phase: "dropped", ID: row.Identity.InputID, Text: in.Text, Requeue: false})
			continue
		}
		in.Identity.RunID = store.NewID(h.Cfg.Now())
		in.Identity.RootID = in.Identity.RunID
		in.Identity.Generation++
		row.Identity, row.Mode = in.Identity, kernel.InputQueue
		if err := h.trace().RequeueKernelInput(context.Background(), row); err != nil {
			if !errors.Is(err, store.ErrInputConflict) {
				// 写不进：库里这句话还停在旧运行上，和内存对不上，这个会话进故障；重试把它改成 held
				lc.raiseLocked(time.Now(), FaultScopeConversation, r.input.CharacterID, r.input.Conversation, FaultRequeue, err, repair{kind: repairHold, input: row.Identity.InputID})
			}
			// 已经不是待处理（ErrInputConflict）：只拒这一条
			events = append(events, SteerEvent{Phase: "dropped", ID: row.Identity.InputID, Text: in.Text, Requeue: false})
			continue
		}
		// 重排只换运行身份；排队耗时仍从这条输入最初受理时算起。
		if err := AdmitBudgetRoot(context.Background(), h.Cfg.Budget, in.Identity, h.Cfg.BudgetConfig.LimitsForInput(in.Identity.Scope, in.Auto, time.UnixMilli(row.AcceptedAt))); err != nil {
			events = append(events, h.requeueFailedLocked(r, row, in, err))
			continue
		}
		if !in.local() {
			events = append(events, SteerEvent{Phase: "dropped", ID: row.Identity.InputID, Text: in.Text, Requeue: true})
			continue
		}
		if err := h.enqueueAcceptedLocked(in, row); err != nil {
			events = append(events, h.requeueFailedLocked(r, row, in, err))
			continue
		}
		var seq uint64
		if len(in.Seqs) > 0 {
			seq = uint64(in.Seqs[0])
		}
		if r.snapshot.Requeued == nil {
			r.snapshot.Requeued = &RequeuedInputs{}
		}
		r.snapshot.Requeued.Inputs = append(r.snapshot.Requeued.Inputs, RequeuedInput{InputID: row.Identity.InputID, RunID: in.Identity.RunID, Text: in.Text, Seq: seq})
	}
	delete(lc.steers, r.input.Identity.RunID)
	return events
}

// rejectBadInputLocked 只拒这一条：载荷读不出的输入标成已取消、记日志、让界面看到它被丢了。
// 标取消又写不进，才算这个会话的状态写不进（进故障，重试再标）。
func (h *Hub) rejectBadInputLocked(r *inputRun, row store.KernelInput, cause error) SteerEvent {
	h.plugins.logf("输入 %s 的载荷读不出，只拒这一条：%v", row.Identity.InputID, cause)
	if err := h.trace().CancelKernelInput(context.Background(), row.Identity.InputID); err != nil && !errors.Is(err, store.ErrInputConflict) {
		h.lifecycle.raiseLocked(time.Now(), FaultScopeConversation, r.input.CharacterID, r.input.Conversation, FaultRequeue, err, repair{kind: repairCancel, input: row.Identity.InputID})
	}
	return SteerEvent{Phase: "dropped", ID: row.Identity.InputID, Requeue: false}
}

// requeueFailedLocked：重排时库里已经改成了新的排队运行，内存却没排上（预算登记或排队失败）。
// 这个会话的记录和实际对不上，进故障；重试把这句话改成 held、把那条排队运行记成被打断。
// 程序在退出（Seal）造成的失败不算：下次启动的恢复会核对。
func (h *Hub) requeueFailedLocked(r *inputRun, row store.KernelInput, in Input, err error) SteerEvent {
	if !errors.Is(err, runstate.ErrSealed) {
		h.lifecycle.raiseLocked(time.Now(), FaultScopeConversation, r.input.CharacterID, r.input.Conversation, FaultRequeue, err,
			repair{kind: repairHold, input: row.Identity.InputID},
			repair{kind: repairRunState, run: in.Identity.RunID, state: kernel.RunSettled, outcome: kernel.OutcomeInterrupted, drained: true})
	}
	return SteerEvent{Phase: "dropped", ID: row.Identity.InputID, Text: in.Text, Requeue: false}
}

func (h *Hub) StopRun(ctx context.Context, req kernel.StopRequest) (kernel.StopReceipt, error) {
	lc := h.lifecycle
	lc.mu.Lock()
	var dropped []SteerEvent
	defer func() {
		lc.mu.Unlock()
		for _, event := range dropped {
			if h.Cfg.OnSteer != nil {
				h.Cfg.OnSteer(event)
			}
		}
	}()
	r := lc.runs[req.RunID]
	if r == nil || r.input.Identity.Scope != req.Scope {
		return kernel.StopReceipt{}, store.ErrInputConflict
	}
	if !r.snapshot.Drained {
		markStoppingLocked(r)
		r.cancel()
		h.wakeRunLocked(r, true)
	}
	for _, p := range lc.pending[r.record.Domain] {
		if p.input.Identity.Scope == req.Scope && p.record.Sequence <= req.ThroughSequence {
			p.cancel()
		}
	}
	for run, rows := range lc.steers {
		kept := rows[:0]
		for _, row := range rows {
			if row.Identity.Scope != req.Scope || row.Domain != r.record.Domain || row.Sequence > req.ThroughSequence {
				kept = append(kept, row)
				continue
			}
			var input Input
			if err := json.Unmarshal([]byte(row.Payload), &input); err != nil {
				// 只拒这一条：下面的 CancelKernelInputs 会把它取消，这里只记日志
				h.plugins.logf("输入 %s 的载荷读不出，停止时只丢掉这一条：%v", row.Identity.InputID, err)
			}
			dropped = append(dropped, SteerEvent{Phase: "dropped", ID: row.Identity.InputID, Text: input.Text, Requeue: false})
		}
		lc.steers[run] = kept
	}
	// 取消信号不能被慢写库挡住；回执仍等两份停止事实都写成，失败照实返回。
	err := h.trace().CancelKernelInputs(ctx, r.record.Domain, req.ThroughSequence, req.Scope)
	if !r.snapshot.Drained && r.snapshot.State == kernel.RunStopping {
		err = errors.Join(err, h.trace().KernelRunState(ctx, req.RunID, kernel.RunStopping, kernel.OutcomeStopped, false))
	}
	if err != nil {
		return kernel.StopReceipt{}, err
	}
	return kernel.StopReceipt{RunID: req.RunID, Revision: r.snapshot.Revision, Drained: r.snapshot.Drained}, nil
}
func (h *Hub) Seal() {
	lc := h.lifecycle
	if lc.sealed.Swap(true) {
		return
	}
	// 关机的路上不再新开空闲压缩：排着的计时器停掉；已经在跑的那一次由它自己的 ctx 取消、不写半截
	h.cancelAllIdleTimers()
	lc.manager.Seal()
	h.stop.stop()
	lc.mu.Lock()
	for _, r := range lc.runs {
		if !r.snapshot.Drained {
			markStoppingLocked(r)
			r.cancel()
			h.wakeRunLocked(r, true)
		}
	}
	lc.mu.Unlock()
}

// markStoppingLocked 只改还在生成的回合：生成已经结束、只剩工具收尾的（draining）结局已定，不改写成“被停下”。
func markStoppingLocked(r *inputRun) {
	if r.snapshot.State != kernel.RunDraining {
		r.snapshot.State, r.snapshot.Outcome = kernel.RunStopping, kernel.OutcomeStopped
	}
}

func (h *Hub) Drain(ctx context.Context) error {
	defer func() {
		if ctx.Err() != nil {
			h.stop.immediate()
		}
	}()
	if err := h.lifecycle.manager.Drain(ctx); err != nil {
		return err
	}
	lc := h.lifecycle
	for {
		lc.mu.Lock()
		var waiting *RunSubscription
		for _, r := range lc.runs {
			if !r.snapshot.Drained {
				waiting = h.subscribeRunLocked(r)
				break
			}
		}
		err := lc.faultsErrLocked()
		lc.mu.Unlock()
		if waiting == nil {
			if lc.sealed.Load() {
				h.stop.finish()
			}
			return err
		}
		select {
		case <-waiting.Wake():
			waiting.Close()
		case <-ctx.Done():
			waiting.Close()
			return ctx.Err()
		}
	}
}

// trustedInput 只在旧宿主薄入口使用：身份来自已入库事件，绝不从正文、昵称或 PrivateOK 推导主人。
func (h *Hub) trustedInput(in Input) (Input, error) {
	if in.Identity.Scope.CharacterID != "" {
		return in, validateInput(in)
	}
	platform, err := store.KernelPlatform(h.Stores, in.Audience.Channel)
	if err != nil {
		return in, err
	}
	if in.Audience.Scene == "" {
		in.Audience.Scene = "private"
		if strings.Contains(in.Conversation, ":group:") {
			in.Audience.Scene = "group"
		}
	}
	s := kernel.Scope{CharacterID: in.CharacterID, Conversation: in.Conversation, ScopeID: in.Scope, Platform: platform, Channel: in.Audience.Channel, Scene: in.Audience.Scene}
	if in.local() && len(in.Seqs) == 0 {
		s.ActorKind, s.ActorID = "owner", "owner"
	} else {
		st, err := h.storeOf(in.Conversation)
		if err != nil {
			return in, err
		}
		for _, seq := range in.Seqs {
			var kind, id string
			if err := st.Events.QueryRow(`SELECT actor_kind,actor_id FROM events WHERE seq=? AND conversation=? AND EXISTS(SELECT 1 FROM observations WHERE event_seq=events.seq AND character_id=?)`, seq, in.Conversation, in.CharacterID).Scan(&kind, &id); err != nil {
				return in, err
			}
			if s.ActorID == "" {
				s.ActorKind, s.ActorID = kind, id
			} else if s.ActorKind != kind || s.ActorID != id {
				s.ActorKind, s.ActorID = "mixed", "mixed"
			}
		}
	}
	// 这条薄入口只替 QQ 补接收账号。别的平台走到这里没有账号，validateInput 会因缺账号拒绝（不认识就拒绝）：
	// 新平台要从这里进，先在这里补它自己的接收账号，不套 QQ 群的 "channel" 占位（占位不是真账号，"发言人不是账号自己"的比较在它那里是空比较）
	if in.Audience.Channel == "qq" {
		parts := strings.Split(in.Conversation, ":")
		if len(parts) > 3 && parts[1] == "private" {
			s.AccountID = parts[2]
		} else {
			s.AccountID = "channel"
		}
	}
	in.Identity = kernel.Invocation{Scope: s, Kind: kernel.KindForeground}
	return in, validateInput(in)
}

func (h *Hub) submitOutcome(ctx context.Context, in Input, mode kernel.InputMode) <-chan Outcome {
	out := make(chan Outcome, 1)
	in, err := h.trustedInput(in)
	if err != nil {
		out <- Outcome{Err: err}
		return out
	}
	key := h.inputClientKey(in)
	if queued, claimed, err := h.claimQueuedInput(ctx, in, key); claimed {
		if err != nil {
			out <- Outcome{Err: err}
			return out
		}
		return queued
	}
	rec, err := h.Submit(ctx, Submission{Input: in, Mode: mode, ClientKey: key})
	if err != nil {
		out <- Outcome{Err: err}
		return out
	}
	lc := h.lifecycle
	lc.mu.Lock()
	r := lc.runs[rec.RunID]
	lc.mu.Unlock()
	if r == nil {
		out <- Outcome{TurnID: rec.RunID, Err: errors.New("输入已受理，需显式恢复或读取原运行状态")}
		return out
	}
	return r.outcome
}
