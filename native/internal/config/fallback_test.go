package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestFallbackModelConfig(t *testing.T) {
	c, err := Parse([]byte(`
defaults:
  fallbackModels: [backup, third]
characters:
  inherit: { model: primary }
  blank: { model: primary, fallbackModels: null }
  off: { model: primary, fallbackModels: [] }
  own: { model: primary, fallbackModels: [own-backup] }
`), "fixture.yml")
	if err != nil {
		t.Fatal(err)
	}
	v := ModelsOf(c)
	if got := v.Fallback("inherit"); !reflect.DeepEqual(got, []string{"backup", "third"}) {
		t.Fatalf("省略该继承：%v", got)
	}
	if got := v.Fallback("blank"); !reflect.DeepEqual(got, []string{"backup", "third"}) {
		t.Fatalf("null 该继承：%v", got)
	}
	if got := v.Fallback("off"); got == nil || len(got) != 0 {
		t.Fatalf("显式空链该禁用而不是继承：%#v", got)
	}
	if got := v.Fallback("own"); !reflect.DeepEqual(got, []string{"own-backup"}) {
		t.Fatalf("角色覆盖：%v", got)
	}
	if got := v.Fallback("missing"); !reflect.DeepEqual(got, []string{"backup", "third"}) {
		t.Fatalf("没有这个角色该用全局链：%v", got)
	}
	for _, raw := range []string{
		"defaults:\n  fallbackModels: [a, a]\n",
		"defaults:\n  fallbackModels: ['', a]\n",
		"characters:\n  a: { model: m, fallbackModels: [m, m] }\n",
	} {
		if _, err := Parse([]byte(raw), "bad.yml"); err == nil || !strings.Contains(err.Error(), "fallbackModels") {
			t.Fatalf("该拒绝 %q：%v", raw, err)
		}
	}
}
