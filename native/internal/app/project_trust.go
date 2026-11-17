package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/approval"
	"sumika-native/internal/policy"
	"sumika-native/internal/projectcontext"
	"sumika-native/internal/slash"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// projectTrust 是项目说明（AGENTS.md 等）的信任：配置里的信任根（projectContext.trustedRoots，手写的照旧算）
// 加上"问一次记住"——主人在审批卡上答过的，和别的"记住"同一份存储（tool-grants.yml，key 是 project:<真实路径>），
// 规则页能看、能撤，撤销下一次判定就生效。"允许一次"只管这次运行；"拒绝"在这次运行里不再问。
// 信任按工作区根的真实路径认（沿 .git 找到的那个目录），不按名字。
type projectTrust struct {
	static projectcontext.Trust // 配置里的信任根；没写就是 nil
	grants *approval.Grants

	mu       sync.Mutex
	session  map[string]bool // 允许一次：这次运行里算数
	declined map[string]bool // 拒绝或没理：这次运行里不再问
	asking   map[string]chan struct{}
}

func newProjectTrust(static projectcontext.Trust, grants *approval.Grants) *projectTrust {
	return &projectTrust{static: static, grants: grants, session: map[string]bool{}, declined: map[string]bool{}, asking: map[string]chan struct{}{}}
}

func projectTrustKey(real string) string { return "project:" + strings.ToLower(real) }

