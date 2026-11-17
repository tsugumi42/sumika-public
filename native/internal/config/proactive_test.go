package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProactiveDefaultsOffAndMatchSpec(t *testing.T) {
	c, err := Parse([]byte("console:\n  port: 7720\n"), "made.yml")
	if err != nil {
		t.Fatal(err)
	}
	p := c.Proactive
	if p.Enabled || len(p.Characters) != 0 || p.Send != "live" {
		t.Fatalf("默认要关、不列角色：%+v", p)
	}
	if p.BaseMinMinutes != 120 || p.BaseMaxMinutes != 180 || p.MinGapMinutes != 45 || p.MaxGapHours != 12 ||
		p.HeatFactor != 0.7 || p.ColdFactor != 1.5 || p.FactorMin != 0.5 || p.FactorMax != 3 ||
		p.FollowUpMinutes != 20 || p.MaxFollowUps != 2 || p.Sleep.Start != "01:00" || p.Sleep.End != "09:00" ||
		p.ActiveWithinMinutes != 30 || p.ChattingWithinMinutes != 10 || p.DeferMinutes != 15 ||
		p.MaxThreadsPerDay != 4 || p.MaxSendsPerThread != 3 || p.MaxOpenThreads != 1 ||
		p.ClaimWindowMinutes != 10 || p.ClaimGraceMinutes != 2 || p.ChatIdleMinutes != 10 ||
		p.CloseAfterHours != 6 || p.PresenceWindowHours != 24 || p.SnoozeDefaultHours != 24 || p.SnoozeMaxDays != 7 ||
		p.DecisionLogDays != 90 || !p.Notify {
		t.Fatalf("默认值和规格第七节配置表不一致：%+v", p)
	}
}

func TestProactivePartialConfigKeepsDefaults(t *testing.T) {
	c, err := Parse([]byte("proactive:\n  enabled: true\n  characters: [aa]\n  sleep:\n    start: \"23:00\"\n"), "made.yml")
	if err != nil {
		t.Fatal(err)
	}
	p := c.Proactive
	if !p.Enabled || len(p.Characters) != 1 || p.Sleep.Start != "23:00" || p.Sleep.End != "09:00" || p.BaseMinMinutes != 120 {
		t.Fatalf("局部配置丢默认：%+v", p)
	}
}

func TestProactiveRejectsBadValues(t *testing.T) {
	for _, bad := range []string{
		"send: both", "send: \"\"",
		"baseMinMinutes: 0", "baseMinMinutes: 200", "baseMaxMinutes: 100", "baseMaxMinutes: -5",
		"minGapMinutes: 0", "minGapMinutes: 99999", "maxGapHours: 0",
		"heatFactor: 1", "heatFactor: 0", "heatFactor: 1.2", "coldFactor: 1", "coldFactor: 0.5",
		"factorMin: 0", "factorMin: 2", "factorMax: 0.5",
		"followUpMinutes: 0", "maxFollowUps: -1", "maxThreadsPerDay: 0", "maxOpenThreads: 0",
		"deferMinutes: 0", "activeWithinMinutes: 0", "chattingWithinMinutes: 0",
		"snoozeDefaultHours: 0", "snoozeMaxDays: 0", "snoozeDefaultHours: 400", "decisionLogDays: 0",
		"sleep:\n    start: \"25:00\"", "sleep:\n    start: \"9\"", "sleep:\n    end: \"\"",
		"sleep:\n    start: \"03:00\"\n    end: \"03:00\"",
		"characters: [\" aa\"]", "characters: [aa, aa]", "characters: [\"\"]",
		"enabled: maybe", "baseMinMinutes: 1.5", "notify: 3",
	} {
		if _, err := Parse([]byte("proactive:\n  "+bad+"\n"), "made.yml"); err == nil {
			t.Errorf("接受了非法配置 %q", bad)
		}
	}
	// 跨午夜区间和边界值是合法的
	for _, ok := range []string{"sleep:\n    start: \"23:00\"\n    end: \"07:00\"", "baseMinMinutes: 45\n  baseMaxMinutes: 45", "factorMin: 1", "factorMax: 1"} {
		if _, err := Parse([]byte("proactive:\n  "+ok+"\n"), "made.yml"); err != nil {
			t.Errorf("拒绝了合法配置 %q：%v", ok, err)
		}
	}
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]int{"00:00": 0, "01:00": 60, "09:30": 570, "23:59": 1439, " 07:05 ": 425} {
		if got, err := ParseClock(in); err != nil || got != want {
			t.Errorf("%q → %d %v，想要 %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "24:00", "7", "ab:cd", "12:60"} {
		if _, err := ParseClock(bad); err == nil {
			t.Errorf("%q 应当解析失败", bad)
		}
	}
}

// 示例配置里 proactive 段每个键写的值，和代码默认值、规格第七节一致（改默认值时三处一起改）
func TestProactiveExampleMatchesDefaults(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "sumika.example.yml"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(b, "sumika.example.yml")
	if err != nil {
		t.Fatal(err)
	}
	got, want := c.Proactive, DefaultProactive()
	got.Characters = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("示例和默认值不一致：\n示例 %+v\n默认 %+v", got, want)
	}
}
