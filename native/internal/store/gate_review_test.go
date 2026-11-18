package store

import (
	"errors"
	"fmt"
	"testing"
)

// 测试钩子在两步之间失败，写入和删除都必须回滚到操作前。
func TestGateRuleStateRollback(t *testing.T) {
	t.Parallel()
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprint("remove=", remove), func(t *testing.T) {
			t.Parallel()
			s, _ := fresh(t)
			rule := GateRule{Conversation: "*", Frequency: 0}
			const key = "app:auto-star-zero"
			if remove {
				if err := s.SetGateRuleWithState(&rule, key, false, 100); err != nil {
					t.Fatal(err)
				}
			}
			broken := errors.New("编的两步之间失败")
			err := s.setGateRuleWithState(&rule, key, remove, 200, func() error { return broken })
			if !errors.Is(err, broken) {
				t.Errorf("没有返回测试钩子的错误：%v", err)
			}
			rules, err := s.GateRules("*")
			if err != nil {
				t.Fatal(err)
			}
			_, _, owned, err := s.LoadGateState(key)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if remove {
				want = 1
			}
			if len(rules) != want || owned != remove {
				t.Fatalf("中途失败没有回滚：rules=%+v owned=%v", rules, owned)
			}
		})
	}
}
