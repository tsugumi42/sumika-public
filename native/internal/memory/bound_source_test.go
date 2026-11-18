package memory

import (
	"context"
	"testing"
)

func TestRememberUsesBoundInputsNotLatestMessage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, quote string
		which       int
		wantOK      bool
	}{
		{"earlier admitted input", "我喜欢茶", 1, true},
		{"later unadmitted input", "我喜欢咖啡", 1, false},
		{"missing binding", "我喜欢咖啡", 0, false},
		{"ambiguous quote", "我喜欢", 2, false},
		{"explicit admitted quote", "我喜欢咖啡", 2, true},
		{"invalid binding", "我喜欢茶", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := newSQLEvents(t)
			a := ev.add(ownerSays(tlMain, "我喜欢茶", 0))
			b := ev.add(ownerSays(tlMain, "我喜欢咖啡", 0))
			m, _ := openTools(t, ev)
			var ids []int64
			switch tc.which {
			case 1:
				ids = []int64{a}
			case 2:
				ids = []int64{a, b}
			case -1:
				ids = []int64{a, 0}
			}
			r, err := m.Remember(context.Background(), ownerHere, RememberArgs{InputSeqs: ids, Kind: "preference", Text: "主人喜欢茶", SourceQuote: tc.quote, Temporal: "stable"})
			if err != nil || r.IsError == tc.wantOK {
				t.Fatalf("result: %+v %v", r, err)
			}
			if !tc.wantOK {
				if writes(t, m) != [4]int{} {
					t.Fatal("rejected source wrote data")
				}
				return
			}
			want := a
			if tc.quote == "我喜欢咖啡" {
				want = b
			}
			items := m.Items()
			if len(items) != 1 || len(items[0].SourceSeqs) != 1 || items[0].SourceSeqs[0] != want {
				t.Fatalf("wrong bound source: %+v", items)
			}
		})
	}
}
