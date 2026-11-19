package config

import (
	"strings"
	"testing"
)

// 备份的三个新键：默认值、范围检查（规格-数据目录与备份 10.3）
func TestBackupKnobs(t *testing.T) {
	d := Defaults()
	if d.Backup.MinFreeGB != 5 || d.Backup.RunMaxHours != 6 || d.Backup.CloseWaitSeconds != 30 {
		t.Fatalf("默认值不对：%+v", d.Backup)
	}
	t.Setenv("SUMIKA_CONFIG", write(t, "backup: { minFreeGB: 0, runMaxHours: 12, closeWaitSeconds: 10 }\n"))
	c, err := Load(t.TempDir())
	if err != nil || c.Backup.MinFreeGB != 0 || c.Backup.RunMaxHours != 12 || c.Backup.CloseWaitSeconds != 10 {
		t.Fatalf("该读到写的值（minFreeGB: 0 是不查）：%+v %v", c.Backup, err)
	}
	cases := map[string]struct{ yml, want string }{
		"minFreeGB 太大":         {"backup: { minFreeGB: 2000000 }\n", "backup.minFreeGB"},
		"minFreeGB 是 inf":      {"backup: { minFreeGB: .inf }\n", "backup.minFreeGB"},
		"minFreeGB 是 NaN":      {"backup: { minFreeGB: .nan }\n", "backup.minFreeGB"},
		"runMaxHours 是 inf":    {"backup: { runMaxHours: .inf }\n", "backup.runMaxHours"},
		"minFreeGB 是负数":        {"backup: { minFreeGB: -1 }\n", "backup.minFreeGB"},
		"runMaxHours 太小":       {"backup: { runMaxHours: 0.1 }\n", "backup.runMaxHours"},
		"runMaxHours 太大":       {"backup: { runMaxHours: 100 }\n", "backup.runMaxHours"},
		"closeWaitSeconds 是 0": {"backup: { closeWaitSeconds: 0 }\n", "backup.closeWaitSeconds"},
		"closeWaitSeconds 太大":  {"backup: { closeWaitSeconds: 9999 }\n", "backup.closeWaitSeconds"},
	}
	for name, tc := range cases {
		t.Setenv("SUMIKA_CONFIG", write(t, tc.yml))
		if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：应该报出 %q，得到 %v", name, tc.want, err)
		}
	}
}
