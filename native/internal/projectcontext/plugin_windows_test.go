//go:build windows

package projectcontext

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/testutil/junction"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

func TestHostRealPathTrustAndProtection(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	file := put(t, root, "sub/AGENTS.md", "规则")
	link := filepath.Join(other, "alias")
	if err := junction.Create(link, root); err != nil {
		t.Fatalf("建临时联接：%v", err)
	}
	ctx := context.Background()
	for _, target := range []string{root, filepath.Join(root, "sub"), link} {
		ok, err := RootsTrust{link}.Trusted(ctx, turn.Workspace{Root: target})
		if err != nil || !ok {
			t.Fatalf("真实信任根没认出 %s：%v %v", target, ok, err)
		}
	}
	if ok, err := (RootsTrust{root}).Trusted(ctx, turn.Workspace{Root: other}); err != nil || ok {
		t.Fatalf("不相关的真实根被信任：%v %v", ok, err)
	}
	err := ReadApproval(dirs(link))(ctx, file)
	if err == nil || !strings.Contains(err.Error(), "审批未通过") {
		t.Fatalf("受保护目录联接没按真实路径拒绝：%v", err)
	}
}

func TestHostRechecksRootBeforeTool(t *testing.T) {
	trusted, untrusted, parent := t.TempDir(), t.TempDir(), t.TempDir()
	put(t, trusted, "AGENTS.md", "已信任的规则")
	put(t, untrusted, "AGENTS.md", "未经信任的规则")
	link := filepath.Join(parent, "workspace")
	join := func(target string) {
		t.Helper()
		if err := junction.Create(link, target); err != nil {
			t.Fatalf("建临时联接：%v", err)
		}
	}
	join(trusted)
	reads := 0
	p := NewPlugin(PluginOptions{Enabled: true, Trust: RootsTrust{trusted}, Resolver: Options{Open: func(path string) (io.ReadCloser, error) { reads++; return os.Open(path) }}})
	p.active = true
	in := turn.ContextIn{ContextID: "ctx", Workspace: turn.Workspace{ID: "w", Root: link, CWD: link}, Audience: audience.Console("local")}
	c, err := p.onContext(context.Background(), in)
	if err != nil || c == nil {
		t.Fatalf("最初的信任没有生效：%+v %v", c, err)
	}
	c.OnAccepted()
	reads = 0
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	join(untrusted)
	args, _ := jsjson.Parse(`{"path":"target.txt"}`)
	r, err := p.Execute(context.Background(), in, "read", args, func(context.Context) (tools.Result, error) { return tools.Result{Text: "普通结果"}, nil })
	if err != nil || r.IsError || reads != 0 || strings.Contains(r.Text, "未经信任的规则") {
		t.Fatalf("根换到未信任目标后仍扫描：%+v %v，读取 %d", r, err, reads)
	}
}

func TestPluginRootReplacement(t *testing.T) {
	for _, kind := range []string{"trusted", "workspace", "scan"} {
		t.Run(kind, func(t *testing.T) {
			root, other, parent := t.TempDir(), t.TempDir(), t.TempDir()
			put(t, root, "AGENTS.md", "原规则")
			put(t, other, "AGENTS.md", "绝不能读的规则")
			link := filepath.Join(parent, "link")
			join := func(target string) {
				t.Helper()
				if err := junction.Create(link, target); err != nil {
					t.Fatalf("建联接：%v", err)
				}
			}
			join(root)
			trusted := root
			if kind == "trusted" {
				trusted = link
			}
			base, err := NewRootsTrust([]string{trusted})
			if err != nil {
				t.Fatal(err)
			}
			barrier := barrierTrust{base: base, entered: make(chan struct{}), release: make(chan struct{})}
			reads := 0
			p := NewPlugin(PluginOptions{Enabled: true, Trust: base, Resolver: Options{Open: func(path string) (io.ReadCloser, error) { reads++; return os.Open(path) }}})
			p.active = true
			in := turn.ContextIn{ContextID: "ctx", Workspace: turn.Workspace{ID: "w", Root: link, CWD: link}, Audience: audience.Console("local")}
			pin, err := PinRoot(link)
			if err != nil {
				t.Fatal(err)
			}
			o := p.opts.Resolver
			o.Root, o.CWD, o.RootIdentity = link, link, pin
			r, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			if kind == "workspace" {
				p.opts.Trust = barrier
				go func() { _, e := p.onContext(ctx, in); done <- e }()
				select {
				case <-barrier.entered:
				case <-ctx.Done():
					t.Fatal("信任屏障未到达")
				}
			}
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			join(other)
			if kind == "scan" {
				snap := r.Discover(context.Background())
				if snap.Complete || reads != 0 {
					t.Fatalf("扫描未核对放行时的身份：完整 %v，读取 %d", snap.Complete, reads)
				}
				return
			}
			if kind == "workspace" {
				close(barrier.release)
			} else {
				_, e := p.onContext(ctx, in)
				done <- e
			}
			select {
			case e := <-done:
				if e == nil {
					t.Error("根替换后仍放行")
				}
			case <-ctx.Done():
				t.Fatal("根核对没有结束")
			}
			if reads != 0 {
				t.Fatalf("根替换后读取了 %d 次正文", reads)
			}
		})
	}
}
