package worker

import (
	"strings"
	"testing"
)

func TestVersionShape(t *testing.T) {
	if len(strings.Split(Version, ".")) != 3 {
		t.Fatalf("版本号不是 x.y.z：%q", Version)
	}
	if len(strings.Split(MinCompatible, ".")) != 3 {
		t.Fatalf("最低兼容版本不是 x.y.z：%q", MinCompatible)
	}
}
