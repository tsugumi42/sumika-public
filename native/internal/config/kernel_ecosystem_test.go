package config

import "testing"

func TestEcosystemDisabledAndQuotaValidation(t *testing.T) {
	c, err := ParseEcosystem([]byte("magpie:\n  baseUrl: http://127.0.0.1:3425/v1\nskills:\n  enabled: false\nweb:\n  enabled: false\nsubagents:\n  enabled: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Skills.Enabled || c.Web.Enabled || c.Subagents.Enabled || c.Web.MonthlyMicroCredits != nil {
		t.Fatal("默认开启或自给消费额度")
	}
	for _, raw := range []string{"skills:\n  readBytes: 0\n", "skills:\n  pageItems: 1.5\n", "web:\n  monthlyMicroCredits: 1.5\n", "web:\n  dailySearch: -1\n", "web:\n  maxInflight: 9\n", "web:\n  responseMaxBytes: 5242881\n", "subagents:\n  maxDepth: 2\n", "subagents:\n  maxActivePerRoot: 0\n", "subagents:\n  enabled: true\n"} {
		if _, err := ParseEcosystem([]byte(raw)); err == nil {
			t.Fatalf("配置未拒绝 %q", raw)
		}
	}
}
