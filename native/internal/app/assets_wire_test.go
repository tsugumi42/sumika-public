package app

import (
	"testing"

	"sumika-native/internal/assetsvc"
)

// 装配时素材服务就在跑：状态 active、关不掉（Essential），别的插件可以 Requires "assets"。
// 放回旧代码（没有装配）就红：插件表里没有 assets。
func TestAssetsServiceWired(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.writeConfig(testToken)
	a := e.open()
	t.Cleanup(func() { a.Close() })
	for _, s := range a.hub.PluginStatuses() {
		if s.ID != assetsvc.ID {
			continue
		}
		if s.State != "active" || !s.Essential || s.Name == "" || s.Sub == "" {
			t.Fatalf("素材服务应该在跑、关不掉、有名字和说明：%+v", s)
		}
		return
	}
	t.Fatal("插件表里没有素材服务")
}
