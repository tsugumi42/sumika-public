package turn

// 插件的启停和依赖（规格-插件两层 第二节第 1～3 条，照 Magpie 的 Reconcile）。
//
// 每个插件一条记录（pluginEntry）：目录里的顺序、期望开着没有（desired）、状态、代号（gen，每次启动加一，停掉以后晚到的注册凭代号识别）、
// 启动序号（停的时候从新往旧）、它注册的 cleanup、它提供的服务名。状态：
//   - off：没开（配置关了，或者停掉了）
//   - pending：开着，但 Requires 的服务还没有（"等服务"）
//   - active：起来了，钩子和工具都在
//   - failed：Setup 出错或者 panic，记错误；不自动重试，改开关才再试
//
// 锁：reconMu 管"启动、停掉"这些动作（Reconcile 只能一个跑）；p.mu 管表和钩子列表。Setup 和 cleanup 都在两把锁之外跑，
// 但在 reconMu 之内（所以 Setup 里不许调 Hub.Use / StopPlugin / Reconcile，会死锁）。
// 外面的 StopPlugin 要等正在 Setup 的那个跑完才动手，所以"Setup 期间被停"不会留下半截注册。

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/plugintrace"
)

const (
	stateOff     = "off"
	statePending = "pending"
	stateStart   = "starting"
	stateActive  = "active"
	stateFailed  = "failed"
)

// Manifest 是插件的清单（规格-插件两层 第二节第 1 条）。没实现 Manifested 的插件当作：名字就是 ID、没有设置、能开能关、没有依赖。
type Manifest struct {
	Name      string   // 中文名，设置页的标题
	Sub       string   // 一行说明，照设计语言第十一节
	Requires  []string // 需要的服务名；服务没有在提供，插件就"等服务"
	Provides  []string // 声明会提供的服务名（只用于检查；实际注册用 Registry.Provide）
	Essential bool     // 关不掉的（审批这类）；开关对它无效
	// Settings 是一个结构体（字段带 title、desc、min、max、enum 标签），SettingFields 读出它的设置字段（plugin_settings.go）
	Settings any
}

// Manifested 是可选接口：插件声明清单
type Manifested interface {
	Manifest() Manifest
}

// pluginEntry 是插件表里的一条
type pluginEntry struct {
	id       string
	p        Plugin
	man      Manifest
	desired  bool
	state    string
	err      error
	gen      int
	seq      int
	cleanups []func()
	services []string
}

// serviceEntry 是一个提供的服务：owner 是提供它的插件，停掉时一起撤掉
type serviceEntry struct {
	owner string
	gen   int
	v     any
}

// manifestOf 读清单；没有清单时用默认值
func manifestOf(pl Plugin) Manifest {
	if m, ok := pl.(Manifested); ok {
		man := m.Manifest()
		if man.Name == "" {
			man.Name = pl.ID()
		}
		return man
	}
	return Manifest{Name: pl.ID()}
}

