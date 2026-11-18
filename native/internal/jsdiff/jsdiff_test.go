package jsdiff

// 对拍（规格-Go后台 四点七 2d）：testdata/diff.json 是 scripts/go-golden-policy.test.ts 让 Bun 算的（jsdiff 的 diffLines 和 pi 的 generateDiffString），
// 随机文本对 400 对 + 手写的边角，输入连同结果都在里面

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestSameAsBun(t *testing.T) {
	b, err := os.ReadFile("testdata/diff.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Old   string `json:"old"`
		New   string `json:"new"`
		Parts []struct {
			Value   string `json:"value"`
			Count   int    `json:"count"`
			Added   bool   `json:"added"`
			Removed bool   `json:"removed"`
		} `json:"parts"`
		Diff  string `json:"diff"`
		First *int   `json:"firstChangedLine"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		var want []Part
		for _, p := range c.Parts {
			want = append(want, Part{Value: p.Value, Count: p.Count, Added: p.Added, Removed: p.Removed})
		}
		got := DiffLines(c.Old, c.New)
		d, first, has := GenerateDiffString(c.Old, c.New, 4)
		wantFirst, wantHas := 0, c.First != nil
		if wantHas {
			wantFirst = *c.First
		}
		if !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) || d != c.Diff || first != wantFirst || has != wantHas {
			bad++
			if bad <= 10 {
				t.Errorf("%q → %q 不一样\n Go：%+v\n%s\nBun：%+v\n%s", c.Old, c.New, got, d, want, c.Diff)
			}
		}
	}
	t.Logf("%d 对，不一样的 %d 对", len(cases), bad)
}
