package turn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/store"
)

// 内核故障（质检 X-07）。
//
// 旧写法把所有收尾写库失败都攒进一个只增不减的 lc.err，见到非空整台后台拒收，没有恢复路径，别处也看不见。
// 现在每一处出错先分类，类别由类型表达，不看错误文字：
//
//   - 只拒这一条：单条输入、单条数据有问题（载荷解不开、这一句已经不是待处理状态）。拒掉这一条、如实报失败，
//     不进故障状态。
//   - 只挡这个会话（会话故障）：这个会话的状态写不进记录库，内核对它的记录和实际已经对不上。这个会话停止受理新输入，
//     别的会话照常；每处失败的写入都记成一条“待修复”，重试时重写并读回确认。
//   - 整个内核（内核故障）：只有启动时对整库的恢复和清扫没做成（RecoverKernelInputs、SweepInterrupted）：
//     哪些会话受影响没法知道，所有会话的基线都未知。
//
// 故障只放内存。重启时的恢复本来就会核对整库（没收尾的运行记成被打断、排着的输入记成待处理），所以不需要迁移，
// 重试到一半程序退出也安全。解除只有一个入口：RetryFault / RetryAllFaults，给主人和运维用（界面、sumika-ctl），
// 不在模型能调的工具里。

// FaultKind 是故障的来源，稳定的键；界面、自检按它归类。
type FaultKind string

const (
	FaultRunState       FaultKind = "run_state"       // 一轮结束时的状态没写进记录库
	FaultActivityEnd    FaultKind = "activity_end"    // 工具、子运行的退出没记进记录库
	FaultRequeue        FaultKind = "requeue"         // 中途插进来的话没能重新排队
	FaultAdmit          FaultKind = "admit"           // 一句话开始处理时，“已开始”没能记进库
	FaultStartupRecover FaultKind = "startup_recover" // 启动时恢复上次没处理完的输入没做成
	FaultStartupSweep   FaultKind = "startup_sweep"   // 启动时清扫上次没收尾的回合没做成
)

// reason 是给人看的一句话（日常说法，不带库名和字段名）。
func (k FaultKind) reason() string {
	switch k {
	case FaultRunState:
		return "上一轮结束时，状态没能写进记录库"
	case FaultActivityEnd:
		return "上一轮里工具和子任务的收尾没能记进记录库"
	case FaultRequeue:
		return "中途插进来的话没能重新排队"
	case FaultAdmit:
		return "一句话开始处理时，状态没能写进记录库"
	case FaultStartupRecover:
		return "启动时收拾上次没处理完的话没做成"
	case FaultStartupSweep:
		return "启动时收拾上次没收尾的回合没做成"
	}
	return "内核状态没能写进记录库"
}

// FaultScope 是故障的范围，显式的字段，不拿“会话号为空”当哨兵：空会话号的输入收尾失败也只是会话故障，不能拖垮全部。
type FaultScope string

const (
	FaultScopeConversation FaultScope = "conversation" // 只挡这个会话
	FaultScopeKernel       FaultScope = "kernel"       // 整个内核
)

// Fault 是一条故障的快照。Scope 说明范围；内核故障没有角色和会话。
type Fault struct {
	Scope        FaultScope
	Kind         FaultKind
	CharacterID  string
	Conversation string
	At           time.Time
	Reason       string // 日常说法一句话
	Detail       string // 最近一次的原始错误（截短），只给折叠区和日志看
	Tries        int    // 已经重试了几次
	LastTry      time.Time
}

// Kernel 说明这是整个内核的故障。
func (f Fault) Kernel() bool { return f.Scope == FaultScopeKernel }

