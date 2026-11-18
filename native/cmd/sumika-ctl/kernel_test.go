package main

import (
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

func TestParseKernelArgs(t *testing.T) {
	o, err := parseArgs([]string{"kernel", "retry", "c1", "console:c1"})
	if err != nil || o.req.Cmd != ctlproto.CmdKernel || strings.Join(o.req.Args, "|") != "retry|c1|console:c1" {
		t.Fatalf("%v %+v", err, o.req)
	}
	o, err = parseArgs([]string{"kernel", "faults"})
	if err != nil || o.req.Wait != "" || len(o.req.Args) != 1 {
		t.Fatalf("%v %+v", err, o.req)
	}
}

func TestRenderKernelFaults(t *testing.T) {
	var out, errOut strings.Builder
	r := ctlproto.Reply{OK: true, Data: []byte(`{"faults":[{"scope":"conversation","character":"c1","conversation":"console:c1","kind":"run_state","reason":"上一轮结束时，状态没能写进记录库","detail":"database is locked","at":1763000000000,"tries":2},{"scope":"kernel","kind":"startup_recover","reason":"启动时收拾上次没处理完的话没做成","at":1763000000000,"tries":0}]}`)}
	renderKernel(&out, &errOut, ctlproto.Request{Cmd: "kernel", Args: []string{"faults"}}, r)
	got := out.String()
	for _, want := range []string{"2 处故障", "会话 console:c1（角色 c1）", "整个内核（所有会话都停止受理）", "已重试 2 次", "最近的原始错误：database is locked", "kernel retry"} {
		if !strings.Contains(got, want) {
			t.Errorf("少了 %q：\n%s", want, got)
		}
	}
	out.Reset()
	renderKernel(&out, &errOut, ctlproto.Request{Cmd: "kernel", Args: []string{"faults"}}, ctlproto.Reply{OK: true, Data: []byte(`{"faults":[]}`)})
	if !strings.Contains(out.String(), "没有故障") {
		t.Fatalf("没有故障要说没有：%q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatal(errOut.String())
	}
}

// 重试没恢复：退出码 1、stderr 里写“没有恢复”和还没恢复的那些；绝不打印成功的话
func TestKernelRetryFailureIsNotPrintedAsSuccess(t *testing.T) {
	f := newFakeServer(t, func(ctlproto.Request) string {
		return `{"ok":false,"code":"failed","error":"没有恢复：这个对话暂时没法继续","data":{"cleared":[],"remaining":[{"scope":"conversation","character":"c1","conversation":"console:c1","kind":"run_state","reason":"上一轮结束时，状态没能写进记录库","detail":"made","at":1763000000000,"tries":1}]}}`
	})
	code, stdout, stderr := runWith(t, f, "kernel", "retry", "c1", "console:c1")
	if code != ctlproto.ExitFailed {
		t.Fatalf("没恢复该是退出码 %d：%d", ctlproto.ExitFailed, code)
	}
	if strings.Contains(stdout, "已恢复") || !strings.Contains(stderr, "没有恢复") || !strings.Contains(stderr, "还没恢复的 1 处") || !strings.Contains(stderr, "console:c1") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestKernelRetrySuccessAndNothingToRetry(t *testing.T) {
	f := newFakeServer(t, okReply(`{"cleared":[{"scope":"conversation","character":"c1","conversation":"console:c1","kind":"run_state","reason":"状态没写进","at":1763000000000,"tries":1}],"remaining":[]}`))
	code, stdout, _ := runWith(t, f, "kernel", "retry")
	if code != ctlproto.ExitOK || !strings.Contains(stdout, "已恢复 1 处") {
		t.Fatalf("%d %q", code, stdout)
	}
	if got := f.only(t); got.Cmd != ctlproto.CmdKernel || len(got.Args) != 1 || got.Args[0] != "retry" {
		t.Fatalf("%+v", got)
	}
	f = newFakeServer(t, okReply(`{"cleared":[],"remaining":[]}`))
	code, stdout, _ = runWith(t, f, "kernel", "retry")
	if code != ctlproto.ExitOK || !strings.Contains(stdout, "没有要重试的故障") {
		t.Fatalf("%d %q", code, stdout)
	}
}
