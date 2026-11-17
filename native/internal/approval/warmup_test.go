package approval

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
	chain "sumika-native/internal/toolchain"
)

func TestGateUnavailableAnalyzerAsks(t *testing.T) {
	// PATH 指到空的隔离目录，确实让解析器起不来；不能依赖这台机器有没有装 pwsh。
	// 外部程序登记表还会去自带目录和 Program Files 找（规格-外部程序清单 第 1 条），
	// 所以用不带这些目录的环境重查一次，测完再查回去，别让缓存漏到别的测试。
	t.Setenv("PATH", t.TempDir())
	chain.Refresh(chain.Env{})
	t.Cleanup(func() { chain.Refresh(chain.Env{}) })
	g := NewGate(t.TempDir())
	defer g.Analyzer.Close()
	args, err := jsjson.Parse(`{"command":"Get-Date"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"auto", "yolo"} {
		t.Run(mode, func(t *testing.T) {
			g.Mode = mode
			var asked *Request
			g.Ask = func(_ context.Context, req Request, _ *Pending) Decision {
				asked = &req
				return Decision{Kind: "denied"}
			}
			block, reason := g.Check(context.Background(), t.TempDir(), "c", policy.Audience{Channel: "console", PrivateOk: true}, "console:c", Call{ID: "c1", Name: "powershell", Args: args})
			if !block || !strings.Contains(reason, "拒绝") || asked == nil || !strings.Contains(strings.Join(asked.Notes, "；"), "拆不开") {
				t.Fatalf("解析器起不来应当询问、拒绝后拦下：block=%v reason=%q card=%+v", block, reason, asked)
			}
		})
	}
}