var (
	// ErrConversationFaulted：这个会话处在故障状态，输入被拒；解除靠 RetryFault。
	ErrConversationFaulted = errors.New("这个对话暂时没法继续")
	// ErrKernelFaulted：整个内核处在故障状态，所有输入被拒；解除靠 RetryFault。
	ErrKernelFaulted = errors.New("后台暂时不能受理新的输入")
	// ErrNoFault：重试的目标没有故障。
	ErrNoFault = errors.New("没有要重试的故障")
	// ErrRetryBusy：这个故障正在重试，或这个会话还有一轮在收尾，现在不能重试。
	ErrRetryBusy = errors.New("还有一轮在收尾或正在重试，稍后再试")
)

// FaultError 是故障期间被拒的输入收到的错误：说清是谁出了什么事，不报成别的。
type FaultError struct {
	Fault Fault
	err   error // 原始错误，只给日志和详情看（Cause）；不 Unwrap：故障错误不能被 errors.Is 认成 context.Canceled 之类，控制流别走错
}

func (e *FaultError) Error() string {
	head := ErrConversationFaulted.Error()
	if e.Fault.Kernel() {
		head = ErrKernelFaulted.Error()
	}
	s := head + "：" + e.Fault.Reason
	if e.Fault.Detail != "" {
		s += "（" + e.Fault.Detail + "）"
	}
	return s
}

func (e *FaultError) Is(target error) bool {
	if e.Fault.Kernel() {
		return target == ErrKernelFaulted
	}
	return target == ErrConversationFaulted
}

// Cause 是原始错误，只给日志和详情看，不参与控制流（FaultError 故意不 Unwrap：
// 原始错误可能是 context.Canceled，让 errors.Is 认成取消会让调用方把“被故障拒掉”当成“被取消”。）
func (e *FaultError) Cause() error { return e.err }

type repairKind int

const (
	repairRunState    repairKind = iota // 把这一轮的状态重写成 state/outcome/drained 并读回
	repairActivityEnd                   // 重写没记进库的活动退出
	repairHold                          // 已受理但没能往下处理的话，标成 held（和重启恢复同一个结果）
	repairCancel                        // 载荷坏掉的话，标成已取消
	repairRecover                       // 启动恢复（整库）
	repairSweep                         // 启动清扫（整库）
)

// repair 是一次没写成的写入，重试时照原样再做一遍。
type repair struct {
	kind    repairKind
	run     string
	state   kernel.RunState
	outcome kernel.Outcome
	drained bool
	input   string
	at      int64
}

type faultState struct {
	Fault
	domain   string
	err      error
	repairs  []repair
	retrying bool
}

func (f *faultState) fault() Fault { return f.Fault }

func (f *faultState) faultError() *FaultError { return &FaultError{Fault: f.Fault, err: f.err} }

// faultKey 是会话故障的键，和输入箱的会话域同一个。
func faultKey(characterID, conversation string) string { return convKey(characterID, conversation) }

// faultFor 在锁里查这个会话域的故障：内核故障优先，没有再看会话自己的。没有返回 nil。
func (lc *hubLifecycle) faultFor(domain string) *FaultError {
	if lc.kernelFault != nil {
		return lc.kernelFault.faultError()
	}
	if f := lc.faults[domain]; f != nil {
		return f.faultError()
	}
	return nil
}

// faultsErrLocked 把所有没解除的故障合成一个错误（Drain 用：退出时有没解除的故障，就保留库和数据锁）。
func (lc *hubLifecycle) faultsErrLocked() error {
	var errs []error
	if lc.kernelFault != nil {
		errs = append(errs, lc.kernelFault.faultError())
	}
	keys := make([]string, 0, len(lc.faults))
	for k := range lc.faults {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		errs = append(errs, lc.faults[k].faultError())
	}
	return errors.Join(errs...)
}

