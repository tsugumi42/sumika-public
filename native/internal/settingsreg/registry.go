package settingsreg

import (
	"reflect"
	"strings"
	"sync"

	"sumika-native/internal/config"
	"sumika-native/internal/desksettings"
)

// Entry 是一个设置键给界面、设置工具和自知投影看的样子，由 records.go 里的记录生成。
// 分类、生效方式、类型、存储在记录里是英文标识，这里出去的是中文显示串。
type Entry struct {
	Key, Name, Type, Storage, Effect, Description, Category string
	Aliases                                                 []string
	Options                                                 []string
	Min, Max                                                *float64
	MinExclusive, MaxExclusive                              bool
	Default                                                 any
	ReadOnly                                                bool
	Reason                                                  string
	// Readers 是后台谁读它（包路径:函数），尚未生效的键为空。
	Readers []string `json:"-"`
	// UI 是设置页给用户看的名字和说明，没有就是 nil。
	UI *Label `json:"-"`
}
type Value struct {
	Entry
	Value  any
	Source string
}

// 生效方式的显示串（出现在 Entry.Effect 里）。立即和下一轮对话是改完就管用；两种重启是读取处只在启动时取一次；
// 尚未生效是后台没有任何地方读它，写了也不会变（界面不出可编辑控件，说明里不许写“重启后生效”）。
const (
	EffectNow            = "立即"
	EffectNextTurn       = "下一轮对话"
	EffectRestartReceive = "重启接收"
	EffectRestartApp     = "重启程序"
	EffectNone           = "尚未生效"
)

// EffectKinds 是全部合法的生效方式（测试用它拦下写错或漏写的）。
var EffectKinds = []string{EffectNow, EffectNextTurn, EffectRestartReceive, EffectRestartApp, EffectNone}

// Inert 说这一项目前没有任何功能读它：写了不会生效。
func (e Entry) Inert() bool { return e.Effect == EffectNone }

const (
	// inertReason 是“后台没人读”的项给界面的原因：只读、不出控件。
	inertReason = "目前没有任何功能读取这项配置，改了不会生效。"
	// fileOnlyReason 是只读项没写专门原因时的说法。
	fileOnlyReason = "目前只能在配置文件中修改。"
)

var (
	catalogOnce sync.Once
	catalog     []Entry
)

// Lookup 按键找登记项；带通配符的登记项（characters.*.model）匹配具体的键（characters.anne.model）。
// 结果和 Entries 同源，设置页、设置工具、自知投影靠它对同一个键说同一句“生不生效”。
func Lookup(key string) (Entry, bool) {
	catalogOnce.Do(func() { catalog = Entries() })
	for _, e := range catalog {
		if e.Key == key {
			return e, true
		}
	}
	for _, e := range catalog {
		prefix, suffix, dynamic := strings.Cut(e.Key, "*")
		if dynamic && !strings.Contains(key, "*") && strings.HasPrefix(key, prefix) && strings.HasSuffix(key, suffix) && len(key) > len(prefix)+len(suffix) {
			return e, true
		}
	}
	return Entry{}, false
}

// LabelOf 是设置页给这个键写的名字和说明；没写的键返回 false。
func LabelOf(key string) (Label, bool) {
	if e, ok := Lookup(key); ok && e.UI != nil {
		return *e.UI, true
	}
	return Label{}, false
}

func appearanceDefault() any { return desksettings.DefaultAppearance() }
func panesDefault() any      { return desksettings.DefaultPanes() }

// entry 把一条记录变成 Entry。类型和默认值从 config 的结构体推出来（yml 存储），其余存储由记录自己给。
func (r record) entry(defaults reflect.Value) Entry {
	e := Entry{Key: r.key, Name: r.name, Storage: storageLabel[r.storage], Effect: effectLabels[r.effect], Description: r.desc,
		Category: categoryText[r.cat], Aliases: append([]string(nil), r.aliases...), ReadOnly: !r.writable, Reason: r.reason,
		Readers: append([]string(nil), r.readers...), UI: r.ui}
	var typ typeID
	if r.storage == "" {
		t, def := schemaValue(defaults, strings.Split(r.key, "."))
		typ, e.Default = typeName(t), def
	}
	if r.def != nil {
		if f, ok := r.def.(func() any); ok {
			e.Default = f()
		} else {
			e.Default = r.def
		}
	}
	if r.options != nil {
		e.Options = append([]string(nil), r.options...)
		typ = tyEnum
	}
	if r.kind != "" {
		typ = r.kind
	}
	e.Type = typeLabels[typ]
	if r.rng != nil {
		lo := r.rng.lo
		e.Min = &lo
		if r.rng.hi != nil {
			hi := *r.rng.hi
			e.Max = &hi
		}
	}
	e.MinExclusive = r.minExclusive
	if e.ReadOnly && e.Reason == "" {
		e.Reason = fileOnlyReason
		if r.effect == fxNone {
			e.Reason = inertReason
		}
	}
	return e
}

func Entries() []Entry {
	defaults := reflect.ValueOf(config.Defaults())
	out := make([]Entry, 0, len(records))
	for _, r := range records {
		out = append(out, r.entry(defaults))
	}
	return out
}
