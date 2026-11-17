package turn

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/modelcall"
	"sumika-native/internal/store"
)

// 结局原因：主窗口把一轮的结局用日常说法写出来（错误不再只有“回复出错了”）。
// Class 是稳定的键（沿用 llm.ErrorClasses，另加 exit），Detail 是截短的原始错误，只给界面折叠区看。
const (
	ReasonExit = "exit" // 程序退出时被切断
	// ReasonLocal 是读卡、读提示词、工作目录这类本地文件出错；不是模型服务的问题，不能说成网络断了。
	ReasonLocal = "local"
	// detailRunes 是折叠区里最多留的字数：供应商的错误体可能很长。
	detailRunes = 300
)

// RunReason 说明一轮为什么这样结束。正常结束（done、手动停止）Class 为空。
type RunReason struct {
	Class  string
	Detail string
	Model  string // 出错那一步用的模型；不知道是空
}

// reasonOf 给结局定原因。err 取这一轮所有错误里的第一个，res 可空。
func (h *Hub) reasonOf(state kernel.Outcome, err error, res *Result) RunReason {
	switch state {
	case kernel.OutcomeStopped:
		if h.lifecycle.sealed.Load() {
			return RunReason{Class: ReasonExit}
		}
		return RunReason{}
	case kernel.OutcomeBudgetExhausted:
		return RunReason{Class: "budget_exhausted"}
	case kernel.OutcomeError:
	default:
		return RunReason{}
	}
	r := RunReason{Class: modelcall.ErrorClass(err)}
	var pe *fs.PathError
	var um *llm.UnknownModelError
	switch {
	case errors.As(err, &um):
		r.Class, r.Model = llm.ClassModelUnknown, um.ID
	case errors.As(err, &pe):
		// Windows 上 PathError 解开是 syscall.Errno，会被误认成网络错误，先归到本地。
		r.Class = ReasonLocal
	}
	if r.Class == "" {
		r.Class = "unknown"
	}
	if err != nil {
		r.Detail = truncate(err.Error(), detailRunes)
	}
	if r.Model == "" && res != nil && res.Built != nil {
		r.Model = res.Built.Model.ID
	}
	return r
}

func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

// ErrInputNotPending：这句话已经开始处理（或已经处理完），撤不回了。
var ErrInputNotPending = errors.New("这句话已经开始处理，撤不回了")

// HeldInput 是重启后没人处理的一句话。
type HeldInput struct {
	Scope      kernel.Scope
	InputID    string
	Text       string
	AcceptedAt time.Time
}

func heldOf(row store.KernelInput) (HeldInput, error) {
	var in Input
	if err := json.Unmarshal([]byte(row.Payload), &in); err != nil {
		return HeldInput{}, err
	}
	text := in.Text
	for _, p := range in.Parts {
		if text != "" && p.Text != "" {
			text += "\n"
		}
		text += p.Text
	}
	return HeldInput{Scope: row.Identity.Scope, InputID: row.Identity.InputID, Text: text, AcceptedAt: time.UnixMilli(row.AcceptedAt)}, nil
}