// raiseLocked 记一次故障；同一处已经有故障就追加原因和待修复项，保留最早的时间。范围由 scope 决定，不看会话号是不是空。
func (lc *hubLifecycle) raiseLocked(now time.Time, scope FaultScope, characterID, conversation string, kind FaultKind, err error, repairs ...repair) {
	var f *faultState
	domain := ""
	if scope == FaultScopeKernel {
		f = lc.kernelFault
	} else {
		domain = faultKey(characterID, conversation)
		f = lc.faults[domain]
	}
	if f == nil {
		f = &faultState{Fault: Fault{Scope: scope, Kind: kind, CharacterID: characterID, Conversation: conversation, At: now, Reason: kind.reason()}, domain: domain}
		if scope == FaultScopeKernel {
			lc.kernelFault = f
		} else {
			lc.faults[domain] = f
		}
	}
	f.err = errors.Join(f.err, err)
	f.Detail = truncate(oneLine(f.err), detailRunes)
	f.repairs = append(f.repairs, repairs...)
}

// addRepairsLocked 给已经存在的会话故障追加待修复项（比如故障期间被拒的排队输入要改成 held）。
func (lc *hubLifecycle) addRepairsLocked(domain string, repairs ...repair) {
	f := lc.faults[domain]
	if f == nil {
		f = lc.kernelFault
	}
	if f != nil {
		f.repairs = append(f.repairs, repairs...)
	}
}

func oneLine(err error) string {
	if err == nil {
		return ""
	}
	return strings.ReplaceAll(err.Error(), "\n", "；")
}

// Faults 列出现在所有没解除的故障（内核的在前，其余按时间）。给自检、界面、sumika-ctl 看。
func (h *Hub) Faults() []Fault {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	var out []Fault
	if lc.kernelFault != nil {
		out = append(out, lc.kernelFault.fault())
	}
	rest := make([]Fault, 0, len(lc.faults))
	for _, f := range lc.faults {
		rest = append(rest, f.fault())
	}
	sort.Slice(rest, func(i, j int) bool {
		if !rest[i].At.Equal(rest[j].At) {
			return rest[i].At.Before(rest[j].At)
		}
		return rest[i].Conversation < rest[j].Conversation
	})
	return append(out, rest...)
}

// FaultOf 查某个会话现在受不受故障影响：会话自己的故障，或整个内核的。
func (h *Hub) FaultOf(characterID, conversation string) (Fault, bool) {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if e := lc.faultFor(faultKey(characterID, conversation)); e != nil {
		return e.Fault, true
	}
	return Fault{}, false
}

// RetryFault 重试一个故障：characterID、conversation 都空是整个内核的那个。
// 重新做每一处没写成的写入并读回确认；全部确认才解除（nil）。没恢复就返回带着新原因的 *FaultError，故障保持，不说成功。
// 这是主人和运维的操作，不要接进模型能调的工具。
func (h *Hub) RetryFault(ctx context.Context, characterID, conversation string) error {
	if (characterID == "") != (conversation == "") {
		return errors.New("会话要同时给出角色和会话号")
	}
	scope := FaultScopeConversation
	if characterID == "" {
		scope = FaultScopeKernel
	}
	return h.retryFault(ctx, scope, characterID, conversation)
}