// register 把插件放进目录（不启动）。正在跑的同 id 报错，停了的换上新对象，清单不合法（名字为空、Essential 和 Requires 自己提供的服务冲突）也报错
func (p *plugins) register(pl Plugin) (*pluginEntry, error) {
	id := pl.ID()
	if id == "" {
		return nil, fmt.Errorf("插件没有 ID")
	}
	man := manifestOf(pl)
	for _, r := range man.Requires {
		if r == "" {
			return nil, fmt.Errorf("插件 %s 的依赖名字是空的", id)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = map[string]*pluginEntry{}
	}
	if old, dup := p.entries[id]; dup {
		// 在跑（或正在启动）的不能再登记；停了的换上新的插件对象，位置不变
		if old.state == stateActive || old.state == stateStart {
			return nil, fmt.Errorf("插件 %s 已经启用了", id)
		}
		old.p, old.man = pl, man
		return old, nil
	}
	e := &pluginEntry{id: id, p: pl, man: man, state: stateOff}
	p.entries[id] = e
	p.order = append(p.order, e)
	return e, nil
}

// setDesired 改期望的开关（不启动也不停，要再 reconcile）。Essential 的忽略 on=false；失败过的改成开，允许重新试一次
func (p *plugins) setDesired(id string, on bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[id]
	if e == nil {
		return false
	}
	if e.man.Essential {
		on = true
	}
	e.desired = on
	if on && e.state == stateFailed {
		e.state = stateOff
		e.err = nil
	}
	return true
}

// depsOKLocked 依赖的服务都在（p.mu 之内调）
func (p *plugins) depsOKLocked(e *pluginEntry) bool {
	for _, r := range e.man.Requires {
		if _, ok := p.services[r]; !ok {
			return false
		}
	}
	return true
}

// reconcile 把每个插件调成期望的样子：先算出要停的（没开的，加上因为依赖没了必须连带停的），按启动序号从新往旧停；
// 再起该起的（开着、没起来、依赖都在的），循环到一轮没有进展为止。调用方不持 p.mu
func (p *plugins) reconcile() {
	p.reconMu.Lock()
	defer p.reconMu.Unlock()
	for {
		progress := false
		if p.stopPass() {
			progress = true
		}
		// 起：开着、没起来（关或者等服务）、依赖都在的。order 可能正被 register 追加，先在 p.mu 之内拷一份
		p.mu.Lock()
		order := append([]*pluginEntry(nil), p.order...)
		p.mu.Unlock()
		for _, e := range order {
			p.mu.Lock()
			start := e.desired && (e.state == stateOff || e.state == statePending) && p.depsOKLocked(e)
			p.mu.Unlock()
			if start {
				p.start(e)
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	p.mu.Lock()
	for _, e := range p.order {
		if e.state == stateOff && e.desired {
			e.state = statePending
		}
	}
	p.mu.Unlock()
}

// stopPass 停该停的：没开的；依赖的服务只有将要停的插件提供、或者已经没了的，也要停（依赖方要先于提供方停，所以算完整个集合再从新往旧停）。
// 返回是不是真的停了东西
func (p *plugins) stopPass() bool {
	p.mu.Lock()
	toStop := map[*pluginEntry]string{} // 要停的 → 为什么（写进插件日志的 host.unload）
	for _, e := range p.order {
		if e.state == stateActive && !e.desired {
			toStop[e] = "关掉了"
		}
	}
	for changed := true; changed; {
		changed = false
		// 留下来的插件提供的服务
		avail := map[string]bool{}
		for name, s := range p.services {
			if owner := p.entries[s.owner]; owner != nil && owner.state == stateActive && toStop[owner] == "" {
				avail[name] = true
			}
		}
		for _, e := range p.order {
			if e.state != stateActive || toStop[e] != "" {
				continue
			}
			for _, r := range e.man.Requires {
				if !avail[r] {
					toStop[e] = "依赖的服务 " + r + " 没了"
					changed = true
					break
				}
			}
		}
	}
	var list []*pluginEntry
	for e := range toStop {
		list = append(list, e)
	}
	p.mu.Unlock()
	if len(list) == 0 {
		return false
	}
	sort.Slice(list, func(i, j int) bool { return list[i].seq > list[j].seq })
	for _, e := range list {
		p.teardown(e, toStop[e])
	}
	return true
}

// start 起一个插件：Setup 出错或 panic 记成 failed，已经注册的撤掉，别的照常
func (p *plugins) start(e *pluginEntry) {
	p.mu.Lock()
	e.gen++
	gen := e.gen
	p.seqNext++
	e.seq = p.seqNext
	e.state = stateStart
	e.err = nil
	p.mu.Unlock()

	began := time.Now()
	cleanup, err := p.runSetup(e, gen)
	took := time.Since(began)

	p.mu.Lock()
	if err != nil {
		e.state = stateFailed
		e.err = err
		fns := e.cleanups
		e.cleanups = nil
		p.dropServicesLocked(e.id)
		p.mu.Unlock()
		p.logf("插件 %s 没启用：%v", e.id, err)
		p.trace.Write(e.id, gen, plugintrace.EventLoad, plugintrace.Event{Took: took, Timed: true, Result: plugintrace.Fail, Kind: "apply", Error: err.Error()}, "")
		p.runCleanups(e.id, fns)
		return
	}
	if cleanup != nil {
		e.cleanups = append(e.cleanups, cleanup)
	}
	e.state = stateActive
	p.mu.Unlock()
	p.trace.Write(e.id, gen, plugintrace.EventLoad, plugintrace.Event{Took: took, Timed: true, Result: plugintrace.OK, Note: fmt.Sprintf("代号 %d", gen)}, "")
}

// runSetup 调插件的 Setup，慌了收住，返回错误
func (p *plugins) runSetup(e *pluginEntry, gen int) (cleanup func(), err error) {
	defer func() {
		if x := recover(); x != nil {
			cleanup = nil
			err = fmt.Errorf("启动时出错：%v", x)
		}
	}()
	return e.p.Setup(&Registry{p: p, id: e.id, gen: gen})
}

// teardown 停一个插件：代号加一（之后晚到的注册当场撤），撤掉服务，调 cleanup（从后往前）；why 写进插件日志
func (p *plugins) teardown(e *pluginEntry, why string) {
	p.mu.Lock()
	fns := e.cleanups
	e.cleanups = nil
	gen := e.gen
	e.gen++
	p.dropServicesLocked(e.id)
	// 先让贡献失效，再调可能阻塞的 cleanup；清理期间也不能接纳旧结果。
	for _, h := range p.context {
		if h.plugin == e.id {
			h.alive = false
		}
	}
	if e.desired {
		e.state = statePending
	} else {
		e.state = stateOff
	}
	p.mu.Unlock()
	began := time.Now()
	errs := p.runCleanups(e.id, fns)
	ev := plugintrace.Event{Took: time.Since(began), Timed: true, Result: plugintrace.OK, Note: why}
	if len(errs) > 0 {
		ev.Result, ev.Kind, ev.Error = plugintrace.Fail, "cleanup", strings.Join(errs, "；")
	}
	p.trace.Write(e.id, gen, plugintrace.EventUnload, ev, "")
}

// runCleanups 从后往前调，慌了记一行，接着调别的；返回慌了的那几条
func (p *plugins) runCleanups(id string, fns []func()) (errs []string) {
	for i := len(fns) - 1; i >= 0; i-- {
		func() {
			defer func() {
				if x := recover(); x != nil {
					p.logf("插件 %s 撤销注册时出错：%v", id, x)
					errs = append(errs, fmt.Sprint(x))
				}
			}()
			fns[i]()
		}()
	}
	return errs
}

// dropServicesLocked 撤掉这个插件提供的服务（p.mu 之内）
func (p *plugins) dropServicesLocked(id string) {
	for name, s := range p.services {
		if s.owner == id {
			delete(p.services, name)
		}
	}
}

// onCleanupFor 记一个撤销函数：插件还在启动或者已经起来，且代号没变，就挂在它名下；否则（已经停了、代号过期）当场调，不入表。
// 撤销函数不能在持 p.mu 时调，所以这里先判再释放锁
func (p *plugins) onCleanupFor(r *Registry, fn func()) {
	p.mu.Lock()
	e := p.entries[r.id]
	if e != nil && e.gen == r.gen && (e.state == stateStart || e.state == stateActive) {
		e.cleanups = append(e.cleanups, fn)
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	fn()
}

// provide 注册一个服务（Registry.Provide）：重名（别的插件已经提供）报错，停掉时撤掉
func (p *plugins) provide(r *Registry, name string, v any) error {
	if name == "" {
		return fmt.Errorf("服务名是空的")
	}
	p.mu.Lock()
	if old, ok := p.services[name]; ok && old.owner != r.id {
		p.mu.Unlock()
		return fmt.Errorf("服务 %s 已经由插件 %s 提供了", name, old.owner)
	}
	if p.services == nil {
		p.services = map[string]*serviceEntry{}
	}
	p.services[name] = &serviceEntry{owner: r.id, gen: r.gen, v: v}
	p.mu.Unlock()
	p.onCleanupFor(r, func() {
		p.mu.Lock()
		if s, ok := p.services[name]; ok && s.owner == r.id && s.gen == r.gen {
			delete(p.services, name)
		}
		p.mu.Unlock()
	})
	return nil
}

// service 取一个正在提供的服务
func (p *plugins) service(name string) (any, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.services[name]
	if !ok {
		return nil, false
	}
	return s.v, true
}

// PluginStatus 是设置页要的一行：目录里的每个插件（不管开没开）
type PluginStatus struct {
	ID, Name, Sub string
	State         string // off / pending / active / failed
	Err           string
	Desired       bool
	Essential     bool
	Requires      []string
	Settings      any
}

// PluginStatuses 列出目录里所有插件，按注册顺序
func (h *Hub) PluginStatuses() []PluginStatus {
	p := h.plugins
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PluginStatus, 0, len(p.order))
	for _, e := range p.order {
		s := PluginStatus{ID: e.id, Name: e.man.Name, Sub: e.man.Sub, State: e.state, Desired: e.desired, Essential: e.man.Essential, Requires: e.man.Requires, Settings: e.man.Settings}
		if e.err != nil {
			s.Err = e.err.Error()
		}
		out = append(out, s)
	}
	return out
}

// RegisteredPlugins 返回起来了的插件（active）的 ID，已排序；设置页和 clientinfo 用
func (h *Hub) RegisteredPlugins() []string {
	p := h.plugins
	p.mu.Lock()
	defer p.mu.Unlock()
	var names []string
	for _, e := range p.order {
		if e.state == stateActive {
			names = append(names, e.id)
		}
	}
	sort.Strings(names)
	return names
}

// Register 把插件放进目录，不启动（启动交给 SetEnabled + Reconcile）
func (h *Hub) Register(pl Plugin) error {
	_, err := h.plugins.register(pl)
	return err
}

// SetEnabled 改开关并调整（当场 reconcile，不重启）。插件不在目录里返回 false
func (h *Hub) SetEnabled(id string, on bool) bool {
	if !h.plugins.setDesired(id, on) {
		return false
	}
	h.plugins.reconcile()
	return true
}

// Reconcile 按期望把所有插件调整一次（启动时注册完调一次）
func (h *Hub) Reconcile() { h.plugins.reconcile() }

// Reload 重新加载一个在跑的插件：实现了 Reconfigurer 就当场调；Reconfigure 出错、panic，或者没实现，就停了再起。
// 关不掉的（Essential）没法停，Reconfigure 失败直接报错；停了再起没起来也报错，不把失败当成成功。
func (h *Hub) Reload(id string) error {
	p := h.plugins
	done, err := p.reconfigureInPlace(id)
	if err != nil || done {
		return err
	}
	p.mu.Lock()
	e := p.entries[id]
	essential := e != nil && e.man.Essential
	p.mu.Unlock()
	if essential {
		return fmt.Errorf("插件 %s 关不掉，没法停了再起，重新加载没成", id)
	}
	p.setDesired(id, false)
	p.reconcile()
	p.setDesired(id, true)
	p.reconcile()
	st, serr := p.entryState(id)
	if st != stateActive {
		if serr == nil {
			return fmt.Errorf("插件 %s 重新加载后没启用（状态 %s）", id, st)
		}
		return fmt.Errorf("插件 %s 重新加载后没启用：%w", id, serr)
	}
	return nil
}

// reconfigureInPlace 当场调插件的 Reconfigure（持 reconMu，别的启停等它完）。插件不在跑返回错误；
// 没实现、或者 Reconfigure 出错、panic，返回 false，由调用方停了再起
func (p *plugins) reconfigureInPlace(id string) (bool, error) {
	p.reconMu.Lock()
	defer p.reconMu.Unlock()
	p.mu.Lock()
	e := p.entries[id]
	if e == nil || e.state != stateActive {
		p.mu.Unlock()
		return false, fmt.Errorf("插件 %s 没有在跑", id)
	}
	rc, ok := e.p.(Reconfigurer)
	p.mu.Unlock()
	if !ok {
		return false, nil
	}
	if err := callReconfigure(rc); err != nil {
		p.logf("插件 %s 重新加载没成，停了再起：%v", id, err)
		return false, nil
	}
	return true, nil
}

// callReconfigure 调 Reconfigure，慌了收住，当成错误返回
func callReconfigure(rc Reconfigurer) (err error) {
	defer func() {
		if x := recover(); x != nil {
			err = fmt.Errorf("重新加载时出错：%v", x)
		}
	}()
	return rc.Reconfigure()
}

// entryState 读一个插件的状态和错误（持 p.mu）；不在目录里返回空状态
func (p *plugins) entryState(id string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[id]
	if e == nil {
		return "", nil
	}
	return e.state, e.err
}

// Reconfigurer 是可选接口：设置值变了，插件自己读配置当场生效（没实现、或者返回错误，就停了再起）
type Reconfigurer interface {
	Reconfigure() error
}

// use 启用一个插件（Hub.Use）：注册进目录、开、reconcile；Setup 出错或 panic 返回错误，别的插件不受影响
func (p *plugins) use(pl Plugin) error {
	e, err := p.register(pl)
	if err != nil {
		return err
	}
	p.setDesired(e.id, true)
	p.reconcile()
	p.mu.Lock()
	st, serr := e.state, e.err
	p.mu.Unlock()
	if st == stateFailed {
		return fmt.Errorf("插件 %s 没启用：%w", e.id, serr)
	}
	return nil
}

// stop 停掉一个插件（Hub.StopPlugin）：关开关、reconcile；Setup 正在跑的，等它跑完再停
func (p *plugins) stop(id string) {
	if p.setDesired(id, false) {
		p.reconcile()
	}
}

// onCleanup 测试用：给在跑的插件挂一个撤销函数（插件停掉时调）。插件不在跑就当场调
func (p *plugins) onCleanup(id string, fn func()) {
	p.mu.Lock()
	e := p.entries[id]
	if e != nil && (e.state == stateStart || e.state == stateActive) {
		e.cleanups = append(e.cleanups, fn)
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	fn()
}

// Provide 注册一个服务：插件停掉时撤掉。同名服务已经被别的插件提供时报错
func (r *Registry) Provide(name string, v any) error { return r.p.provide(r, name, v) }

// Service 取一个在跑的插件提供的服务（没有就是 false）
func (r *Registry) Service(name string) (any, bool) { return r.p.service(name) }

// ToolNames 是这个听众现在能用的工具名（内置的也在里面）。设置页和装配测试用：插件关了，它的工具就不在这里
func (h *Hub) ToolNames(a audience.Audience) []string {
	decls, _ := h.plugins.toolsFor(a)
	names := make([]string, 0, len(decls))
	for _, d := range decls {
		names = append(names, d.Name)
	}
	return names
}
