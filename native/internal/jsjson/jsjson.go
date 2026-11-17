// Package jsjson 照 JS 的 JSON.parse / JSON.stringify 读写 JSON：Bun 版把模型给的工具参数 JSON.parse 成对象，
// 下一次请求、steps.args_json 再 JSON.stringify 回去（pi openai-completions.js、store.ts stepStart），Go 版要写出同一串字。
// 和 Go 的 encoding/json 不一样的地方：对象的键保留原来的先后，只是像数组下标的键（"0"、"12"）按数字排在最前（JS 对象的属性顺序）；
// 同一个键出现两次时值取后一个、位置留在第一次的地方；数字按 JS 的写法（1.0 写成 1、1e21 写成 1e+21）；字符串只转义 JS 会转义的
package jsjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Kind 是值的种类
type Kind int

const (
	Null Kind = iota
	Bool
	Number
	String
	Array
	Object
)

// Value 是解析出来的一个 JSON 值（对象的键按 JS 的先后存）
type Value struct {
	Kind Kind
	Bool bool
	Num  float64
	Str  string
	Arr  []Value
	Keys []string         // 对象：键的先后（已经按 JS 的规则排好）
	Obj  map[string]Value // 对象：键 → 值
}

// Parse 像 JSON.parse：语法不对返回错误（错误文字和 JS 引擎的不一样，调用方别拿它去和 Bun 比）
func Parse(s string) (Value, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return Value{}, err
	}
	// 后面只能是空白：解析错（"}#"的 #）、还有别的值，都和 JSON.parse 一样算不合法
	if _, err := dec.Token(); err != io.EOF {
		return Value{}, errors.New("JSON 后面还有多余的内容")
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return Value{}, err
	}
	switch t := tok.(type) {
	case nil:
		return Value{Kind: Null}, nil
	case bool:
		return Value{Kind: Bool, Bool: t}, nil
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return Value{}, err
		}
		return Value{Kind: Number, Num: f}, nil
	case string:
		return Value{Kind: String, Str: t}, nil
	case json.Delim:
		if t == '[' {
			v := Value{Kind: Array, Arr: []Value{}}
			for dec.More() {
				e, err := parseValue(dec)
				if err != nil {
					return Value{}, err
				}
				v.Arr = append(v.Arr, e)
			}
			_, err := dec.Token()
			return v, err
		}
		v := Value{Kind: Object, Obj: map[string]Value{}}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return Value{}, err
			}
			k := kt.(string)
			e, err := parseValue(dec)
			if err != nil {
				return Value{}, err
			}
			if _, seen := v.Obj[k]; !seen {
				v.Keys = append(v.Keys, k)
			}
			v.Obj[k] = e
		}
		_, err := dec.Token()
		v.Keys = jsKeyOrder(v.Keys)
		return v, err
	}
	return Value{}, fmt.Errorf("读不懂的记号 %v", tok)
}