func (h *Hub) retryFault(ctx context.Context, scope FaultScope, characterID, conversation string) error {
	lc := h.lifecycle
	tr := h.trace()
	lc.mu.Lock()
	if lc.sealed.Load() {
		lc.mu.Unlock()
		return errors.New("后台正在退出，重试没有做；下次启动会重新核对")
	}
	var f *faultState
	domain := ""
	if scope == FaultScopeKernel {
		f = lc.kernelFault
	} else {
		domain = faultKey(characterID, conversation)
		f = lc.faults[domain]
	}
	if f == nil {
		lc.mu.Unlock()
		return ErrNoFault
	}
	if f.retrying || h.liveRunsLocked(scope == FaultScopeKernel, domain) {
		lc.mu.Unlock()
		return ErrRetryBusy
	}
	if tr == nil {
		lc.mu.Unlock()
		return errors.New("持久输入没有 trace 库")
	}
	f.retrying = true
	todo := append([]repair(nil), f.repairs...)
	lc.mu.Unlock()

	// 写库不拿 lc.mu；故障还在，新输入照样被拒，重试期间没有新写入插进来。
	var left []repair
	var errs []error
	for _, r := range todo {
		if err := h.redo(ctx, tr, r); err != nil {
			left = append(left, r)
			errs = append(errs, err)
		}
	}

	lc.mu.Lock()
	defer lc.mu.Unlock()
	f.retrying = false
	f.Tries++
	f.LastTry = time.Now()
	// 重试期间可能又追加了新的待修复项（按下标之后的部分），不丢
	added := f.repairs[len(todo):]
	f.repairs = append(left, added...)
	if len(f.repairs) == 0 && len(errs) == 0 {
		if scope == FaultScopeKernel {
			lc.kernelFault = nil
		} else {
			delete(lc.faults, domain)
		}
		h.plugins.logf("内核故障已解除（%s，重试 %d 次）", f.Kind, f.Tries)
		return nil
	}
	f.err = errors.Join(errs...)
	f.Detail = truncate(oneLine(f.err), detailRunes)
	return fmt.Errorf("没有恢复：%w", f.faultError())
}

// RetryFaultOf 重试正在挡着这个会话的那个故障（内核故障优先：它挡着所有会话）。没有故障返回 ErrNoFault。
// 界面里出事的对话上的“重试”用它：界面只认识自己的会话，不必知道挡着它的是会话故障还是内核故障。
func (h *Hub) RetryFaultOf(ctx context.Context, characterID, conversation string) error {
	f, ok := h.FaultOf(characterID, conversation)
	if !ok {
		return ErrNoFault
	}
	return h.retryFault(ctx, f.Scope, f.CharacterID, f.Conversation)
}

// RetryReport 是 RetryAllFaults 的结果。
type RetryReport struct {
	Cleared   []Fault
	Remaining []Fault
	// StrayEnds 是重试后仍没写成的“没人认领的活动退出记录”条数（压缩、检查点、记忆这类不属于前台运行的活动）；它们不挡受理。
	StrayEnds int
}

// RetryAllFaults 逐个重试所有故障（内核的先）。全部解除返回 nil；否则返回还没恢复的那些，原因照实。
func (h *Hub) RetryAllFaults(ctx context.Context) (RetryReport, error) {
	var rep RetryReport
	var errs []error
	for _, f := range h.Faults() {
		err := h.retryFault(ctx, f.Scope, f.CharacterID, f.Conversation)
		switch {
		case err == nil, errors.Is(err, ErrNoFault):
			rep.Cleared = append(rep.Cleared, f)
		default:
			if cur, ok := h.faultByScope(f.Scope, f.CharacterID, f.Conversation); ok {
				rep.Remaining = append(rep.Remaining, cur)
			}
			errs = append(errs, err)
		}
	}
	// 没人认领的活动退出记录（不属于任何会话故障）：一起重写，写不成的只报条数，不挡受理
	if h.lifecycle.manager.EndFailures("") > 0 && !h.lifecycle.sealed.Load() {
		if err := h.lifecycle.manager.RetryEnds(ctx, ""); err != nil {
			errs = append(errs, fmt.Errorf("还有活动退出的记录没写进库：%w", err))
		}
	}
	rep.StrayEnds = h.StrayEndFailures()
	return rep, errors.Join(errs...)
}

// StrayEndFailures 是没挂在任何会话故障上的、没记进库的活动退出记录条数（比如父轮落定后才退出的子运行，
// 压缩、检查点、记忆这类不属于前台运行的活动）。它们不挡受理，自检 S10 里提一句，RetryAllFaults 时一起重写。
func (h *Hub) StrayEndFailures() int {
	lc := h.lifecycle
	lc.mu.Lock()
	owned := map[string]bool{}
	for _, f := range lc.faults {
		for _, r := range f.repairs {
			if r.kind == repairActivityEnd {
				owned[r.run] = true
			}
		}
	}
	lc.mu.Unlock()
	return lc.manager.EndFailuresExcept(owned)
}

