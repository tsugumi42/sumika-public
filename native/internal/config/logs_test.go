package config

import (
	"strings"
	"testing"
)

// 规格-日志分级第六节：logs.level 只认四个级别词，logs.modules 的值也一样；默认 info
func TestLogSettingsCheck(t *testing.T) {
	if d := DefaultLogSettings(); d.Level != "info" || len(d.Modules) != 0 {
		t.Fatalf("默认值 %+v", d)
	}
	if bad := (LogSettings{Level: "INFO"}).check(); len(bad) != 0 {
		t.Errorf("大写的级别词该认：%v", bad)
	}
	if bad := (LogSettings{Level: "trace"}).check(); len(bad) != 1 || !strings.Contains(bad[0], "logs.level") {
		t.Errorf("trace 不是级别，要报一条 logs.level：%v", bad)
	}
	if bad := (LogSettings{Level: "info", Modules: map[string]string{"turn": "loud"}}).check(); len(bad) != 1 || !strings.Contains(bad[0], "logs.modules.turn") {
		t.Errorf("模块覆盖的级别写错要报出来：%v", bad)
	}
	if bad := (LogSettings{Level: "info", Modules: map[string]string{"turn": "debug", "snowluma": "WARN"}}).check(); len(bad) != 0 {
		t.Errorf("名单里的模块、级别大小写都不拘：%v", bad)
	}
	// 模块名写错（少个字母、大写）要报，并列出可用名单；名字和级别都错报两条
	bad := (LogSettings{Level: "info", Modules: map[string]string{"snowlum": "debug", "Turn": "debug"}}).check()
	if len(bad) != 2 || !strings.Contains(bad[0], "logs.modules.Turn") || !strings.Contains(bad[1], "logs.modules.snowlum") || !strings.Contains(bad[1], "snowluma") {
		t.Errorf("写错的模块名要报出来并列出可用名单：%v", bad)
	}
	if bad := (LogSettings{Level: "info", Modules: map[string]string{"nope": "loud"}}).check(); len(bad) != 2 {
		t.Errorf("名字和级别都错要报两条：%v", bad)
	}
}
