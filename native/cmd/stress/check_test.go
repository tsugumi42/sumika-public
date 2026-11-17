package main

import "testing"

func TestIsolation(t *testing.T) {
	secrets := map[string]string{"a": "secret-a", "b": "secret-b"}
	if got := isolation([]requestRecord{{"a", "secret-a"}, {"b", "tool: secret-b"}}, secrets); len(got) != 0 {
		t.Fatal(got)
	}
	for _, body := range []string{"user: secret-a", "tool: secret-a"} {
		if got := isolation([]requestRecord{{"b", body}}, secrets); len(got) != 1 {
			t.Fatalf("串会话必须报出，实际 %v", got)
		}
	}
}
func TestAccounting(t *testing.T) {
	want := map[string]counts{"a": {3, 6, 15}, "b": {3, 6, 15}}
	if got := accounting(want, want); len(got) != 0 {
		t.Fatal(got)
	}
	for _, bad := range []counts{{2, 6, 15}, {3, 5, 15}, {3, 6, 14}, {4, 6, 15}, {3, 7, 15}, {3, 6, 16}} {
		if got := accounting(want, map[string]counts{"a": bad, "b": want["b"]}); len(got) != 1 {
			t.Fatalf("丢失或重复 %+v 未报出：%v", bad, got)
		}
	}
	if len(accounting(want, map[string]counts{"a": want["a"]})) != 1 {
		t.Fatal("整会话丢失未报出")
	}
}
func TestLinear(t *testing.T) {
	clean := []scalePoint{{10, 100, 100}, {50, 500, 500}, {100, 1000, 1000}}
	if !linear(clean) {
		t.Fatal("线性增长误报")
	}
	for _, bad := range []scalePoint{{100, 2100, 1000}, {100, 1000, 2100}} {
		if linear([]scalePoint{clean[0], clean[1], bad}) {
			t.Fatalf("超线性增长未报出：%+v", bad)
		}
	}
	if linear(clean[:1]) {
		t.Fatal("只有一档不能证明线性")
	}
}