// FaultExact 查这一个故障本身：characterID、conversation 都空是内核的，否则是这个会话自己的（不含内核故障）。
func (h *Hub) FaultExact(characterID, conversation string) (Fault, bool) {
	scope := FaultScopeConversation
	if characterID == "" && conversation == "" {
		scope = FaultScopeKernel
	}
	return h.faultByScope(scope, characterID, conversation)
}

func (h *Hub) faultByScope(scope FaultScope, characterID, conversation string) (Fault, bool) {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if scope == FaultScopeKernel {
		if lc.kernelFault != nil {
			return lc.kernelFault.fault(), true
		}
		return Fault{}, false
	}
	if f := lc.faults[faultKey(characterID, conversation)]; f != nil {
		return f.fault(), true
	}
	return Fault{}, false
}

// liveRunsLocked：这个会话域还有没收尾的运行（kernel 为真 = 整个内核：还有任何运行、活动在跑）。
// 重试要在没人写这些行的时候做；整库的恢复和清扫再做一次会把正在跑的当成上一个进程没收尾的。
func (h *Hub) liveRunsLocked(kernel bool, domain string) bool {
	lc := h.lifecycle
	for _, r := range lc.runs {
		if !r.snapshot.Drained && (kernel || r.record.Domain == domain) {
			return true
		}
	}
	if kernel {
		return len(lc.internal) > 0 || lc.manager.Active() > 0
	}
	return len(lc.pending[domain]) > 0 || lc.active[domain] != nil
}

// redo 把一处没写成的写入再做一遍，并读回确认。
func (h *Hub) redo(ctx context.Context, tr *store.Store, r repair) error {
	switch r.kind {
	case repairRunState:
		if err := tr.KernelRunState(ctx, r.run, r.state, r.outcome, r.drained); err != nil {
			return err
		}
		state, _, drained, err := tr.KernelRunRow(ctx, r.run)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil // 库里没有这一行，没有对不上的记录
		case err != nil:
			return err
		case drained, !r.drained && state == r.state:
			return nil
		}
		return fmt.Errorf("运行 %s 的状态没写进去：库里还是 %s", r.run, state)
	case repairActivityEnd:
		return h.lifecycle.manager.RetryEnds(ctx, r.run)
	case repairHold, repairCancel:
		var err error
		if r.kind == repairHold {
			err = tr.HoldKernelInput(ctx, r.input)
		} else if err = tr.CancelKernelInput(ctx, r.input); errors.Is(err, store.ErrInputConflict) {
			err = nil // 已经不是待处理，读回再看
		}
		if err != nil {
			return err
		}
		row, err := tr.KernelInputByID(ctx, r.input)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return err
		case row.State == kernel.InputAccepted:
			return fmt.Errorf("输入 %s 的状态没写进去：库里还是 accepted", r.input)
		}
		return nil
	case repairRecover:
		return tr.RecoverKernelInputs(ctx)
	case repairSweep:
		return h.sweepInterrupted(tr, r.at)
	}
	return fmt.Errorf("不认识的修复项 %d", r.kind)
}

// sweepInterrupted 清扫上一个进程没收尾的回合（启动时和重试都走它）；at 是启动那一刻。
func (h *Hub) sweepInterrupted(st *store.Store, at int64) error {
	sw, err := st.SweepInterrupted(at)
	if err == nil && sw.Turns > 0 {
		h.surfaceSwept(st, at)
	}
	switch {
	case err != nil:
		h.plugins.fail("收拾上一个进程没收尾的回合出错：%v", err)
	case sw != (store.TurnsSwept{}):
		h.plugins.logf("上一个进程没收尾：%d 个回合记成被打断，%d 个工具不知道结果，%d 个审批记成取消", sw.Turns, sw.Unknown, sw.Waiting)
	}
	return err
}
