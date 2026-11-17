package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/approval"
	"sumika-native/internal/projectcontext"
	"sumika-native/internal/turn"
)

// 信任的三个来源：配置里手写的信任根、"记住"（规则存储）、这次运行里的"允许一次"；撤销"记住"当场失效；
// 按真实路径认，不按名字、不按父目录（别的项目不沾光）。
func TestProjectTrustSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	work := t.TempDir()
	ws, err := turn.NewWorkspace("console:x", work)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := turn.NewWorkspace("console:x", t.TempDir())
	grants := approval.LoadGrants(filepath.Join(t.TempDir(), "tool-grants.yml"))
	pt := newProjectTrust(nil, grants)
	if ok, err := pt.Trusted(ctx, ws); ok || err != nil {
		t.Fatalf("什么都没答过：%v %v", ok, err)
	}
	pin, err := projectcontext.PinRoot(ws.Root)
	if err != nil {
		t.Fatal(err)
	}
	// 记住：写进规则存储（和审批卡点"记住"写的一样）
	if err := grants.Put(approval.Rule{Key: projectTrustKey(pin.Real()), Label: "项目说明", Tool: "project_trust", At: 1}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := pt.Trusted(ctx, ws); !ok {
		t.Fatal("记住过的目录该信任")
	}
	if ok, _ := pt.Trusted(ctx, other); ok {
		t.Fatal("别的目录不该沾光")
	}
	if ok, err := grants.Remove(projectTrustKey(pin.Real())); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := pt.Trusted(ctx, ws); ok {
		t.Fatal("撤销以后当场不再信任")
	}
	// 允许一次：只在这次运行里（内存）
	pt.session[strings.ToLower(pin.Real())] = true
	if ok, _ := pt.Trusted(ctx, ws); !ok {
		t.Fatal("允许一次该在这次运行里算数")
	}
	// 配置里手写的信任根照旧算
	static := projectcontext.ConfigTrust([]string{pin.Real()}, nil)
	pt2 := newProjectTrust(static, grants)
	if ok, err := pt2.Trusted(ctx, ws); !ok || err != nil {
		t.Fatalf("配置信任根：%v %v", ok, err)
	}
	if ok, _ := pt2.Trusted(ctx, other); ok {
		t.Fatal("配置信任根不盖别的目录")
	}
}
