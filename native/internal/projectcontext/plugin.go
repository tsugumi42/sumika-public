package projectcontext

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"sumika-native/internal/approval"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// Trust 由宿主提供；以后可换成界面保存的信任记录。
type Trust interface {
	Trusted(context.Context, turn.Workspace) (bool, error)
}

type RootsTrust []string

func (roots RootsTrust) Trusted(ctx context.Context, w turn.Workspace) (bool, error) {
	t, err := NewRootsTrust(roots)
	if err != nil {
		return false, err
	}
	return t.Trusted(ctx, w)
}

// realStrict 是解受保护目录真实路径用的函数；放成变量只为测试能数它被调了几次
var realStrict = approval.RealStrict

// ReadApproval 不发审批卡；受保护资料在具备上下文审批入口之前一律拒绝。
// protected 是取受保护目录清单的函数，不是清单的快照：每次读都现取，装配时晚一步并进审批门的目录（备份目录）也算数；nil 表示没有受保护目录
func ReadApproval(protected func() []string) func(context.Context, string) error {
	return func(ctx context.Context, real string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var dirs []string
		if protected != nil {
			dirs = protected()
		}
		if policy.TouchesProtected(real, filepath.Dir(real), dirs, false) {
			return fmt.Errorf("受保护路径的读取审批未通过；需要宿主提供当次上下文读取审批")
		}
		for _, dir := range dirs {
			if policy.IsNetworkSharePath(dir) { // 网络共享：不解析（解析会真的连过去，每读一个项目说明多一次网络超时）；字面比对上面已经做过。
				continue // 本机的设备路径（\\?\D:\、\\.\D:\、\\?\Volume{GUID}\）不在这里：它们落在本机的盘上，要解析成普通路径才比得上受保护目录
			}
			resolved, err := realStrict(dir)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("无法确认受保护目录，读取审批未通过：%w", err)
			}
			if policy.TouchesProtected(real, filepath.Dir(real), []string{resolved}, false) {
				return fmt.Errorf("受保护路径的读取审批未通过；需要宿主提供当次上下文读取审批")
			}
		}
		return nil
	}
}

type PluginOptions struct {
	Enabled  bool
	Trust    Trust
	UserRoot string
	Resolver Options
	// Ask 在工作区还没有信任、而且里面真有说明文件（AGENTS.md 等）时问主人一次，问完以后 Trust 会重新判。
	// 它可以等主人回答（回合开始前），ctx 取消就返回；不设就不问，只看 Trust（配置里的信任根）。
	Ask func(ctx context.Context, in turn.ContextIn, root string, files []string)
}

// 工作区说明文件的名字（和 Resolver 发现时用的一致：本地、覆盖、通用；Fallback 名字不用来触发提问）。
var instructionNames = []string{"AGENTS.override.md", "AGENTS.md", "CLAUDE.md"}

// InstructionFiles 是从工作目录一路向上到工作区根（含）之间，真有的说明文件（普通文件）的路径。
// 只用来判断“值不值得问一次信任”，不读内容。
func InstructionFiles(root, cwd string) []string {
	var out []string
	dir := cwd
	if dir == "" {
		dir = root
	}
	for i := 0; i < 64; i++ {
		for _, n := range instructionNames {
			p := filepath.Join(dir, n)
			if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
				out = append(out, p)
			}
		}
		if samePath(dir, root) || !inside(root, dir) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return out
}

type workspaceKey struct{ ID, Root, CWD string }
type sessionKey struct{ Character, Conversation, Scope string }
type binding struct {
	key      workspaceKey
	resolver *Resolver
	ledger   *Ledger
	dirs     map[string]bool
	err      error
	bypass   bool
	pin      *RootIdentity
}

// Plugin 放在 projectcontext 包，app 只提供信任、读取政策和配置。
// 缓存按工作区共享，接纳账本和触及目录按 ContextID 隔离。
type Plugin struct {
	opts      PluginOptions
	mu        sync.Mutex
	active    bool
	resolvers map[workspaceKey]*Resolver
	sessions  map[sessionKey]string
	contexts  map[string]*binding
}

