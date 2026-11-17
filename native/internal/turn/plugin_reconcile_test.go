package turn

// 插件启停和依赖（规格-插件两层 第二节第 2、3 条，第八节第一步的生命周期部分）。直接用 Hub{plugins: 新的插件表} 构造，
// 不起整个后台；等待用通道同步，不用固定睡眠。每条都写了"放回旧代码就红"的理由（见每个测试的注释）

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// testPlugin 是带清单的测试插件
type testPlugin struct {
	id    string
	man   Manifest
	setup func(r *Registry) (func(), error)
}

func (p testPlugin) ID() string                        { return p.id }
func (p testPlugin) Manifest() Manifest                { return p.man }
func (p testPlugin) Setup(r *Registry) (func(), error) { return p.setup(r) }

// newTestHub 是只有插件表的 Hub
func newTestHub(t *testing.T) *Hub {
	t.Helper()
	logf := func(format string, args ...any) { t.Logf(format, args...) }
	return &Hub{plugins: newPlugins(context.Background(), HookTimeouts{}, logf)}
}

// stateOf 从设置页用的清单里找一个插件的状态
func stateOf(h *Hub, id string) string {
	for _, s := range h.PluginStatuses() {
		if s.ID == id {
			return s.State
		}
	}
	return "missing"
}

// contextHooks 数表里还挂着的上下文钩子（停掉以后应该是 0）
func contextHooks(h *Hub) int {
	h.plugins.mu.Lock()
	defer h.plugins.mu.Unlock()
	return len(h.plugins.context)
}

// 清单说 Setup 慌了：Use 不炸、这个插件的注册撤干净、状态是"启用失败"、别的插件照常起来。
// 放回旧代码（没有 recover）就红：测试里 panic 会冒到这里
func TestPluginSetupPanicIsolated(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	err := h.Use(testPlugin{id: "boom", setup: func(r *Registry) (func(), error) {
		r.OnContext(func(context.Context, ContextIn) (*Contribution, error) { return nil, nil })
		panic("配置炸了")
	}})
	if err == nil {
		t.Fatal("Setup panic 应该报错（启用失败）")
	}
	if got := stateOf(h, "boom"); got != "failed" {
		t.Fatalf("状态应该是 failed，实际 %s", got)
	}
	if n := contextHooks(h); n != 0 {
		t.Fatalf("Setup panic 前注册的钩子应该撤掉，还剩 %d 个", n)
	}
	if err := h.Use(testPlugin{id: "fine", setup: func(r *Registry) (func(), error) { return nil, nil }}); err != nil {
		t.Fatalf("别的插件应该照常启用：%v", err)
	}
	if got := h.RegisteredPlugins(); !reflect.DeepEqual(got, []string{"fine"}) {
		t.Fatalf("在跑的应该只有 fine，实际 %v", got)
	}
}

// Setup 阻塞着的时候另一个协程停掉它：Setup 跑完以后注册要撤干净，不能留下"已经停了却还在跑"的钩子和名字。
// 放回旧代码（停的时候 cleanups 里还没有它的撤销函数，Setup 之后又挂回去）就红：RegisteredPlugins 里还会有它
func TestPluginStoppedWhileSetupRunning(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- h.Use(testPlugin{id: "slow", setup: func(r *Registry) (func(), error) {
			r.OnContext(func(context.Context, ContextIn) (*Contribution, error) { return nil, nil })
			close(entered)
			<-release
			return nil, nil
		}})
	}()
	<-entered
	stopped := make(chan struct{})
	go func() {
		h.StopPlugin("slow") // 要等 Setup 跑完才动手（reconMu）
		close(stopped)
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Setup 正常返回不该报错：%v", err)
	}
	<-stopped
	if got := stateOf(h, "slow"); got != "off" {
		t.Fatalf("停掉以后状态应该是 off，实际 %s", got)
	}
	if n := contextHooks(h); n != 0 {
		t.Fatalf("停掉以后钩子应该撤干净，还剩 %d 个", n)
	}
	if got := h.RegisteredPlugins(); len(got) != 0 {
		t.Fatalf("停掉的插件不该还在跑的列表里：%v", got)
	}
}

