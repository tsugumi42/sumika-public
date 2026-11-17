package selfcheck_test

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/selfcheck"
)

// S13：没接 → unknown；关着 → ok；正常 → ok；启动中 → unknown；legacy/off/down → bad，原因（缺模型时带完整路径）进例子和备注
func TestEmbedUnavailableCheck(t *testing.T) {
	c := findCheck(t, "embed.unavailable", selfcheck.DefaultThresholds())
	eval := func(f func() selfcheck.EmbedState) selfcheck.Result {
		env := envAt(t0, selfcheck.NewCounters())
		env.Embed = f
		return c.Eval(context.Background(), env)
	}
	if r := eval(nil); r.State != selfcheck.Unknown {
		t.Fatalf("没接嵌入状态：%+v", r)
	}
	if r := eval(func() selfcheck.EmbedState { return selfcheck.EmbedState{Enabled: false} }); r.State != selfcheck.Ok {
		t.Fatalf("嵌入关着是故意的，不算异常：%+v", r)
	}
	if r := eval(func() selfcheck.EmbedState {
		return selfcheck.EmbedState{Enabled: true, Backend: "go-models", Status: "ready"}
	}); r.State != selfcheck.Ok {
		t.Fatalf("就绪：%+v", r)
	}
	if r := eval(func() selfcheck.EmbedState {
		return selfcheck.EmbedState{Enabled: true, Backend: "go-models", Status: "starting"}
	}); r.State != selfcheck.Unknown {
		t.Fatalf("启动中不该急着报：%+v", r)
	}
	r := eval(func() selfcheck.EmbedState {
		return selfcheck.EmbedState{Enabled: true, Backend: "legacy", Status: "down", Reason: "embed.backend: legacy 已移除"}
	})
	if r.State != selfcheck.Bad || r.Example["backend"] != "legacy" || !strings.Contains(r.Next, "go-models") {
		t.Fatalf("legacy 要报 bad 并在 Next 里说怎么改：%+v", r)
	}
	const looking = `D:\数据\models\_待分类\gte-multilingual-base\model_quantized.onnx`
	r = eval(func() selfcheck.EmbedState {
		return selfcheck.EmbedState{Enabled: true, Backend: "go-models", Status: "down", Reason: "找不到模型文件：" + looking + "。把它放到模型目录里。\n第二行"}
	})
	if r.State != selfcheck.Bad || r.Value != 1 || r.Threshold != 1 {
		t.Fatalf("缺模型要报 bad：%+v", r)
	}
	if !strings.Contains(r.Example["reason"], looking) || !strings.Contains(r.Note, looking) {
		t.Fatalf("例子里要带它在找的完整路径：%+v %q", r.Example, r.Note)
	}
	if strings.Contains(r.Example["reason"], "第二行") {
		t.Fatalf("例子只取第一行（不把 stderr 末尾搬进文件）：%q", r.Example["reason"])
	}
}
