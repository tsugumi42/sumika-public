package app

import (
	"fmt"
	"testing"

	"sumika-native/internal/store"
)

// 配置静音不能取得用户规则的归属，恢复非零时也不能删掉它。
func TestConfigZeroPreservesUserStarAcrossReopens(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"用户0.1", "用户限时0", "原来没有规则"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			a := e.open()
			var want []store.GateRule
			if kind != "原来没有规则" {
				r := store.GateRule{Conversation: "*", Frequency: 0.1}
				if kind == "用户限时0" {
					r.Frequency = 0
					r.Until = e.now.AddDate(0, 0, 1).UnixMilli()
				}
				if err := a.set.TraceStore().SetGateRule(r, false); err != nil {
					t.Fatal(err)
				}
				want = []store.GateRule{r}
			}
			a.cleanup()
			e.writeConfigFreq(testToken, "0")
			b := e.open()
			rules, err := b.set.TraceStore().GateRules("*")
			if err != nil {
				t.Fatal(err)
			}
			_, _, owned, err := b.set.TraceStore().LoadGateState(autoStarZero)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "原来没有规则" {
				if fmt.Sprint(rules) != fmt.Sprint(want) || owned {
					t.Errorf("配置0接管用户规则：规则=%+v 标记=%v，要 %+v 且无标记", rules, owned, want)
				}
			} else if len(rules) != 1 || rules[0].Frequency != 0 || rules[0].Until != 0 || !owned {
				t.Errorf("无用户规则时没有生成自有永久0：%+v 标记=%v", rules, owned)
			}
			b.cleanup()
			e.writeConfigFreq(testToken, "0.3")
			c := e.open()
			defer c.cleanup()
			rules, err = c.set.TraceStore().GateRules("*")
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(rules) != fmt.Sprint(want) {
				t.Errorf("恢复0.3后用户规则丢失：%+v，要 %+v", rules, want)
			}
			_, _, owned, err = c.set.TraceStore().LoadGateState(autoStarZero)
			if err != nil {
				t.Fatal(err)
			}
			if owned {
				t.Error("恢复非零后仍有装配标记")
			}
		})
	}
}
