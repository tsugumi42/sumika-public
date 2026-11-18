package approval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/policy"
)

// 手写的范围字段不是文字、效果大小写不对、带范围的 allow 写进文件的样子（旧版本看不懂，会整条丢掉）。
func TestScopedRulesHardening(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tool-grants.yml")
	text := "grants:\n" +
		"- pattern: Write(a)\n  effect: allow\n  session: 12345\n" + // 数字会话号：allow 整条不要
		"- pattern: Write(b)\n  effect: ask\n  project:\n  - D:\\a\n" + // 列表：ask 当全局
		"- pattern: Write(c)\n  effect: Ask\n" + // 大小写
		"- pattern: Write(d)\n  effect: deny\n  session: 7\n" // deny 带坏范围：当全局
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	g := LoadGrants(file)
	by := map[string]policy.UserRule{}
	for _, r := range g.UserRules() {
		by[r.Raw] = r
	}
	if _, ok := by["Write(a)"]; ok {
		t.Error("session 写成数字的 allow 不该变成全局放行")
	}
	if r, ok := by["Write(b)"]; !ok || r.Effect != "ask" || r.Project != "" {
		t.Errorf("project 写成列表的 ask 该当全局：%+v %v", r, ok)
	}
	if r, ok := by["Write(c)"]; !ok || r.Effect != "ask" {
		t.Errorf("Ask 大小写该认：%+v %v", r, ok)
	}
	if r, ok := by["Write(d)"]; !ok || r.Effect != "deny" {
		t.Errorf("deny 照拦：%+v %v", r, ok)
	}
	// 写进文件的带范围 allow 用旧版不认的 effect，读回还是 allow
	file2 := filepath.Join(t.TempDir(), "g.yml")
	g2 := LoadGrants(file2)
	if _, err := g2.PutScopedRule("Write(x)", "allow", `D:\Work`, "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := g2.PutRule("Write(y)", "allow", 1); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file2)
	if !strings.Contains(string(b), "effect: allow-scoped") || strings.Count(string(b), "effect: allow\n") != 1 {
		t.Fatalf("带范围的 allow 该写成旧版不认的 effect，全局的照旧：\n%s", b)
	}
	if _, err := policy.ParseUserRule("Write(x)", "allow-scoped"); err == nil {
		t.Fatal("旧版的效果校验必须不认 allow-scoped")
	}
	back := LoadGrants(file2)
	var got policy.UserRule
	for _, r := range back.UserRules() {
		if r.Raw == "Write(x)" {
			got = r
		}
	}
	if got.Effect != "allow" || got.Project != `D:\Work` {
		t.Fatalf("读回来该是带范围的 allow：%+v", got)
	}
	for _, r := range back.List() {
		if r.Pattern == "Write(x)" && (r.Effect != "allow" || r.Project != `D:\Work`) {
			t.Fatalf("规则页读回：%+v", r)
		}
	}
	if len(back.BadRules) != 0 {
		t.Fatalf("不该有写坏的：%v", back.BadRules)
	}
}