// 停掉以后晚到的注册（代号过期）当场撤掉，不挂进表里
func TestPluginLateRegistrationAfterStopUndone(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	var late *Registry
	if err := h.Use(testPlugin{id: "late", setup: func(r *Registry) (func(), error) {
		late = r
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	h.StopPlugin("late")
	// 停掉以后，它的 Registry 还被插件的后台协程拿着；此时注册的钩子不能留下
	late.OnContext(func(context.Context, ContextIn) (*Contribution, error) { return nil, nil })
	if n := contextHooks(h); n != 0 {
		t.Fatalf("停掉以后晚到的钩子应该当场撤掉，还剩 %d 个", n)
	}
	// 停了又开：旧代号的 Registry 晚到的注册仍然要撤掉（不能因为插件又在跑了就挂上去）
	if err := h.Use(testPlugin{id: "late", setup: func(r *Registry) (func(), error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	late.OnContext(func(context.Context, ContextIn) (*Contribution, error) { return nil, nil })
	if n := contextHooks(h); n != 0 {
		t.Fatalf("旧代号的钩子在新的一次启动里不该挂上，还剩 %d 个", n)
	}
}

// 依赖排序：B 要 A 提供的服务。B 先开，等服务；A 开了以后两个都起；关 A 时 B 先停（状态"等服务"），再开 A 两个都回来。
// 循环也要测：C 要 B 提供的服务，一次开齐要循环到起不动为止。
// 放回旧代码（没有依赖、没有 Service）就红：Service 和 Requires 都是新的
func TestPluginDependenciesReconcile(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	var mu sync.Mutex
	var events []string
	record := func(s string) {
		mu.Lock()
		events = append(events, s)
		mu.Unlock()
	}
	var gotSvc any
	a := testPlugin{id: "a", man: Manifest{Provides: []string{"svc.a"}}, setup: func(r *Registry) (func(), error) {
		if err := r.Provide("svc.a", "A的服务"); err != nil {
			return nil, err
		}
		record("a 起")
		return func() { record("a 停") }, nil
	}}
	b := testPlugin{id: "b", man: Manifest{Requires: []string{"svc.a"}, Provides: []string{"svc.b"}}, setup: func(r *Registry) (func(), error) {
		v, ok := r.Service("svc.a")
		if !ok {
			return nil, errors.New("没有 svc.a")
		}
		gotSvc = v
		if err := r.Provide("svc.b", "B的服务"); err != nil {
			return nil, err
		}
		record("b 起")
		return func() { record("b 停") }, nil
	}}
	c := testPlugin{id: "c", man: Manifest{Requires: []string{"svc.b"}}, setup: func(r *Registry) (func(), error) {
		record("c 起")
		return func() { record("c 停") }, nil
	}}

	// 先开 c、再 b，都等服务，状态 pending
	if err := h.Use(c); err != nil {
		t.Fatalf("等服务不是错误：%v", err)
	}
	if err := h.Use(b); err != nil {
		t.Fatalf("等服务不是错误：%v", err)
	}
	if got := stateOf(h, "b"); got != "pending" {
		t.Fatalf("b 应该等服务（pending），实际 %s", got)
	}
	if got := stateOf(h, "c"); got != "pending" {
		t.Fatalf("c 应该等服务（pending），实际 %s", got)
	}
	// 开 a：一次 reconcile 循环要把 a、b、c 全起来
	if err := h.Use(a); err != nil {
		t.Fatal(err)
	}
	if got := h.RegisteredPlugins(); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("开了 a 以后三个都该在跑，实际 %v", got)
	}
	if gotSvc != "A的服务" {
		t.Fatalf("b 拿到的服务应该是 A 提供的，实际 %v", gotSvc)
	}

	// 关 a：c、b 先停（从新往旧），a 最后停；b、c 变成等服务
	h.SetEnabled("a", false)
	mu.Lock()
	gotOrder := append([]string(nil), events...)
	mu.Unlock()
	want := []string{"a 起", "b 起", "c 起", "c 停", "b 停", "a 停"}
	if !reflect.DeepEqual(gotOrder, want) {
		t.Fatalf("启停顺序不对\n期望 %v\n实际 %v", want, gotOrder)
	}
	if got := stateOf(h, "a"); got != "off" {
		t.Fatalf("a 应该 off，实际 %s", got)
	}
	if got := stateOf(h, "b"); got != "pending" {
		t.Fatalf("a 关了 b 应该等服务，实际 %s", got)
	}
	// 再开 a：两个都回来
	h.SetEnabled("a", true)
	if got := h.RegisteredPlugins(); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("重新开 a 以后三个都该回来，实际 %v", got)
	}
}

// Requires 的服务只有一个插件能提供：第二个注册同名服务报错，状态 failed，第一个不受影响
func TestPluginServiceNameConflict(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	if err := h.Use(testPlugin{id: "first", setup: func(r *Registry) (func(), error) {
		return nil, r.Provide("svc.x", 1)
	}}); err != nil {
		t.Fatal(err)
	}
	err := h.Use(testPlugin{id: "second", setup: func(r *Registry) (func(), error) {
		return nil, r.Provide("svc.x", 2)
	}})
	if err == nil || !strings.Contains(err.Error(), "svc.x") {
		t.Fatalf("同名服务应该报错，实际 %v", err)
	}
	if got := stateOf(h, "second"); got != "failed" {
		t.Fatalf("second 应该 failed，实际 %s", got)
	}
	if got := stateOf(h, "first"); got != "active" {
		t.Fatalf("first 不受影响，应该 active，实际 %s", got)
	}
}

// Essential 的插件关不掉：开关写 false 也还在跑
func TestPluginEssentialCannotBeDisabled(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	if err := h.Use(testPlugin{id: "approval", man: Manifest{Essential: true}, setup: func(r *Registry) (func(), error) {
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	h.SetEnabled("approval", false)
	if got := stateOf(h, "approval"); got != "active" {
		t.Fatalf("Essential 的插件关不掉，应该 active，实际 %s", got)
	}
}

// 停掉的插件可以再开（同 id 换上新对象，位置不变）；在跑的同 id 再登记报错
func TestPluginRestartAndDuplicate(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	mk := func() Plugin {
		return testPlugin{id: "again", setup: func(r *Registry) (func(), error) { return nil, nil }}
	}
	if err := h.Use(mk()); err != nil {
		t.Fatal(err)
	}
	if err := h.Use(mk()); err == nil {
		t.Fatal("同一个 id 在跑的时候再启用应该报错")
	}
	h.StopPlugin("again")
	if err := h.Use(mk()); err != nil {
		t.Fatalf("停了以后可以再开：%v", err)
	}
	if got := stateOf(h, "again"); got != "active" {
		t.Fatalf("应该 active，实际 %s", got)
	}
}

// 设置值变了：实现了 Reconfigure 的当场调（不重启，Setup 只跑一次）；没实现的停了再起（Setup 跑两次、cleanup 调一次）；
// Reconfigure 出错的停了再起。放回旧代码（没有 Reload）就红：编译不过，也就是 Reload 本身是新的
type reloadPlugin struct {
	setups, cleanups, reconfigs int
	reconfigErr                 error
	withReconfig                bool
}

func (p *reloadPlugin) ID() string { return "reloadable" }
func (p *reloadPlugin) Setup(r *Registry) (func(), error) {
	p.setups++
	return func() { p.cleanups++ }, nil
}

type reloadWithReconfig struct{ *reloadPlugin }

func (p reloadWithReconfig) Reconfigure() error {
	p.reconfigs++
	return p.reconfigErr
}

func TestPluginReloadReconfigures(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	rp := &reloadPlugin{}
	if err := h.Use(reloadWithReconfig{rp}); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload("reloadable"); err != nil {
		t.Fatal(err)
	}
	if rp.reconfigs != 1 || rp.setups != 1 || rp.cleanups != 0 {
		t.Fatalf("实现了 Reconfigure：应该当场调、不重启，实际 reconfigs=%d setups=%d cleanups=%d", rp.reconfigs, rp.setups, rp.cleanups)
	}
}

func TestPluginReloadRestartsWithoutReconfigure(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	rp := &reloadPlugin{}
	if err := h.Use(rp); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload("reloadable"); err != nil {
		t.Fatal(err)
	}
	if rp.setups != 2 || rp.cleanups != 1 || stateOf(h, "reloadable") != "active" {
		t.Fatalf("没实现：应该停了再起，实际 setups=%d cleanups=%d state=%s", rp.setups, rp.cleanups, stateOf(h, "reloadable"))
	}
}

func TestPluginReloadRestartsOnReconfigureError(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	rp := &reloadPlugin{reconfigErr: errTestReconfig}
	if err := h.Use(reloadWithReconfig{rp}); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload("reloadable"); err != nil {
		t.Fatal(err)
	}
	if rp.reconfigs != 1 || rp.setups != 2 || rp.cleanups != 1 || stateOf(h, "reloadable") != "active" {
		t.Fatalf("Reconfigure 出错：应该停了再起，实际 reconfigs=%d setups=%d cleanups=%d", rp.reconfigs, rp.setups, rp.cleanups)
	}
}

var errTestReconfig = errors.New("改不了")
