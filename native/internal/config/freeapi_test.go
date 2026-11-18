package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFreeAPIDefaultsAreTheUsersNumbers(t *testing.T) {
	d := Defaults().FreeAPI
	if d.Limits.Weather.PerActorDaily != 20 || d.Limits.Weather.PerConversationHourly != 15 || d.Limits.ExchangeRate.PerActorDaily != 20 || d.Limits.ExchangeRate.PerConversationHourly != 15 {
		t.Errorf("每人每天 20、每群每小时 15：%+v", d.Limits)
	}
	if d.Limits.Calendar.PerActorDaily != 0 || d.Limits.Calendar.PerConversationHourly != 0 {
		t.Errorf("日历默认不限：%+v", d.Limits.Calendar)
	}
	if !d.Probe.Enabled || d.Probe.IntervalHours != 12 || d.NetworkRegion != "cn" {
		t.Errorf("探测默认开、12 小时一次：%+v", d)
	}
	if bad := d.check(); len(bad) != 0 {
		t.Errorf("默认值要合格：%v", bad)
	}
}

func TestFreeAPIPartialYAMLKeepsOtherDefaults(t *testing.T) {
	c, err := Parse([]byte("freeApi:\n  limits:\n    weather:\n      perActorDaily: 5\n  providers:\n    open-meteo: {dailyCap: 100}\n"), "x.yml")
	if err != nil {
		t.Fatal(err)
	}
	f := c.FreeAPI
	if f.Limits.Weather.PerActorDaily != 5 || f.Limits.Weather.PerConversationHourly != 15 || f.Limits.ExchangeRate.PerActorDaily != 20 || !f.Probe.Enabled || f.Probe.IntervalHours != 12 {
		t.Errorf("只写一个值，其余应保持默认：%+v", f)
	}
	if f.Providers["open-meteo"].DailyCap != 100 {
		t.Errorf("提供方覆盖：%+v", f.Providers)
	}
}

func TestFreeAPIRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"区域乱写":   "freeApi:\n  networkRegion: mars\n",
		"间隔太小":   "freeApi:\n  probe:\n    intervalHours: 0\n",
		"间隔太大":   "freeApi:\n  probe:\n    intervalHours: 49\n",
		"次数为负":   "freeApi:\n  limits:\n    weather:\n      perActorDaily: -1\n",
		"次数过大":   "freeApi:\n  limits:\n    exchangeRate:\n      perConversationHourly: 1000000\n",
		"提供方 id": "freeApi:\n  providers:\n    'Bad Id!': {dailyCap: 1}\n",
		"上限为负":   "freeApi:\n  providers:\n    open-meteo: {perMinute: -3}\n",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y), "x.yml"); err == nil {
			t.Errorf("%s：应当被拒", name)
		}
	}
}

// 页面写入的数字要写成整数（不是 12.0），写完能重新读出来
func TestFreeAPISettingWritesIntegers(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sumika.yml")
	raw := []byte("freeApi:\n  probe:\n    enabled: true\n")
	os.WriteFile(file, raw, 0o600)
	c, err := SaveSetting(context.Background(), file, filepath.Join(dir, "history"), ContentVersion(raw), SettingChange{"freeApi.probe.intervalHours", json.Number("24")}, time.Unix(1, 0))
	if err != nil || c.FreeAPI.Probe.IntervalHours != 24 {
		t.Fatalf("%v %+v", err, c.FreeAPI)
	}
	onDisk, _ := os.ReadFile(file)
	if strings.Contains(string(onDisk), "24.0") {
		t.Errorf("整数不该写成 24.0：%s", onDisk)
	}
	// 越界的值写不进去
	if _, err := SaveSetting(context.Background(), file, filepath.Join(dir, "history"), ContentVersion(onDisk), SettingChange{"freeApi.probe.intervalHours", json.Number("99")}, time.Unix(2, 0)); err == nil {
		t.Error("间隔 99 小时应被拒")
	}
	if _, err := SaveSetting(context.Background(), file, filepath.Join(dir, "history"), ContentVersion(onDisk), SettingChange{"freeApi.limits.weather.perActorDaily", json.Number("2.5")}, time.Unix(3, 0)); err == nil {
		t.Error("小数次数应被拒")
	}
}
