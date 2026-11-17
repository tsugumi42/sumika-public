package turn

// 设置字段从清单结构体读出（规格-插件两层 第二节第 4 条）：标题、说明来自标签，类型照字段，别的类型报错。

import (
	"reflect"
	"testing"
)

type sampleSettings struct {
	Open    bool    `yaml:"open" title:"开着" desc:"说明一句"`
	Limit   int     `yaml:"limit" title:"上限" min:"1" max:"10"`
	Ratio   float64 `yaml:"ratio"`
	Place   string  `yaml:"place" title:"地点"`
	Mode    string  `yaml:"mode" title:"模式" enum:"auto|manual"`
	hidden  int
	Ignored string `yaml:"-"`
}

func TestSettingFieldsFromTags(t *testing.T) {
	t.Parallel()
	got, err := SettingFields(sampleSettings{Limit: 3, Mode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	want := []SettingField{
		{Key: "open", Title: "开着", Desc: "说明一句", Kind: "bool", Default: false},
		{Key: "limit", Title: "上限", Kind: "int", Min: ptrF(1), Max: ptrF(10), Default: 3},
		{Key: "ratio", Title: "ratio", Kind: "float", Default: 0.0},
		{Key: "place", Title: "地点", Kind: "string", Default: ""},
		{Key: "mode", Title: "模式", Kind: "enum", Options: []string{"auto", "manual"}, Default: "auto"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("设置字段不对\n期望 %+v\n实际 %+v", want, got)
	}
}

func TestSettingFieldsRejectsOtherTypes(t *testing.T) {
	t.Parallel()
	type bad struct {
		Tags []string `yaml:"tags"`
	}
	if _, err := SettingFields(bad{}); err == nil {
		t.Fatal("切片类型的设置字段应该报错")
	}
	if _, err := SettingFields(3); err == nil {
		t.Fatal("不是结构体应该报错")
	}
}

func ptrF(f float64) *float64 { return &f }

// 标签写错的设置字段直接报错（插件不启用），不静默：
// enabled 是保留名（config 把它当开关消费了，设置值到不了这里）；枚举默认值不在选项里；min 大于 max；范围用在字符串上；enum 用在数字上。
// 放回旧代码（没有这些检查）就红：返回 nil 错误。
func TestSettingFieldsRejectsBadTags(t *testing.T) {
	t.Parallel()
	type reservedEnabled struct {
		Enabled bool `yaml:"enabled"`
	}
	type badDefault struct {
		Mode string `yaml:"mode" enum:"auto|manual"`
	}
	type badRange struct {
		N int `yaml:"n" min:"5" max:"1"`
	}
	type rangeOnString struct {
		S string `yaml:"s" min:"1"`
	}
	type enumOnInt struct {
		N int `yaml:"n" enum:"1|2"`
	}
	cases := map[string]any{
		"保留名 enabled": reservedEnabled{},
		"枚举默认值不在选项里":  badDefault{Mode: "other"},
		"min 大于 max":  badRange{},
		"范围用在字符串上":    rangeOnString{},
		"enum 用在数字上":  enumOnInt{},
	}
	for name, v := range cases {
		if _, err := SettingFields(v); err == nil {
			t.Errorf("%s：应该报错", name)
		}
	}
	// 枚举默认值在选项里、空默认值，都照常通过
	if _, err := SettingFields(badDefault{Mode: "manual"}); err != nil {
		t.Fatalf("合法的枚举默认值不该报错：%v", err)
	}
	if _, err := SettingFields(badDefault{}); err != nil {
		t.Fatalf("空默认值不该报错：%v", err)
	}
}