func (t *projectTrust) Trusted(ctx context.Context, w turn.Workspace) (bool, error) {
	if t.static != nil {
		ok, err := t.static.Trusted(ctx, w)
		if err != nil {
			return false, err // 信任根的身份变了：照旧严格，不退到别的来源
		}
		if ok {
			return true, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	pin, err := projectcontext.PinRoot(w.Root)
	if err != nil {
		return false, err
	}
	real := pin.Real()
	if t.grants != nil && t.grants.Match(projectTrustKey(real), "") {
		return true, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.session[strings.ToLower(real)], nil
}

// ask 问主人一次。同一个目录同时只问一次（别的会话在问就等它）；没有桌面在看就不问（没人能答，不空等）；
// 主人答"记住"由审批自己写进规则存储，这里只记"允许一次"和"拒绝"。
func (a *App) askProjectTrust(ctx context.Context, in turn.ContextIn, real string, files []string) {
	t := a.projectTrustDyn
	if d := a.desktopPtr.Load(); t == nil || d == nil || !d.watchingApprovals() {
		return
	}
	key := strings.ToLower(real)
	for {
		t.mu.Lock()
		if t.declined[key] || t.session[key] {
			t.mu.Unlock()
			return
		}
		wait, busy := t.asking[key]
		if !busy {
			t.asking[key] = make(chan struct{})
			t.mu.Unlock()
			break
		}
		t.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return
		}
	}
	defer func() {
		t.mu.Lock()
		close(t.asking[key])
		delete(t.asking, key)
		t.mu.Unlock()
	}()
	if ok, _ := t.Trusted(ctx, in.Workspace); ok { // 等的时候别处已经答过了
		return
	}
	g := a.approvalGate
	now := a.now()
	minutes := g.ApprovalMinutes
	if minutes <= 0 || minutes > 3 {
		minutes = 3 // 回合在等它回答，别让一个没人理的问题卡十分钟
	}
	var notes []string
	for _, f := range files {
		notes = append(notes, "发现："+f)
	}
	id := "project-trust:" + store.NewID(now)
	req := approval.Request{
		Identity: in.Identity, ID: id, CharacterID: in.CharacterID, Conversation: in.Conversation, ToolCallID: id,
		Tool: "project_trust", Title: "读取项目说明", Subject: real,
		Why:   "这个目录里有项目说明文件（AGENTS.md 等），读进去会影响她怎么做事、怎么用工具。信任它才会带上；不信任就当没有，其余照常。",
		Notes: notes, Options: []string{policy.ByAlways}, KindLabel: "「" + real + " 的项目说明」",
		CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Duration(minutes * float64(time.Minute))).UnixMilli(),
	}
	pending := &approval.Pending{Key: projectTrustKey(real), Label: "「" + real + " 的项目说明」", Tool: "project_trust", Conversation: in.Conversation}
	d := g.Ask(ctx, req, pending)
	if ctx.Err() != nil {
		return // 主人发了新消息或停了：不算拒绝，下一轮再问
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if d.Kind == "approved" {
		if d.By != policy.ByAlways { // 记住的已经由审批写进规则存储；一次的只管这次运行
			t.session[key] = true
		}
		return
	}
	t.declined[key] = true
}

// reask 让“拒绝或没理”过的目录下一轮重新问（/trust 用）。已经信任的不用问；返回之前是不是被拒过。
func (t *projectTrust) reask(real string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := strings.ToLower(real)
	was := t.declined[key]
	delete(t.declined, key)
	return was
}

// allowedOnce：这个目录这次运行里答过“允许一次”
func (t *projectTrust) allowedOnce(real string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.session[strings.ToLower(real)]
}

var trustSlashCommand = slash.Command{Name: "trust", Summary: "项目说明（AGENTS.md 等）：拒绝过的，想改主意就重新问一次"}

// slashTrust 是 /trust：对这个会话现在的工作区，把“这次运行里不再问”的拒绝清掉，下一次你发消息时她会重新弹那张
// “读取项目说明”的卡（允许一次 / 记住 / 拒绝）。不当场替你信任：信任只能是你在卡上点的。
// 只有主人在本地私聊输入框、或者 sumika-ctl（自检话题）能用；她自己说的话不过命令表。
func (a *App) slashTrust(ctx context.Context, c slashCall) (slashResult, error) {
	t := a.projectTrustDyn
	if t == nil {
		return slashResult{Reply: "项目说明没开（projectContext.enabled 是 false），没有可以信任的东西"}, nil
	}
	if strings.TrimSpace(c.Inv.Arg) != "" {
		return slashResult{}, errors.New("/trust 不带参数：它针对这个会话现在的工作目录")
	}
	info, err := a.workspaceInfo(ctx, c.Conv)
	if err != nil {
		return slashResult{}, err
	}
	if info.Problem != "" {
		return slashResult{}, errors.New("这个会话的工作目录现在用不了：" + info.Problem)
	}
	ws, err := turn.NewWorkspace(c.Conv, info.CWD)
	if err != nil {
		return slashResult{}, err
	}
	pin, err := projectcontext.PinRoot(ws.Root)
	if err != nil {
		return slashResult{}, errors.New("读不了工作区根 " + ws.Root + "：" + err.Error())
	}
	if ok, err := t.Trusted(ctx, ws); err != nil {
		return slashResult{}, errors.New("判断信任时出错：" + err.Error())
	} else if ok {
		switch {
		case t.grants != nil && t.grants.Match(projectTrustKey(pin.Real()), ""):
			return slashResult{Reply: pin.Real() + " 的项目说明已经信任了。要收回，去设置 · 工具与审批的放行规则里撤销"}, nil
		case t.allowedOnce(pin.Real()):
			return slashResult{Reply: pin.Real() + " 的项目说明这次运行里允许过一次了，重启后会再问；想以后都不问，下次弹卡时选“记住”"}, nil
		}
		return slashResult{Reply: pin.Real() + " 在配置的信任根（projectContext.trustedRoots）里，项目说明一直是信任的；要改去配置里改"}, nil
	}
	files := projectcontext.InstructionFiles(ws.Root, ws.CWD)
	if len(files) == 0 {
		return slashResult{Reply: pin.Real() + " 里没有项目说明文件（AGENTS.md、CLAUDE.md 等），不用信任"}, nil
	}
	t.reask(pin.Real())
	return slashResult{Reply: "好，你下一次发消息时会重新问一次要不要读 " + pin.Real() + " 的项目说明（发现 " + strconv.Itoa(len(files)) + " 个文件）。选“记住”就不再问"}, nil
}
