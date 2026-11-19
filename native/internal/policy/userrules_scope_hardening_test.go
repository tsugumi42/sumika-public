package policy

import "testing"

// 复核后补的几处：key 记号、设备路径、联接指出去的工作目录、域名末尾的点。
func TestScopeHardening(t *testing.T) {
	// 规则原文、项目目录里不能带存储用来标范围的记号（key 会撞）；设备路径写法不收
	if _, err := ParseUserRule(`Write(a)@dir:d:\x`, "ask"); err == nil {
		t.Error("原文带 @dir: 该报错")
	}
	if _, err := ParseUserRule(`Write(a@conv:x)`, "ask"); err == nil {
		t.Error("原文带 @conv: 该报错")
	}
	for _, p := range []string{`D:\a@b`, `\\?\D:\Work`, `\\.\D:\Work`} {
		if _, err := ParseScopedRule("Write", "ask", p, ""); err == nil {
			t.Errorf("项目 %q 该报错", p)
		}
	}
	// 工作目录是联接：字面在项目里、真实路径在外面。allow 不能靠联接进来；ask 任一边在项目里就问
	allow := scoped(t, "Write", "allow", `D:\Work`, "")
	ask := scoped(t, "Write", "ask", `D:\Work`, "")
	env := askBase(allow)
	env.Mode, env.Workdir = "yolo", `D:\Work\link`
	env.Real = map[string]string{linkKey(`D:\Work\link`): `E:\Elsewhere`}
	if d := Decide("write", Specs["write"], ruleArgs("path", "a.go"), env); d.By == ByRule {
		t.Errorf("联接指到项目外面，项目范围的 allow 不能放行：%+v", d)
	}
	env.Rules = []UserRule{ask}
	if d := Decide("write", Specs["write"], ruleArgs("path", "a.go"), env); d.RuleKey != ask.Key {
		t.Errorf("字面在项目里，ask 要问：%+v", d)
	}
	env.Workdir = `E:\Elsewhere`
	env.Real = map[string]string{linkKey(`E:\Elsewhere`): `D:\Work\real`}
	if d := Decide("write", Specs["write"], ruleArgs("path", "a.go"), env); d.RuleKey != ask.Key {
		t.Errorf("真实路径在项目里，ask 也要问：%+v", d)
	}
	// 域名末尾的点：拒绝、ask 当同一个域名；放行规则的比法不放宽
	web := mustRules(t, [2]string{"WebFetch(domain:example.com)", "ask"})
	if d := Decide("web_fetch", Specs["web_fetch"], ruleArgs("url", "https://example.com./x"), askBase(web...)); d.Kind != "ask" {
		t.Errorf("末尾带点的主机名该被 ask 盖住：%+v", d)
	}
	if domainHits("example.com", "https://example.com./x", false) {
		t.Error("放行规则的域名比法不该放宽")
	}
	deny := mustRules(t, [2]string{"WebFetch(domain:example.com)", "deny"})
	if d := Decide("web_fetch", Specs["web_fetch"], ruleArgs("url", "https://example.com./x"), askBase(deny...)); d.Kind != "deny" {
		t.Errorf("末尾带点的主机名该被拒绝规则盖住：%+v", d)
	}
}
