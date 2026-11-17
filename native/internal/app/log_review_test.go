package app

import (
	"context"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
)

// 真实 WS 关闭帧的原因可以带聊天正文，适配器自己的出口也必须经过装配日志。
func TestAdapterCloseLogsOmitPeerText(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.start()
	c := e.peer.accept(t)
	if err := c.Close(websocket.StatusPolicyViolation, "group:300 群 300 编的聊天原文"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "适配器断开日志进入统一出口", func() bool { return e.logContains("连接断了，按退避重连") })
	waitFor(t, "装配收到断开状态", func() bool { return e.logContains("QQ 断了") })
	for _, line := range e.logSnapshot() {
		if strings.Contains(line, "编的聊天原文") || strings.Contains(line, "300") {
			t.Errorf("关闭原因泄露聊天或号码：%s", line)
		}
	}
	if !e.logContains("1008") {
		t.Error("关闭日志没有保留关闭码 1008")
	}
}

func TestLogMasksShortGroupForms(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.cleanup()
	a.logf("group:300，群 300，群300")
	logs := e.logSnapshot()
	if got := logs[len(logs)-1]; got != "group:***00，群 ***00，群***00" {
		t.Fatalf("短群号写法漏码：%q", got)
	}
}

// 只按固定模板认模型错误：别的错误（"收拾上一个进程没收尾的回合出错"）原样留着，号码照旧打码。
func TestLogKeepsNonModelErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.cleanup()
	a.logf("收拾上一个进程没收尾的回合出错：database is locked（会话 qq:group:300）")
	logs := e.logSnapshot()
	if got := logs[len(logs)-1]; got != "收拾上一个进程没收尾的回合出错：database is locked（会话 qq:group:***00）" {
		t.Fatalf("非模型错误被改写：%q", got)
	}
}

// 下层带正文记模型错误的几句模板，正文都要丢，能认出来的状态码留着。
func TestLogDropsModelErrorBodies(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.cleanup()
	cases := []struct {
		format string
		args   []any
		want   string
	}{
		{"回合 %s 第 %d 步的模型请求出错：%s；%.1f 秒以后再请求（第 %d 次重试，最多 %d 次）",
			[]any{"编的回合", 2, "400: 编的聊天原文", 0.5, 1, 3},
			"回合 编的回合 第 2 步的模型请求出错：网关 HTTP 错误，状态码 400"},
		{"会话 %s 压缩的摘要请求出错：%s；%.1f 秒以后再请求（第 %d 次重试）",
			[]any{"qq:group:300", "编的聊天原文", 0.5, 1},
			"会话 qq:group:***00 压缩的摘要请求出错：模型请求失败（原因未分类）"},
		{"会话 %s 压缩没成（%s）：只带最近的话重建",
			[]any{"qq:group:300", "编的聊天原文"},
			"会话 qq:group:***00 压缩没成（模型请求失败（原因未分类）"},
		{"会话 %s 后台提前压缩没用上：%s",
			[]any{"qq:group:300", "编的聊天原文"},
			"会话 qq:group:***00 后台提前压缩没用上：模型请求失败（原因未分类）"},
		// 类别要留下：超时、连不上、限流分得出来，原文照样不留（静默失败盘点 二-8）
		{"会话 %s 后台提前压缩没用上：%s",
			[]any{"qq:group:300", "Post \"http://x/v1\": context deadline exceeded 编的聊天原文"},
			"会话 qq:group:***00 后台提前压缩没用上：模型请求失败（超时）"},
		{"会话 %s 后台提前压缩没用上：%s",
			[]any{"qq:group:300", "dial tcp 127.0.0.1:3425: connection refused"},
			"会话 qq:group:***00 后台提前压缩没用上：模型请求失败（连不上网关）"},
		{"会话 %s 计划 %s：%s（回合出错：%s）",
			[]any{"qq:group:300", "编的计划", "error", "500: 编的聊天原文"},
			"会话 qq:group:***00 计划 编的计划：error（回合出错：网关 HTTP 错误，状态码 500"},
	}
	for i, c := range cases {
		a.logf(c.format, c.args...)
		logs := e.logSnapshot()
		if got := logs[len(logs)-1]; got != c.want {
			t.Errorf("第 %d 句：%q，要 %q", i+1, got, c.want)
		}
	}
}

// 直接接装配日志，绕开出口正则，检查号码在拼接之前就打码。
func TestAssemblyMasksBeforeFormatting(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open()
	defer a.cleanup()
	a.logf = e.logf
	s := sinkOf(a)
	ctx := context.Background()
	if err := s.Inbound(ctx, types.Inbound{Account: "400"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Notice(ctx, types.Notice{Account: "500"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Status(ctx, types.Status{Account: "600"}); err != nil {
		t.Fatal(err)
	}
	a.dispatch(channel.Dispatch{Batch: types.Batch{Conv: types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: "100", ID: "700"}, Msgs: []types.Msg{{Who: "qq:700"}}}})
	for _, line := range e.logSnapshot() {
		for _, raw := range []string{"号 400", "号 500", "号 600", "号 100", "private:100:700"} {
			if strings.Contains(line, raw) {
				t.Errorf("装配日志依赖出口才打码：%s", line)
			}
		}
	}
	if !e.logContains("qq:private:***00:***00") {
		t.Error("私聊分流没有记录打码的会话")
	}
}
