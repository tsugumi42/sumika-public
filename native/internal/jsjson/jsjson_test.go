package jsjson

import (
	"encoding/json"
	"os"
	"testing"
)

// 和 Bun 的 JSON.stringify(JSON.parse(输入)) 逐条比（testdata/vectors.json 是 scripts/go-golden-json.ts 用 Bun 跑出来的）
func TestSameAsBun(t *testing.T) {
	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Vectors []struct{ Input, Output string } `json:"vectors"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Vectors) < 3000 {
		t.Fatalf("向量只有 %d 条", len(f.Vectors))
	}
	bad := 0
	for _, v := range f.Vectors {
		p, err := Parse(v.Input)
		got := ""
		if err == nil {
			got = Stringify(p)
		}
		if err != nil || got != v.Output {
			bad++
			if bad <= 10 {
				t.Errorf("%q：Go %q（%v），Bun %q", v.Input, got, err, v.Output)
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d 条和 Bun 不一样（共 %d 条）", bad, len(f.Vectors))
	}
}

// 值后面除了空白不能有别的：JSON.parse 都报错（以前 Go 把"后面解析出错"当成正常结束，坏参数照样执行）
func TestParseTrailing(t *testing.T) {
	for _, s := range []string{`{"a":1}#`, `{"a":1} {"b":2}`, `[1]x`, `{"a":1}}`, `"x"'`} {
		if _, err := Parse(s); err == nil {
			t.Errorf("%s 应该不合法", s)
		}
	}
	for _, s := range []string{"{\"a\":1}  \n", "[1]\t", " 2 "} {
		if _, err := Parse(s); err != nil {
			t.Errorf("%s 应该合法：%v", s, err)
		}
	}
}
