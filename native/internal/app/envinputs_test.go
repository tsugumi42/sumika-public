package app

import (
	"context"
	"testing"

	"sumika-native/internal/mcpclient"
	"sumika-native/internal/turn"
)

// S11 只报隔离和重连用完；从没连上的服务由环境清单另补（坑 11），已经列过的不重复。
func TestNeverConnectedFromHealth(t *testing.T) {
	health := []mcpclient.Health{
		{ID: "ok", Connected: true},
		{ID: "down", Connected: false, Note: "exec: not found"},
		{ID: "exhausted", Connected: false, Reconnect: "exhausted", Note: "次数用完"},
		{ID: "quarantined", Connected: false, Note: "旧调用没结果"},
		{ID: "retrying", Connected: false, Reconnect: "retrying", Note: "正在重连"},
	}
	got := neverConnected(health, "quarantined")
	if len(got) != 2 || got[0].Name != "down" || got[1].Name != "retrying" {
		t.Fatalf("只该补 down 和 retrying：%+v", got)
	}
	if got[0].Cause != "exec: not found" || got[1].Cause != "正在重连" {
		t.Errorf("原因要带上健康状态的那一句：%+v", got)
	}
}

// 插件清单只把 failed 的交给环境项，别的状态不报。
func TestFailedPlugins(t *testing.T) {
	got := failedPlugins([]turn.PluginStatus{
		{ID: "memory", State: "active"},
		{ID: "calendar", State: "failed", Err: "bad config"},
		{ID: "weather", State: "off"},
	})
	if len(got) != 1 || got[0].ID != "calendar" || got[0].Err != "bad config" {
		t.Fatalf("只该报 calendar：%+v", got)
	}
}

// 后台不在接收模式时运行部分返回零值，宿主会按“后台没开/只看历史”记 unknown。
func TestEnvInputsOutsideReceive(t *testing.T) {
	a := &App{opt: Options{Mode: Archive}}
	in := a.EnvInputs(context.Background())
	if in.Gateway.Available || in.Embed.Available || in.Backup.Available || in.MCP.Available {
		t.Fatalf("archive 不该有运行部分：%+v", in)
	}
}
