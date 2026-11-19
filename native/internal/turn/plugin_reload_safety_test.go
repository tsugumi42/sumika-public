package turn

// 重新加载的失败路径（规格-插件两层 第二节第 4 条）。
// 1) Reconfigure panic：收住当成错误走停了再起，reconMu 不泄漏，之后的启停照常（放回旧代码：Reload 的 panic 冒出来，reconMu 永远不释放，SetEnabled 卡死，下面的超时判红）。
// 2) Essential 的插件 Reconfigure 出错：停不了也重启不了，Reload 必须报错（放回旧代码返回 nil，设置改动悄悄没生效）。
// 3) 停了再起，Setup 又出错：Reload 报错（放回旧代码返回 nil）。
// 等待用通道和超时判失败，不用固定睡眠。

import (
	"errors"
	"testing"
	"time"
)

// panicReconfig 的 Reconfigure 直接 panic
type panicReconfig struct{ *reloadPlugin }

func (panicReconfig) Reconfigure() error { panic("读配置时空指针") }

func TestReloadReconfigurePanicDoesNotLeakLock(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	rp := &reloadPlugin{}
	if err := h.Use(panicReconfig{rp}); err != nil {
		t.Fatal(err)
	}
	var reloadErr error
	func() {
		defer func() {
			if x := recover(); x != nil {
				t.Errorf("Reload 不该把 Reconfigure 的 panic 冒出来：%v", x)
			}
		}()
		reloadErr = h.Reload("reloadable")
	}()
	if reloadErr != nil {
		t.Fatalf("Reconfigure panic 后应该停了再起并成功，实际 %v", reloadErr)
	}
	if rp.setups != 2 || rp.cleanups != 1 || stateOf(h, "reloadable") != "active" {
		t.Fatalf("应该停了再起：setups=%d cleanups=%d state=%s", rp.setups, rp.cleanups, stateOf(h, "reloadable"))
	}
	done := make(chan struct{})
	go func() {
		h.SetEnabled("reloadable", false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Reload 之后 SetEnabled 卡死：reconMu 泄漏了")
	}
	if stateOf(h, "reloadable") != "off" {
		t.Fatal("关掉以后应该 off")
	}
}

// 关不掉的插件：Reconfigure 出错时没法停了再起，Reload 报错，不说成功
type essentialReload struct{ *reloadPlugin }

func (essentialReload) Manifest() Manifest { return Manifest{Essential: true} }
func (essentialReload) Reconfigure() error { return errTestReconfig }

func TestReloadEssentialReconfigureErrorReported(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	rp := &reloadPlugin{}
	if err := h.Use(essentialReload{rp}); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload("reloadable"); err == nil {
		t.Fatal("Essential 的插件 Reconfigure 出错，Reload 应该报错，实际返回 nil")
	}
	if rp.setups != 1 || stateOf(h, "reloadable") != "active" {
		t.Fatalf("没有重启，应该仍然 active 且 Setup 只跑一次：setups=%d state=%s", rp.setups, stateOf(h, "reloadable"))
	}
}

// 没有 Reconfigure 的插件停了再起，第二次 Setup 出错：Reload 报错，状态是启用失败
type failsOnSecondSetup struct{ n int }

func (p *failsOnSecondSetup) ID() string { return "flaky" }
func (p *failsOnSecondSetup) Setup(r *Registry) (func(), error) {
	p.n++
	if p.n > 1 {
		return nil, errors.New("再起不来")
	}
	return nil, nil
}

func TestReloadRestartFailureReported(t *testing.T) {
	t.Parallel()
	h := newTestHub(t)
	fp := &failsOnSecondSetup{}
	if err := h.Use(fp); err != nil {
		t.Fatal(err)
	}
	err := h.Reload("flaky")
	if err == nil {
		t.Fatal("停了再起没起来，Reload 应该报错，实际返回 nil")
	}
	if got := stateOf(h, "flaky"); got != "failed" {
		t.Fatalf("应该是启用失败，实际 %s", got)
	}
}
