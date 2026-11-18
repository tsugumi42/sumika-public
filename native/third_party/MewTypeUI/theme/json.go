package theme

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// Import validates JSON and its references before returning an editable definition.
// No files are opened: the host owns file selection and storage.
func Import(data []byte) (Definition, error) {
	var d Definition
	if e := decode(data, reflect.ValueOf(&d).Elem(), ""); e != nil {
		return Definition{}, e
	}
	if _, e := Compile(d); e != nil {
		return Definition{}, e
	}
	return d, nil
}

// Export freezes every inherited token while retaining the author's color aliases.
func Export(d Definition) ([]byte, error) {
	t, e := Compile(d)
	if e != nil {
		return nil, e
	}
	return json.MarshalIndent(t.definition, "", "  ")
}

// Walking objects gives a full key path for unknown fields, type errors and explicit nulls.
func decode(data []byte, out reflect.Value, path string) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("%s: null is not a value", keyPath(path))
	}
	if out.Kind() == reflect.Pointer {
		out.Set(reflect.New(out.Type().Elem()))
		return decode(data, out.Elem(), path)
	}
	switch out.Kind() {
	case reflect.Struct, reflect.Map:
		var object map[string]json.RawMessage
		if e := json.Unmarshal(data, &object); e != nil {
			return fmt.Errorf("%s: %w", keyPath(path), e)
		}
		if out.Kind() == reflect.Map {
			out.Set(reflect.MakeMapWithSize(out.Type(), len(object)))
		}
		for k, v := range object {
			child := k
			if path != "" {
				child = path + "." + k
			}
			if out.Kind() == reflect.Map {
				item := reflect.New(out.Type().Elem()).Elem()
				if e := decode(v, item, child); e != nil {
					return e
				}
				out.SetMapIndex(reflect.ValueOf(k), item)
				continue
			}
			found := false
			for i := range out.NumField() {
				field := out.Type().Field(i)
				name := field.Tag.Get("json")
				if comma := bytes.IndexByte([]byte(name), ','); comma >= 0 {
					name = name[:comma]
				}
				if name == k {
					found = true
					if e := decode(v, out.Field(i), child); e != nil {
						return e
					}
					break
				}
			}
			if !found {
				return fmt.Errorf("%s: unknown key", child)
			}
		}
		return nil
	default:
		if e := json.Unmarshal(data, out.Addr().Interface()); e != nil {
			return fmt.Errorf("%s: %w", keyPath(path), e)
		}
		return nil
	}
}
func keyPath(path string) string {
	if path == "" {
		return "$"
	}
	return path
}
