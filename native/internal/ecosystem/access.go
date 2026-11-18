// Package ecosystem 管理生态入口的可信访问句柄；模型参数不能生成句柄。
package ecosystem

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"sumika-native/internal/kernel"
)

var ErrDenied = errors.New("不可访问或不存在")

// denial 对外和 ErrDenied 同一句话（调用方看不出哪种情况），DenyReason 给本机日志用。
type denial struct{ why string }

func (d denial) Error() string        { return ErrDenied.Error() }
func (d denial) Is(target error) bool { return target == ErrDenied }

// LogDetail 让上层（turn 不能引用本包）写日志时取到原因。
func (d denial) LogDetail() string { return d.why }

// DenyReason 取拒绝的具体原因，只写本机日志，不交给模型或远端。不是拒绝返回空。
func DenyReason(err error) string {
	var d denial
	if errors.As(err, &d) {
		return d.why
	}
	return ""
}

// grantProblem 说这份授权为什么不能用。没问题返回空。
func grantProblem(ok bool, scopeSame, revisionSame, live bool) string {
	switch {
	case !ok:
		return "授权不存在"
	case !scopeSame:
		return "范围对不上"
	case !revisionSame:
		return "修订号对不上"
	case !live:
		return "授权已过期"
	}
	return ""
}

type grant struct {
	scope          kernel.Scope
	caps           map[string]bool
	expires        time.Time
	revision       uint64
	parent         string
	parentRevision uint64
}

// Access 只保存宿主签发时的授权快照；每次入口重新检查当前记录。
type Access struct {
	issuer   *Authority
	id       string
	scope    kernel.Scope
	revision uint64
}
type Authority struct {
	mu     sync.Mutex
	grants map[string]grant
	now    func() time.Time
}

func NewAuthority() *Authority { return &Authority{grants: map[string]grant{}, now: time.Now} }

// Issue 只给已经认证的宿主调用。来源授权与逐次 Gate 审批分开。
func (a *Authority) Issue(s kernel.Scope, caps []string, expires time.Time) (Access, error) {
	if !validScope(s) || !expires.After(a.now()) {
		return Access{}, ErrDenied
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	g := grant{scope: s, caps: map[string]bool{}, expires: expires, revision: s.AuthorityRevision}
	for _, c := range caps {
		g.caps[c] = true
	}
	a.grants[s.AuthorityID] = g
	return Access{a, s.AuthorityID, s, s.AuthorityRevision}, nil
}

// Resolve 供宿主从已认证 Invocation 找回既有句柄；不重签、不改变委托交集。
func (a *Authority) Resolve(scope kernel.Scope) (Access, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g, ok := a.grants[scope.AuthorityID]
	if why := grantProblem(ok, ok && g.scope == scope, ok && g.revision == scope.AuthorityRevision, ok && g.expires.After(a.now())); why != "" {
		return Access{}, denial{why}
	}
	if g.parent != "" {
		parent, ok := a.grants[g.parent]
		if !ok || parent.revision != g.parentRevision || !parent.expires.After(a.now()) {
			return Access{}, denial{"上级授权失效"}
		}
	}
	return Access{a, scope.AuthorityID, scope, g.revision}, nil
}
func validScope(s kernel.Scope) bool {
	return s.CharacterID != "" && s.Conversation != "" && s.ScopeID != "" && s.ActorKind == "owner" && s.ActorID != "" && s.ActorID != "0" && !s.ActorIsAccount() && s.AuthorityID != "" && s.AuthorityRevision > 0 && s.Platform == "local" && s.Channel == "console" && s.Scene == "private"
}
func (a *Authority) Revoke(id string) { a.mu.Lock(); delete(a.grants, id); a.mu.Unlock() }
func (x Access) Scope() kernel.Scope  { return x.scope }
func (x Access) Key() string          { return Digest(x.scope) }
func (x Access) Check(ctx context.Context, capability string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if x.issuer == nil {
		return ErrDenied
	}
	a := x.issuer
	a.mu.Lock()
	defer a.mu.Unlock()
	g, ok := a.grants[x.id]
	if why := grantProblem(ok, ok && g.scope == x.scope, ok && g.revision == x.revision, ok && g.expires.After(a.now())); why != "" {
		return denial{why}
	}
	if !g.caps[capability] {
		return denial{"没有这项能力：" + capability}
	}
	if g.parent != "" {
		parent, ok := a.grants[g.parent]
		if !ok || parent.revision != g.parentRevision || !parent.expires.After(a.now()) {
			return denial{"上级授权失效"}
		}
	}
	return nil
}

// Delegate 只取父当前能力和代理来源白名单的交集；子句柄不能再委派。
func (x Access) Delegate(ctx context.Context, conversation string, caps, sources []string) (Access, error) {
	if err := ctx.Err(); err != nil {
		return Access{}, err
	}
	if x.issuer == nil || conversation == "" {
		return Access{}, ErrDenied
	}
	a := x.issuer
	a.mu.Lock()
	defer a.mu.Unlock()
	parent, ok := a.grants[x.id]
	if !ok || parent.scope != x.scope || parent.revision != x.revision || parent.parent != "" || !parent.expires.After(a.now()) {
		return Access{}, ErrDenied
	}
	allowed := map[string]bool{}
	for _, cap := range caps {
		if !parent.caps[cap] {
			return Access{}, ErrDenied
		}
		allowed[cap] = true
	}
	for _, source := range sources {
		cap := "skill:" + source
		if !parent.caps[cap] {
			return Access{}, ErrDenied
		}
		allowed[cap] = true
	}
	scope := x.scope
	scope.Conversation = conversation
	scope.AuthorityID = ID()
	scope.AuthorityRevision = 1
	a.grants[scope.AuthorityID] = grant{scope: scope, caps: allowed, expires: parent.expires, revision: 1, parent: x.id, parentRevision: x.revision}
	return Access{a, scope.AuthorityID, scope, 1}, nil
}

func (x Access) Revoke() {
	if x.issuer != nil {
		x.issuer.Revoke(x.id)
	}
}
func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func Digest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Finalizer 由结果宿主在接纳或丢弃时调用，执行 ctx 的取消不代表结果已丢弃。
type Finalizer struct {
	once   sync.Once
	finish func(bool)
}

func NewFinalizer(fn func(bool)) *Finalizer { return &Finalizer{finish: fn} }
func (f *Finalizer) Accept()                { f.once.Do(func() { f.finish(true) }) }
func (f *Finalizer) Discard()               { f.once.Do(func() { f.finish(false) }) }