func NewPlugin(o PluginOptions) *Plugin {
	if roots, ok := o.Trust.(RootsTrust); ok {
		t, err := NewRootsTrust(roots)
		if err != nil {
			t = &pinnedTrust{err: err}
		}
		o.Trust = t
	}
	return &Plugin{opts: o, resolvers: make(map[workspaceKey]*Resolver), sessions: make(map[sessionKey]string), contexts: make(map[string]*binding)}
}
func (*Plugin) ID() string { return "projectcontext" }
func (p *Plugin) Setup(reg *turn.Registry) (func(), error) {
	p.mu.Lock()
	p.active = true
	p.mu.Unlock()
	reg.OnContext(p.onContext)
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.active = false
		for id, b := range p.contexts {
			b.ledger.Forget(id)
		}
		clear(p.contexts)
		clear(p.sessions)
		clear(p.resolvers)
	}, nil
}

func (p *Plugin) onContext(ctx context.Context, in turn.ContextIn) (*turn.Contribution, error) {
	if !p.opts.Enabled || !localPrivate(in) {
		return nil, nil
	}
	var pin *RootIdentity
	var err error
	trusted := false
	if p.opts.Trust != nil {
		pin, err = PinRoot(in.Workspace.Root)
		if err == nil {
			trusted, err = p.opts.Trust.Trusted(ctx, in.Workspace)
			if err == nil && !trusted && p.opts.Ask != nil {
				// 没有信任、里面又有说明文件：问一次（答过的记住，不再问）；问之前不持锁，等的是主人
				if files := InstructionFiles(in.Workspace.Root, in.Workspace.CWD); len(files) > 0 {
					p.opts.Ask(ctx, in, pin.real, files)
					trusted, err = p.opts.Trust.Trusted(ctx, in.Workspace)
				}
			}
		}
	} else {
		err = fmt.Errorf("没有工作区信任来源；配置受信任根后重新加载")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active || ctx.Err() != nil {
		return nil, ctx.Err()
	}
	key := workspaceKey{in.Workspace.ID, in.Workspace.Root, in.Workspace.CWD}
	session := sessionKey{in.CharacterID, in.Conversation, in.Scope}
	if old := p.sessions[session]; old != "" && old != in.ContextID {
		if b := p.contexts[old]; b != nil {
			b.ledger.Forget(old)
			delete(p.contexts, old)
		}
	}
	p.sessions[session] = in.ContextID
	if err == nil && trusted {
		err = pin.Check(in.Workspace.Root)
	}
	if err != nil || !trusted {
		p.contexts[in.ContextID] = &binding{key: key, ledger: NewLedger(), err: err, bypass: err == nil}
		return nil, err
	}
	b := p.contexts[in.ContextID]
	if b == nil || b.resolver == nil {
		r := p.resolvers[key]
		if r == nil {
			o := p.opts.Resolver
			o.Root, o.CWD, o.UserRoot = key.Root, key.CWD, p.opts.UserRoot
			o.RootIdentity = pin
			r, err = New(o)
			if err != nil {
				p.contexts[in.ContextID] = &binding{key: key, ledger: NewLedger(), err: err}
				return nil, err
			}
			p.resolvers[key] = r
		}
		b = &binding{key: key, resolver: r, ledger: NewLedger(), dirs: map[string]bool{key.CWD: true}, pin: pin}
		p.contexts[in.ContextID] = b
	}
	var lines []string
	var commits []func()
	// Next 核对 cwd；额外触及目录排序，保证请求顺序稳定。
	dirs := make([]string, 0, len(b.dirs))
	for dir := range b.dirs {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)
	for _, dir := range dirs {
		_, c := b.resolver.NextTo(ctx, in.ContextID, b.ledger, dir)
		if c != nil {
			lines = append(lines, c.Lines...)
			if c.OnAccepted != nil {
				commits = append(commits, c.OnAccepted)
			}
		}
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return &turn.Contribution{Lines: lines, OnAccepted: p.accept(in.ContextID, b, func() {
		for _, commit := range commits {
			commit()
		}
	})}, nil
}

func (p *Plugin) accept(id string, b *binding, commit func()) func() {
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.active && p.contexts[id] == b && commit != nil {
			commit()
		}
	}
}

func (p *Plugin) valid(id string, b *binding) func() bool {
	return func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.active && p.contexts[id] == b
	}
}