// HeldInputs 列出所有会话里 held 的本地输入（重启时还在排队、没处理的话），按受理顺序。
func (h *Hub) HeldInputs(ctx context.Context) ([]HeldInput, error) {
	tr := h.trace()
	if tr == nil {
		return nil, nil
	}
	rows, err := tr.HeldKernelInputs(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []HeldInput
	for _, row := range rows {
		if row.Identity.Scope.Channel != "console" {
			continue
		}
		v, err := heldOf(row)
		if err != nil {
			continue // 载荷读不出的行留在库里，不拿来显示
		}
		out = append(out, v)
	}
	return out, nil
}

func (h *Hub) ownedInput(ctx context.Context, scope kernel.Scope, id string) (store.KernelInput, error) {
	row, err := h.trace().KernelInputByID(ctx, id)
	if err != nil {
		return row, err
	}
	if !sameStableScope(row.Identity.Scope, scope) {
		return row, store.ErrInputConflict
	}
	return row, nil
}

// sameStableScope 只比稳定的归属字段。账号、授权号和授权版本会随绑定变化：
// 更早受理的行没有这些字段，授权被撤销时它们也会变空，整体比较会让这样的行既放不了也丢不掉。
func sameStableScope(a, b kernel.Scope) bool {
	return a.CharacterID == b.CharacterID && a.Conversation == b.Conversation && a.Platform == b.Platform &&
		a.Channel == b.Channel && a.Scene == b.Scene && a.ActorKind == b.ActorKind && a.ActorID == b.ActorID
}

// ReleaseHeld 放行一句 held 的话：沿用原来的输入号和原文，换一次新的排队运行，走和排队一样的内核路径。
func (h *Hub) ReleaseHeld(ctx context.Context, scope kernel.Scope, id string) (kernel.Receipt, error) {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if lc.sealed.Load() {
		return kernel.Receipt{}, errors.New("后台正在退出")
	}
	// 放行 held 的话也是一次受理：出事的会话在恢复之前不放行
	if fe := lc.faultFor(convKey(scope.CharacterID, scope.Conversation)); fe != nil {
		return kernel.Receipt{}, fe
	}
	if h.trace() == nil {
		return kernel.Receipt{}, errors.New("持久输入没有 trace 库")
	}
	row, err := h.ownedInput(ctx, scope, id)
	if err != nil {
		return kernel.Receipt{}, err
	}
	if row.State != kernel.InputHeld {
		return kernel.Receipt{}, store.ErrInputConflict
	}
	if lc.maintenance[row.Domain] {
		return kernel.Receipt{}, ErrInputBusy
	}
	var in Input
	if err := json.Unmarshal([]byte(row.Payload), &in); err != nil {
		return kernel.Receipt{}, err
	}
	if !in.local() {
		return kernel.Receipt{}, ErrInputAuthority
	}
	in.Identity.RunID = store.NewID(h.Cfg.Now())
	in.Identity.RootID = in.Identity.RunID
	in.Identity.Generation++
	// 用当前绑定的身份，不沿用旧行里的授权；时限从放行这一刻算，不从几小时前的受理算。
	in.Identity.Scope = scope
	row.Identity, row.Mode = in.Identity, kernel.InputQueue
	if err := AdmitBudgetRoot(ctx, h.Cfg.Budget, in.Identity, h.Cfg.BudgetConfig.Limits(time.Now())); err != nil {
		return kernel.Receipt{}, err
	}
	if err := h.trace().ReleaseHeldKernelInput(ctx, row); err != nil {
		return kernel.Receipt{}, err
	}
	row.State = kernel.InputAccepted
	row.Revision++
	if err := h.enqueueAcceptedLocked(in, row); err != nil {
		return kernel.Receipt{}, err
	}
	return receipt(row), nil
}

// DiscardHeld 放弃一句 held 的话：只把状态标成已取消，输入行和聊天里的原文都留着，不删数据。
func (h *Hub) DiscardHeld(ctx context.Context, scope kernel.Scope, id string) error {
	if h.trace() == nil {
		return errors.New("持久输入没有 trace 库")
	}
	row, err := h.ownedInput(ctx, scope, id)
	if err != nil {
		return err
	}
	if row.State != kernel.InputHeld {
		return store.ErrInputConflict
	}
	return h.trace().CancelKernelInput(ctx, id)
}

// WithdrawQueued 撤回一句还在排队、没轮到的话（和停止一样取消它那一轮；原文留在聊天记录里）。
// 已经开始处理的撤不回，返回 ErrInputNotPending。
func (h *Hub) WithdrawQueued(ctx context.Context, scope kernel.Scope, id string) error {
	lc := h.lifecycle
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if h.trace() == nil {
		return errors.New("持久输入没有 trace 库")
	}
	for _, p := range lc.pending[convKey(scope.CharacterID, scope.Conversation)] {
		if p.input.Identity.InputID != id || p.input.Identity.Scope != scope {
			continue
		}
		if err := h.trace().CancelKernelInput(ctx, id); err != nil {
			return err
		}
		p.cancel()
		return nil
	}
	return ErrInputNotPending
}
