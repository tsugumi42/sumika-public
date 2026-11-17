package dbguard

import (
	"errors"
	"strings"
	"testing"
)

func TestUnavailableMessage(t *testing.T) {
	err := &Unavailable{
		File:     `C:\data\go\chat\qq.db`,
		Track:    "qq",
		Reason:   ReasonRestoreFailed,
		Detail:   `原库和快照都要人看：C:\data\go\chat\qq.db、C:\data\go\upgrade-snapshots\qq.sqlite`,
		Snapshot: `C:\data\go\upgrade-snapshots\qq.sqlite`,
	}
	msg := err.Error()
	for _, want := range []string{ReasonRestoreFailed, "qq.db", "qq", "快照在", "upgrade-snapshots"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Error() = %q，缺 %q", msg, want)
		}
	}
	var target *Unavailable
	if !errors.As(err, &target) || target != err {
		t.Fatal("errors.As 取不到 *Unavailable")
	}
}
