package textclean

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type vHit struct {
	Kind string `json:"kind"`
	S    string `json:"s"`
	Cps  string `json:"cps"`
}

type vJunk struct {
	Text    string `json:"text"`
	Dropped []vHit `json:"dropped"`
	Suspect []vHit `json:"suspect"`
}

type vector struct {
	Input       string `json:"input"`
	Tail        vJunk  `json:"tail"`
	TailForeign vJunk  `json:"tailForeign"`
	Brackets    struct {
		Text    string `json:"text"`
		Dropped int    `json:"dropped"`
		Lone    int    `json:"lone"`
	} `json:"brackets"`
}

func toV(j Junk) vJunk {
	conv := func(hs []Hit) []vHit {
		out := []vHit{}
		for _, h := range hs {
			out = append(out, vHit{h.Kind, h.S, h.Cps})
		}
		return out
	}
	return vJunk{Text: j.Text, Dropped: conv(j.Dropped), Suspect: conv(j.Suspect)}
}

// 和 Bun 版逐条比（testdata/vectors.json 是 scripts/go-golden-clean.ts 用 Bun 跑出来的）
func TestSameAsBun(t *testing.T) {
	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Vectors []vector `json:"vectors"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Vectors) < 4000 {
		t.Fatalf("向量只有 %d 条", len(f.Vectors))
	}
	bad := 0
	for _, v := range f.Vectors {
		for _, c := range []struct {
			name string
			got  vJunk
			want vJunk
		}{
			{"句尾乱码", toV(DropTailJunk(v.Input, Options{})), v.Tail},
			{"句尾乱码（删外文）", toV(DropTailJunk(v.Input, Options{DropForeign: true})), v.TailForeign},
		} {
			if !reflect.DeepEqual(c.got, c.want) {
				bad++
				if bad <= 10 {
					t.Errorf("%s %q：\nGo  %+v\nBun %+v", c.name, v.Input, c.got, c.want)
				}
			}
		}
		g := DropStrayBrackets(v.Input)
		if g.Text != v.Brackets.Text || g.Dropped != v.Brackets.Dropped || g.Lone != v.Brackets.Lone {
			bad++
			if bad <= 10 {
				t.Errorf("】【 %q：Go %+v Bun %+v", v.Input, g, v.Brackets)
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d 处和 Bun 不一样（共 %d 条输入）", bad, len(f.Vectors))
	}
}

// 清两遍 = 清一遍（LESSONS"清理类变换先写穷举断言"）
func TestIdempotent(t *testing.T) {
	b, _ := os.ReadFile("testdata/vectors.json")
	var f struct {
		Vectors []vector `json:"vectors"`
	}
	json.Unmarshal(b, &f)
	for _, v := range f.Vectors {
		for _, o := range []Options{{}, {DropForeign: true}} {
			once := DropTailJunk(v.Input, o).Text
			if twice := DropTailJunk(once, o).Text; twice != once {
				t.Errorf("句尾乱码清两遍和一遍不一样：%q → %q → %q", v.Input, once, twice)
			}
		}
		once := DropStrayBrackets(v.Input).Text
		if twice := DropStrayBrackets(once).Text; twice != once {
			t.Errorf("】【清两遍和一遍不一样：%q → %q → %q", v.Input, once, twice)
		}
	}
}

func TestTrimLikeJS(t *testing.T) {
	// U+0085 JS 不算空白（Go 的 TrimSpace 算），U+FEFF JS 算（Go 不算）
	if got := Trim("\u0085好\u0085"); got != "\u0085好\u0085" {
		t.Fatalf("U+0085 不该去掉：%q", got)
	}
	if got := Trim("\ufeff 好\u3000"); got != "好" {
		t.Fatalf("U+FEFF、全角空格要去掉：%q", got)
	}
}
