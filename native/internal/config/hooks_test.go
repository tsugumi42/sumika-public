package config

import "testing"

func TestHooksDefaultsAndCheck(t *testing.T) {
	d := Defaults()
	if d.Hooks != DefaultHooks() || !d.Hooks.Enabled || len(d.Hooks.check()) != 0 {
		t.Fatalf("默认值：%+v", d.Hooks)
	}
	c, err := Parse([]byte("hooks:\n  timeoutSeconds: 30\n  stopMaxContinues: 0\n"), "t.yml")
	if err != nil || c.Hooks.TimeoutSeconds != 30 || c.Hooks.StopMaxContinues != 0 || c.Hooks.MaxParallel != 4 || !c.Hooks.Enabled {
		t.Fatalf("部分覆盖要保住别的默认值：%+v %v", c.Hooks, err)
	}
	for name, raw := range map[string]string{
		"时限 0": "hooks:\n  timeoutSeconds: 0\n",
		"时限太大": "hooks:\n  timeoutSeconds: 601\n",
		"输出太小": "hooks:\n  maxOutputBytes: 100\n",
		"并发 0": "hooks:\n  maxParallel: 0\n",
		"续跑负数": "hooks:\n  stopMaxContinues: -1\n",
		"续跑太多": "hooks:\n  stopMaxContinues: 21\n",
	} {
		if _, err := Parse([]byte(raw), "t.yml"); err == nil {
			t.Errorf("%s：该报错", name)
		}
	}
}