// 像数组下标的键（0 ～ 2^32-2 的十进制、没有前导零）按数字排在最前，别的按出现的先后（JS 的 OrdinaryOwnPropertyKeys）
func jsKeyOrder(keys []string) []string {
	var idx, rest []string
	for _, k := range keys {
		if isIndex(k) {
			idx = append(idx, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.SliceStable(idx, func(i, j int) bool {
		a, _ := strconv.ParseUint(idx[i], 10, 64)
		b, _ := strconv.ParseUint(idx[j], 10, 64)
		return a < b
	})
	return append(idx, rest...)
}

func isIndex(k string) bool {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return false
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < math.MaxUint32
}

// Stringify 像 JSON.stringify（不带缩进）
func Stringify(v Value) string {
	var b strings.Builder
	write(&b, v)
	return b.String()
}

func write(b *strings.Builder, v Value) {
	switch v.Kind {
	case Null:
		b.WriteString("null")
	case Bool:
		b.WriteString(strconv.FormatBool(v.Bool))
	case Number:
		b.WriteString(Number_(v.Num))
	case String:
		b.WriteString(Quote(v.Str))
	case Array:
		b.WriteByte('[')
		for i, e := range v.Arr {
			if i > 0 {
				b.WriteByte(',')
			}
			write(b, e)
		}
		b.WriteByte(']')
	case Object:
		b.WriteByte('{')
		for i, k := range v.Keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(Quote(k))
			b.WriteByte(':')
			write(b, v.Obj[k])
		}
		b.WriteByte('}')
	}
}

// Number_ 是 JS 把数字写成字的样子（Number.prototype.toString，JSON.stringify 用的就是它）；NaN、无穷写 null
func Number_(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0" // -0 也写 0
	}
	a := math.Abs(f)
	if a >= 1e-6 && a < 1e21 { // JS：10 的指数在 -6 和 21 之间写成小数（0.000001 写小数，1e-7 写成 1e-7）
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	// 科学记数：最短的有效数字，指数写成 e+21 / e-7（不补零）
	s := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(s, "e")
	n, _ := strconv.Atoi(exp)
	sign := "+"
	if n < 0 {
		sign, n = "-", -n
	}
	return mant + "e" + sign + strconv.Itoa(n)
}

// Quote 像 JSON.stringify 写一个字符串：转义 " \ 和控制字符（\b \f \n \r \t 用短写法，别的写 \u00xx），别的原样
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Canonical 把对象的键排好序，递归到每一层：键的先后不同、内容一样的两个值 Stringify 出来是同一串
// （daemon/util/tool-guard.ts 的 canonical）。排出来的串只拿来比是不是同一组参数、不给人看，
// 所以不用照 JS 的排法（UTF-16 码元、下标键排最前）：同一组键排出同一个先后就够了
func Canonical(v Value) Value {
	switch v.Kind {
	case Array:
		arr := make([]Value, len(v.Arr))
		for i, e := range v.Arr {
			arr[i] = Canonical(e)
		}
		v.Arr = arr
	case Object:
		keys := append([]string(nil), v.Keys...)
		sort.Strings(keys)
		obj := make(map[string]Value, len(v.Obj))
		for k, e := range v.Obj {
			obj[k] = Canonical(e)
		}
		v.Keys, v.Obj = keys, obj
	}
	return v
}

// Without 是去掉一个键的对象（JS 的 const { key: _, ...rest } = v）；不是对象时给一个空对象
func Without(v Value, key string) Value {
	out := Value{Kind: Object, Obj: map[string]Value{}}
	if v.Kind != Object {
		return out
	}
	for _, k := range v.Keys {
		if k != key {
			out.Keys = append(out.Keys, k)
			out.Obj[k] = v.Obj[k]
		}
	}
	return out
}

// StringifyIndent 像 JSON.stringify(v, null, indent)：每层缩进 indent 个空格；空数组写 []、空对象写 {}，键和值之间是 ": "
func StringifyIndent(v Value, indent int) string {
	var b strings.Builder
	writeIndent(&b, v, strings.Repeat(" ", indent), "")
	return b.String()
}

func writeIndent(b *strings.Builder, v Value, step, cur string) {
	switch v.Kind {
	case Array:
		if len(v.Arr) == 0 {
			b.WriteString("[]")
			return
		}
		inner := cur + step
		b.WriteString("[\n")
		for i, e := range v.Arr {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(inner)
			writeIndent(b, e, step, inner)
		}
		b.WriteString("\n" + cur + "]")
	case Object:
		if len(v.Keys) == 0 {
			b.WriteString("{}")
			return
		}
		inner := cur + step
		b.WriteString("{\n")
		for i, k := range v.Keys {
			if i > 0 {
				b.WriteString(",\n")
			}
			b.WriteString(inner + Quote(k) + ": ")
			writeIndent(b, v.Obj[k], step, inner)
		}
		b.WriteString("\n" + cur + "}")
	default:
		write(b, v)
	}
}
