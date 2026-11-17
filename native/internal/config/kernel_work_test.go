package config

import "testing"

func TestWorkConfigurationBounds(t *testing.T) {
	for _, field := range []string{"inlineBytes", "resultMaxBytes", "rootMaxBytes", "readPageBytes", "keepRecentTokens", "summaryMaxTokens", "compactTimeoutMs", "unreferencedDays", "recoveryPageSize"} {
		for _, bad := range []string{"0", "-1", "1.5", "true", "[]", "null"} {
			_, err := Parse([]byte("agent:\n  work:\n    "+field+": "+bad+"\n"), "made.yml")
			if err == nil {
				t.Errorf("接受非法配置 %s=%s", field, bad)
			}
		}
	}
	c, err := Parse([]byte("agent:\n  work:\n    inlineBytes: 32768\n"), "made.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Agent.Work.InlineBytes != 32768 || c.Agent.Work.ResultMaxBytes != 16<<20 {
		t.Fatalf("局部配置丢默认：%+v", c.Agent.Work)
	}
}

func TestStopGraceConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		want       int
		bad        bool
	}{
		{"default", "agent:\n  work:\n    inlineBytes: 32768\n", 5000, false},
		{"zero", "agent:\n  lifecycle:\n    stopGraceMs: 0\n", 0, false},
		{"largest", "agent:\n  lifecycle:\n    stopGraceMs: 9999\n", 9999, false},
		{"small drain", "agent:\n  lifecycle:\n    drainTimeoutMs: 1\n    stopGraceMs: 0\n", 0, false},
		{"negative", "agent:\n  lifecycle:\n    stopGraceMs: -1\n", 0, true},
		{"equal", "agent:\n  lifecycle:\n    stopGraceMs: 10000\n", 0, true},
		{"above", "agent:\n  lifecycle:\n    stopGraceMs: 10001\n", 0, true},
		{"default above drain", "agent:\n  lifecycle:\n    drainTimeoutMs: 5000\n", 2500, false},
		{"written above drain", "agent:\n  lifecycle:\n    drainTimeoutMs: 5000\n    stopGraceMs: 5000\n", 0, true},
		{"fraction", "agent:\n  lifecycle:\n    stopGraceMs: 0.5\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Parse([]byte(tc.yaml), "made.yml")
			if tc.bad {
				if err == nil {
					t.Fatal("accepted invalid grace")
				}
				return
			}
			if err != nil || c.Agent.Lifecycle.StopGraceMS != tc.want {
				t.Fatalf("grace=%d err=%v, want %d", c.Agent.Lifecycle.StopGraceMS, err, tc.want)
			}
		})
	}
}
