package turn

// 插件清单的 Settings 结构体转成设置字段（规格-插件两层 第二节第 1、4 条）：字段名取 yaml 标签（没有就用小写的字段名），
// 标题、说明、范围、枚举来自 title、desc、min、max、enum 标签（enum 用 | 分隔）。设置页照这份画，不另写标题表。
// 只认 bool、整数、小数、字符串（字符串带 enum 就是枚举）；别的类型报错，插件不启用。

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// SettingField 是设置页要的一行
type SettingField struct {
	Key, Title, Desc string
	Kind             string // bool / int / float / string / enum
	Min, Max         *float64
	Options          []string
	Default          any
}

// SettingFields 读出一个设置结构体（或指向它的指针）的字段；零值的结构体也行，取的是字段标签和当前的值作默认
func SettingFields(settings any) ([]SettingField, error) {
	if settings == nil {
		return nil, nil
	}
	v := reflect.ValueOf(settings)
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("设置要是一个结构体，实际是 %s", v.Kind())
	}
	t := v.Type()
	var out []SettingField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		key := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if key == "-" {
			continue
		}
		if key == "" {
			key = strings.ToLower(f.Name[:1]) + f.Name[1:]
		}
		if key == "enabled" {
			// plugins.<id>.enabled 是开关（config 层消费掉了），设置字段不能再叫这个名字，否则配置写的值到不了这里
			return nil, fmt.Errorf("设置名 enabled 保留给插件开关，换一个名字")
		}
		sf := SettingField{Key: key, Title: f.Tag.Get("title"), Desc: f.Tag.Get("desc"), Default: v.Field(i).Interface()}
		if sf.Title == "" {
			sf.Title = key
		}
		switch f.Type.Kind() {
		case reflect.Bool:
			sf.Kind = "bool"
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			sf.Kind = "int"
		case reflect.Float32, reflect.Float64:
			sf.Kind = "float"
		case reflect.String:
			sf.Kind = "string"
			if e := f.Tag.Get("enum"); e != "" {
				sf.Kind = "enum"
				sf.Options = strings.Split(e, "|")
			}
		default:
			return nil, fmt.Errorf("设置 %s 的类型 %s 不认识", key, f.Type)
		}
		if s := f.Tag.Get("min"); s != "" {
			n, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("设置 %s 的 min 不是数：%w", key, err)
			}
			sf.Min = &n
		}
		if s := f.Tag.Get("max"); s != "" {
			n, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("设置 %s 的 max 不是数：%w", key, err)
			}
			sf.Max = &n
		}
		// 范围只用在数字上；枚举的默认值要在选项里；最小不能大于最大（静默忽略会让设置页的范围和实际不一样）
		if (sf.Min != nil || sf.Max != nil) && sf.Kind != "int" && sf.Kind != "float" {
			return nil, fmt.Errorf("设置 %s 是 %s，不能用 min、max", key, sf.Kind)
		}
		if sf.Min != nil && sf.Max != nil && *sf.Min > *sf.Max {
			return nil, fmt.Errorf("设置 %s 的 min 大于 max", key)
		}
		if sf.Kind == "enum" {
			if d, _ := sf.Default.(string); d != "" && !slices.Contains(sf.Options, d) {
				return nil, fmt.Errorf("设置 %s 的默认值 %q 不在枚举选项里", key, d)
			}
		} else if f.Tag.Get("enum") != "" {
			return nil, fmt.Errorf("设置 %s 的 enum 只能用在字符串上", key)
		}
		out = append(out, sf)
	}
	return out, nil
}
