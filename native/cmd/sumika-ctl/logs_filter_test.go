package main

import "testing"

// 规格-日志分级第八节：logs 的 --level、--module、--errors 只用于 logs，其他命令报错
func TestParseLogsFilterFlags(t *testing.T) {
	cases := []struct {
		args          []string
		level, module string
	}{
		{[]string{"logs", "--level", "warn", "--module", "snowluma"}, "warn", "snowluma"},
		{[]string{"logs", "--level=error"}, "error", ""},
		{[]string{"logs", "--errors"}, "error", ""},
		{[]string{"logs", "-n", "5", "--module=turn"}, "", "turn"},
	}
	for _, c := range cases {
		o, err := parseArgs(c.args)
		if err != nil {
			t.Fatalf("%v：%v", c.args, err)
		}
		if o.req.Cmd != "logs" || o.req.Level != c.level || o.req.Module != c.module {
			t.Errorf("%v：得到 cmd=%q level=%q module=%q", c.args, o.req.Cmd, o.req.Level, o.req.Module)
		}
	}
	for _, args := range [][]string{{"status", "--level", "warn"}, {"logs", "--level"}, {"logs", "--module"}} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%v 该报错", args)
		}
	}
}
