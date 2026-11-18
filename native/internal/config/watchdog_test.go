package config

import (
	"strings"
	"testing"
)

func TestWatchdogDefaults(t *testing.T) {
	c, err := Parse([]byte("{}\n"), "t.yml")
	if err != nil {
		t.Fatal(err)
	}
	w := c.Watchdog
	if w.HeartbeatSeconds != 60 || w.StuckMinutes != 5 || w.StartGraceMinutes != 3 || w.CrashLimit != 3 ||
		w.CrashWindowMinutes != 10 || w.RestartDelaySeconds != 3 || !w.Notify {
		t.Fatalf("默认值 %+v", w)
	}
}

func TestWatchdogOverrides(t *testing.T) {
	c, err := Parse([]byte("watchdog:\n  heartbeatSeconds: 30\n  stuckMinutes: 8\n  crashLimit: 5\n  notify: false\n"), "t.yml")
	if err != nil {
		t.Fatal(err)
	}
	w := c.Watchdog
	if w.HeartbeatSeconds != 30 || w.StuckMinutes != 8 || w.CrashLimit != 5 || w.Notify || w.CrashWindowMinutes != 10 {
		t.Fatalf("%+v", w)
	}
}

func TestWatchdogRejectsBadValues(t *testing.T) {
	for _, c := range []struct{ yml, want string }{
		{"heartbeatSeconds: 1", "heartbeatSeconds"},
		{"heartbeatSeconds: 601", "heartbeatSeconds"},
		{"stuckMinutes: 1", "stuckMinutes"},
		{"startGraceMinutes: 0", "startGraceMinutes"},
		{"crashLimit: 0", "crashLimit"},
		{"crashWindowMinutes: 0", "crashWindowMinutes"},
		{"restartDelaySeconds: -1", "restartDelaySeconds"},
		{"heartbeatSeconds: 300\n  stuckMinutes: 5", "至少要有 heartbeatSeconds 的三倍"},
	} {
		_, err := Parse([]byte("watchdog:\n  "+c.yml+"\n"), "t.yml")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q：想要报 %q，实际 %v", c.yml, c.want, err)
		}
	}
}
