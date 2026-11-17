package settingsreg

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"sumika-native/internal/config"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/persona"
)

func field(v reflect.Value, name string) reflect.Value {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v = reflect.Zero(v.Type().Elem())
		} else {
			v = v.Elem()
		}
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")
		if tag[0] == "-" {
			continue
		}
		if len(tag) > 1 && tag[1] == "inline" {
			if x := field(v.Field(i), name); x.IsValid() {
				return x
			}
			continue
		}
		if tag[0] == name {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}
func schemaValue(v reflect.Value, path []string) (reflect.Type, any) {
	for _, part := range path {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v = reflect.Zero(v.Type().Elem())
			} else {
				v = v.Elem()
			}
		}
		if part == "*" {
			switch v.Kind() {
			case reflect.Map, reflect.Slice, reflect.Array:
				v = reflect.Zero(v.Type().Elem())
			default:
				return nil, nil
			}
			continue
		}
		v = field(v, part)
		if !v.IsValid() {
			return nil, nil
		}
	}
	return v.Type(), safeValue(v, strings.Join(path, "."))
}
func typeName(t reflect.Type) typeID {
	if t == nil {
		return tyUnknown
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return tySwitch
	case reflect.String:
		return tyText
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint64, reflect.Float32, reflect.Float64:
		return tyNumber
	case reflect.Slice, reflect.Array:
		return tyList
	case reflect.Map:
		return tyTable
	case reflect.Struct:
		return tyObject
	}
	return tyText
}
func secret(k string) bool {
	return strings.HasSuffix(k, ".token") || strings.HasPrefix(k, "web.") && strings.HasSuffix(k, "eyRef")
}
func cleanURL(s string) string {
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "地址无效"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	return u.String()
}
func safeValue(v reflect.Value, k string) any {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		return safeValue(v.Elem(), k)
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return safeValue(v.Elem(), k)
	}
	if secret(k) {
		if v.Kind() == reflect.String && v.String() != "" {
			return "已存"
		}
		return "没存"
	}
	if k == "magpie.baseUrl" || k == "linkReader.proxy" || strings.HasPrefix(k, "qq.accounts.") && strings.HasSuffix(k, ".url") {
		if v.Kind() == reflect.String {
			return cleanURL(v.String())
		}
	}
	switch v.Kind() {
	case reflect.Struct:
		out := map[string]any{}
		t := v.Type()
		for i := range v.NumField() {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("yaml"), ",")
			if tag[0] == "" || tag[0] == "-" {
				continue
			}
			out[tag[0]] = safeValue(v.Field(i), k+"."+tag[0])
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		out := map[string]any{}
		it := v.MapRange()
		for it.Next() {
			key := fmt.Sprint(it.Key().Interface())
			out[key] = safeValue(it.Value(), k+"."+key)
		}
		return out
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = safeValue(v.Index(i), k+"."+strconv.Itoa(i))
		}
		return out
	default:
		if v.CanInterface() {
			return v.Interface()
		}
		return nil
	}
}

type match struct {
	key   string
	value reflect.Value
}

func expand(v reflect.Value, parts []string, prefix string) []match {
	if len(parts) == 0 {
		return []match{{prefix, v}}
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	p := parts[0]
	if p == "*" {
		var out []match
		switch v.Kind() {
		case reflect.Map:
			keys := v.MapKeys()
			sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface()) })
			for _, key := range keys {
				s := fmt.Sprint(key.Interface())
				out = append(out, expand(v.MapIndex(key), parts[1:], join(prefix, s))...)
			}
		case reflect.Slice, reflect.Array:
			for i := range v.Len() {
				out = append(out, expand(v.Index(i), parts[1:], join(prefix, strconv.Itoa(i)))...)
			}
		}
		return out
	}
	x := field(v, p)
	if !x.IsValid() {
		return nil
	}
	return expand(x, parts[1:], join(prefix, p))
}
func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}
func source(c config.Config, key string) string {
	if c.Present[key] {
		return "yml"
	}
	return "默认"
}
func Read(c config.Config, desktop *desksettings.Store, charactersDir string) ([]Value, error) {
	entries := Entries()
	out := make([]Value, 0, len(entries))
	var document map[string]bool
	var auto bool
	var appearance any = desksettings.DefaultAppearance()
	var panes any = desksettings.DefaultPanes()
	if desktop != nil {
		raw, _, err := desktop.Document()
		if err != nil {
			return nil, err
		}
		document = map[string]bool{}
		for k := range raw {
			document[k] = true
		}
		auto, err = desktop.AutoReceive()
		if err != nil {
			return nil, err
		}
		appearance, err = desktop.Appearance()
		if err != nil {
			return nil, err
		}
		panes, err = desktop.Panes()
		if err != nil {
			return nil, err
		}
	}
	for _, e := range entries {
		if e.Key == "trace.debug" {
			out = append(out, Value{Entry: e, Source: source(c, e.Key)})
			continue
		}
		switch e.Storage {
		case storageLabel[stPlanned], storageLabel[stHost]:
			out = append(out, Value{Entry: e, Source: "默认"})
			continue
		case storageLabel[stCard]:
			out = append(out, Value{Entry: e, Source: "角色卡"})
			continue
		case storageLabel[stDesktop]:
			v := any(auto)
			key := "autoReceive"
			if e.Key == "desktop.appearance" {
				v = appearance
				key = "appearance"
			}
			if e.Key == "desktop.panes" {
				v = panes
				key = "panes"
			}
			s := "默认"
			if document[key] {
				s = "桌面设置"
			}
			out = append(out, Value{Entry: e, Value: v, Source: s})
			continue
		}
		if strings.Contains(e.Key, "*") {
			out = append(out, Value{Entry: e, Value: e.Default, Source: "默认"})
		}
		for _, m := range expand(reflect.ValueOf(c), strings.Split(e.Key, "."), "") {
			item := e
			item.Key = m.key
			out = append(out, Value{Entry: item, Value: safeValue(m.value, m.key), Source: source(c, m.key)})
		}
	}
	if charactersDir != "" {
		dirs, err := os.ReadDir(charactersDir)
		if errors.Is(err, os.ErrNotExist) {
			// Windows 读一个普通文件为目录时也可能返回路径不存在。
			if _, statErr := os.Stat(charactersDir); errors.Is(statErr, os.ErrNotExist) {
				return out, nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("读取角色目录：%w", err)
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			id := d.Name()
			name, color, _, err := persona.Display(charactersDir, id, "")
			if errors.Is(err, os.ErrNotExist) {
				continue // 没有 character.yml 的不是角色（_shared 这种共用目录），不能让它把整张设置表读失败
			}
			if err != nil {
				return nil, err
			}
			md, err := os.ReadFile(filepath.Join(persona.CardDir(charactersDir, id), "character.md"))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("读取角色 %s 人设卡：%w", id, err)
			}
			for _, e := range entries {
				if !strings.HasPrefix(e.Key, "card.*.") {
					continue
				}
				item := e
				item.Key = strings.Replace(e.Key, "*", id, 1)
				var v any
				switch e.Key {
				case "card.*.name":
					v = name
				case "card.*.color":
					v = color
				case "card.*.persona":
					v = string(md)
				}
				out = append(out, Value{Entry: item, Value: v, Source: "角色卡"})
			}
		}
	}
	return out, nil
}
