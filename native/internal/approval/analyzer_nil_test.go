package approval

// 拆命令的分析器为空（装配时忘了给）：原来主人 × 主窗口一跑 powershell 就空指针崩掉。现在 NewGate 默认带一个；
// 真的为空也不崩，当成"拆不开"、要问主人（只会更严）

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
)

// 点了停止：拆命令马上放弃，当成"拆不开"，不能因为等不到结果就放行。yolo 下改成忽略 ctx 的话，
// 这里会真拆出 Get-Date、自动放行，就红了。
func TestGateCancelledWhileAnalyzingBlocks(t *testing.T) {
	g := NewGateWith(t.TempDir(), policy.NewAnalyzer())
	defer g.Analyzer.Close()
	g.Mode = "yolo"
	args, err := jsjson.Parse(`{"command":"Get-Date"}`)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	block, _ := g.Check(ctx, t.TempDir(), "suzu", policy.Audience{Channel: "console", PrivateOk: true}, "console:suzu", Call{ID: "c1", Name: "powershell", Args: args})
	if !block {
		t.Fatal("等拆命令时被取消，不能放行")
	}
}

func TestGateNilAnalyzer(t *testing.T) {
	g := NewGate(t.TempDir())
	if g.Analyzer == nil {
		t.Fatal("NewGate 默认带上分析器")
	}
	g.Analyzer.Close()
	g.Analyzer = nil
	var asked *Request
	g.Ask = func(_ context.Context, req Request, _ *Pending) Decision {
		asked = &req
		return Decision{Kind: "cancelled"}
	}
	args, err := jsjson.Parse(`{"command":"Get-Date"}`)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := g.Check(context.Background(), t.TempDir(), "suzu", policy.Audience{Channel: "console", PrivateOk: true}, "console:suzu", Call{ID: "c1", Name: "powershell", Args: args})
	if !block || asked == nil || !strings.Contains(strings.Join(asked.Notes, "；"), "拆不开") {
		t.Fatalf("当成拆不开、要问主人：拦下 %v，卡片 %+v", block, asked)
	}
}
