package approval

import (
	"context"
	"path/filepath"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
)

// 按会话选工作目录：审批卡要带目录；"这个对话都允许"只在点的那个目录里算。

func TestConvGrantsBoundToWorkdir(t *testing.T) {
	c := NewConvGrants()
	a, b := filepath.Join(t.TempDir(), "项目甲"), filepath.Join(t.TempDir(), "项目乙")
	c.Merge("console:x", Rule{Key: "powershell:git", Workdir: a, Dirs: []string{"D:/外面"}})
	if d, ok := c.Match("console:x", a, "powershell:git", ""); !ok || len(d) != 1 {
		t.Fatalf("在点的目录里应当放行：%v %v", d, ok)
	}
	if _, ok := c.Match("console:x", b, "powershell:git", ""); ok {
		t.Fatal("换了目录，旧目录里点的授权不该放行")
	}
	// Windows 路径不分大小写
	if _, ok := c.Match("console:x", filepath.Clean(a+string(filepath.Separator)), "powershell:git", ""); !ok {
		t.Fatal("同一目录写法不同不该失效")
	}
	// 在新目录里再点一次：只留新目录这份，不带旧目录的外部目录
	c.Merge("console:x", Rule{Key: "powershell:git", Workdir: b, Dirs: []string{"D:/别处"}})
	if d, ok := c.Match("console:x", b, "powershell:git", ""); !ok || len(d) != 1 || d[0] != "D:/别处" {
		t.Fatalf("新目录这份不对：%v %v", d, ok)
	}
	if _, ok := c.Match("console:x", a, "powershell:git", ""); ok {
		t.Fatal("一类授权同时只记一个目录；换回旧目录要重新点")
	}
}

func TestGateCardCarriesWorkdirAndPendingRemembersIt(t *testing.T) {
	g := NewGate(t.TempDir())
	g.Mode = "ask"
	defer g.Analyzer.Close()
	dir := t.TempDir()
	var card Request
	var pend *Pending
	g.Ask = func(_ context.Context, r Request, p *Pending) Decision {
		card, pend = r, p
		return Decision{Kind: "cancelled"}
	}
	args, err := jsjson.Parse(`{"path":"x.txt","content":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	blocked, _ := g.Check(context.Background(), dir, "suzu", policy.Audience{Channel: "console", PrivateOk: true}, "console:suzu", Call{ID: "w1", Name: "write", Args: args})
	if !blocked || card.Workdir != dir || pend == nil || pend.Workdir != dir {
		t.Fatalf("卡片和待决要带目录：blocked=%v card=%q pend=%+v", blocked, card.Workdir, pend)
	}
}
