package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
)

func openFreeAPIApp(t *testing.T) *App {
	t.Helper()
	e := newEnv(t)
	fake := newLifeFake(t)
	fake.attach(e, "")
	a := e.open()
	t.Cleanup(func() { a.Close() })
	return a
}

func TestFreeAPISettingsActions(t *testing.T) {
	t.Parallel()
	a := openFreeAPIApp(t)
	ctx := context.Background()
	v, err := a.freeAPIView("")
	if err != nil || len(v.Categories) != 4 || !v.ProbeEnabled || v.ProbeHours != 12 || len(v.Limits) != 3 {
		t.Fatalf("默认视图：%v %+v", err, v)
	}
	if v.Limits[0].PerActorDaily != 20 || v.Limits[0].PerConversationHourly != 15 || v.Limits[2].PerActorDaily != 0 {
		t.Errorf("默认次数：%+v", v.Limits)
	}
	// 置顶、关闭、清退避都落在引擎里
	if v, err = a.freeAPIAct(ctx, api.FreeAPIAction{Kind: api.FreeAPIPin, ID: "met-norway", Bool: true}); err != nil {
		t.Fatal(err)
	}
	if p := findProvider(v, "met-norway"); !p.Pinned {
		t.Errorf("置顶没生效：%+v", p)
	}
	if v, err = a.freeAPIAct(ctx, api.FreeAPIAction{Kind: api.FreeAPIOff, ID: "tencent-weather", Bool: true}); err != nil || !findProvider(v, "tencent-weather").Off {
		t.Fatalf("关闭没生效：%v", err)
	}
	if _, err = a.freeAPIAct(ctx, api.FreeAPIAction{Kind: api.FreeAPIOff, ID: "不存在", Bool: true}); err == nil {
		t.Error("不存在的接口应报错")
	}
	// 改探测和次数：写进配置文件，工具下一次调用就读到
	if _, err = a.freeAPIAct(ctx, api.FreeAPIAction{Kind: api.FreeAPIProbeHours, Int: 24}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.freeAPIAct(ctx, api.FreeAPIAction{Kind: api.FreeAPIProbeOn, Bool: false}); err != nil {
		t.Fatal(err)
	}
	if v, err = a.freeAPIAct(ctx, api.FreeAPIAction{Kind: api.FreeAPILimit, Tool: "weather", Field: "actor", Int: 7}); err != nil {
		t.Fatal(err)
	}
	if v.ProbeEnabled || v.ProbeHours != 24 || v.Limits[0].PerActorDaily != 7 {
		t.Errorf("配置没改：%+v", v)
	}
	if got := a.lifeSettings().FreeAPI; got.Limits["weather"].PerActorDaily != 7 || got.ProbeEnabled || got.ProbeInterval.Hours() != 24 {
		t.Errorf("工具读到的设置：%+v", got)
	}
	// 敌对输入：不认识的工具、字段、种类、越界、版本冲突都不写
	for name, act := range map[string]api.FreeAPIAction{
		"工具名拼错": {Kind: api.FreeAPILimit, Tool: "weather.perActorDaily", Field: "actor", Int: 1},
		"字段拼错":  {Kind: api.FreeAPILimit, Tool: "weather", Field: "../x", Int: 1},
		"种类不认识": {Kind: "rm -rf"},
		"间隔越界":  {Kind: api.FreeAPIProbeHours, Int: 99},
		"次数为负":  {Kind: api.FreeAPILimit, Tool: "weather", Field: "actor", Int: -5},
		"版本过期":  {Kind: api.FreeAPILimit, Tool: "weather", Field: "actor", Int: 3, ConfigBase: "旧版本"},
	} {
		if _, err := a.freeAPIAct(ctx, act); err == nil {
			t.Errorf("%s：应当被拒", name)
		}
	}
	if c, _ := config.LoadIn(a.root, a.opt.DataDir); c.FreeAPI.Limits.Weather.PerActorDaily != 7 || c.FreeAPI.Probe.IntervalHours != 24 {
		t.Errorf("被拒的操作不该改配置：%+v", c.FreeAPI)
	}
	if _, err := a.freeAPIAct(ctx, api.FreeAPIAction{Kind: api.FreeAPILimit, Tool: "weather", Field: "actor", Int: 3, ConfigBase: "旧版本"}); !errors.Is(err, config.ErrConfigVersion) {
		t.Errorf("版本冲突应报 ErrConfigVersion：%v", err)
	}
}

func TestFreeAPIProbeNoteSummarizes(t *testing.T) {
	t.Parallel()
	a := openFreeAPIApp(t)
	v, err := a.freeAPIAct(context.Background(), api.FreeAPIAction{Kind: api.FreeAPIProbe, ID: "open-meteo-geo"})
	if err != nil || !strings.Contains(v.Note, "检测") && v.Note == "" {
		t.Fatalf("%v %q", err, v.Note)
	}
	// 一分钟内连点：说明原因
	v, _ = a.freeAPIAct(context.Background(), api.FreeAPIAction{Kind: api.FreeAPIProbe, ID: "open-meteo-geo"})
	if !strings.Contains(v.Note, "一分钟") {
		t.Errorf("连点应提示：%q", v.Note)
	}
}

func findProvider(v api.FreeAPIView, id string) (p struct{ Pinned, Off bool }) {
	for _, c := range v.Categories {
		for _, x := range c.Providers {
			if x.ID == id {
				p.Pinned, p.Off = x.Pinned, x.Off
			}
		}
	}
	return
}