func (p *Plugin) Execute(ctx context.Context, in turn.ContextIn, name string, args jsjson.Value, exec func(context.Context) (tools.Result, error)) (tools.Result, error) {
	if !p.opts.Enabled || !localPrivate(in) || (name != "read" && name != "ls" && name != "write" && name != "edit") {
		return exec(ctx)
	}
	p.mu.Lock()
	b := p.contexts[in.ContextID]
	active := p.active
	p.mu.Unlock()
	write := name == "write" || name == "edit"
	if active && b != nil && b.bypass && b.key == (workspaceKey{in.Workspace.ID, in.Workspace.Root, in.Workspace.CWD}) {
		return exec(ctx)
	}
	var guard error
	if !active || b == nil || b.resolver == nil || b.key != (workspaceKey{in.Workspace.ID, in.Workspace.Root, in.Workspace.CWD}) {
		guard = fmt.Errorf("工作区 %q 没有成功绑定；恢复根路径并重新开始回合", in.Workspace.Root)
		if b != nil && b.err != nil {
			guard = b.err
		}
	} else {
		trusted, err := p.opts.Trust.Trusted(ctx, in.Workspace)
		guard = err
		if guard == nil && !trusted {
			guard = fmt.Errorf("工作区 %q 不再受信任；恢复路径后重新开始回合", in.Workspace.Root)
		}
		if guard == nil {
			guard = b.pin.Check(in.Workspace.Root)
		}
	}
	if guard != nil {
		if !write {
			return exec(ctx)
		}
		return tools.Result{Text: guard.Error(), IsError: true}, nil
	}
	target := policy.ToolPath(policy.PathArg(args), in.Workspace.CWD)
	if name == "write" || name == "edit" {
		d := b.resolver.BeforeWrite(ctx, in.ContextID, b.ledger, target)
		switch d.Status {
		case RulesIncomplete:
			return tools.Result{Text: d.Err.Error(), IsError: true}, nil
		case NeedContext:
			c := d.Contribution
			return tools.Result{Text: strings.Join(c.Lines, "\n"), Controlled: true, OnAccepted: p.accept(in.ContextID, b, func() {
				b.dirs[filepath.Dir(target)] = true
				if c.OnAccepted != nil {
					c.OnAccepted()
				}
			}), Acceptable: p.valid(in.ContextID, b)}, nil
		}
		return exec(ctx)
	}
	result, err := exec(ctx)
	if err != nil || result.IsError || ctx.Err() != nil {
		return result, err
	}
	dir := target
	if name == "read" {
		dir = filepath.Dir(target)
	}
	_, c := b.resolver.NextTo(ctx, in.ContextID, b.ledger, dir)
	if c != nil {
		start := len(result.Text)
		result.Text += "\n\n" + strings.Join(c.Lines, "\n")
		result.ControlledText = result.Text[start:]
	}
	result.Acceptable = p.valid(in.ContextID, b)
	result.OnAccepted = p.accept(in.ContextID, b, func() {
		b.dirs[dir] = true
		if c != nil && c.OnAccepted != nil {
			c.OnAccepted()
		}
	})
	return result, nil
}

// 本地群聊尚无角色工作区授权；宿主确认主人、绑定会话并授权角色后才能放开。
func localPrivate(in turn.ContextIn) bool {
	return in.Audience.Channel == "console" && in.Audience.Scene == "private"
}
