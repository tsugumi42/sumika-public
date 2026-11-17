package main

// trust 子命令的端到端：命令行 → 管道 → ctlserver 校验 → 假后端；只带 --character，不带参数，被拒的不碰后端。

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

func TestTrustAgainstRealServer(t *testing.T) {
	name := testPipeName("trust")
	ln, err := ctlserver.ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	b := &slashStub{stubBackend: stubBackend{quit: make(chan struct{})}}
	srv := ctlserver.New(b, ctlserver.Options{})
	go srv.Serve(ln)
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, _ bool) (net.Conn, error) { return ctlproto.Dial(ctx, name) }
	do := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb, dial)
		return code, out.String(), errb.String()
	}
	code, out, errs := do("trust", "--character", "example")
	if code != 0 || !strings.Contains(out, "已记下 /trust") || strings.Contains(out, "定期任务") {
		t.Fatalf("trust：%d %q %q", code, out, errs)
	}
	b.smu.Lock()
	got := b.reqs[len(b.reqs)-1]
	n := len(b.reqs)
	b.smu.Unlock()
	if got.Command != "trust" || got.Text != "/trust" || got.Sub != "" || got.Character != "example" {
		t.Fatalf("后端收到：%+v", got)
	}
	for _, args := range [][]string{{"trust", "yes"}, {"trust", "set", "/tmp"}, {"trust", "--character", "a:b"}, {"trust", "--new"}} {
		if code, _, errs := do(args...); code != 3 {
			t.Fatalf("%v 该被拒（退出码 3）：%d %q", args, code, errs)
		}
	}
	b.smu.Lock()
	defer b.smu.Unlock()
	if len(b.reqs) != n {
		t.Fatalf("被拒的请求不该到后端：%d → %d", n, len(b.reqs))
	}
}
