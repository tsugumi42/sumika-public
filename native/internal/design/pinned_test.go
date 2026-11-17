package design_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/design"
	"sumika-native/internal/design/colorx"
)

// 钉值测试（规格-设计令牌 第十节第 3 步、V6）：第 2 步证明过令牌颜色和旧 chatui 逐字节一致，
// 第 3 步 chatui 改成薄壳以后再和它比就是自己比自己，所以把 12 种模式的全部颜色和默认角色色阶梯钉在
// testdata/colors.json 里。配色有意改动时设 DESIGN_UPDATE_VECTORS=1 重写，并在提交说明写原因。

func modes() []design.Settings {
	var out []design.Settings
	for _, dark := range []bool{false, true} {
		for _, n := range []string{design.NeutralCool, design.NeutralPlain, design.NeutralWarm} {
			for _, wp := range []bool{true, false} {
				s := design.DefaultSettings()
				s.Dark, s.Neutral, s.Wallpaper = dark, n, wp
				out = append(out, s)
			}
		}
	}
	return out
}

// pinnedChars 是 chatui.CharColors 的 8 个默认角色色（这里抄一份，钉值不随那边改动漂移）。
var pinnedChars = [][3]float64{
	{0.75, 0.1, 195}, {0.71, 0.11, 232}, {0.72, 0.12, 350}, {0.76, 0.11, 70},
	{0.73, 0.12, 150}, {0.7, 0.12, 290}, {0.74, 0.11, 25}, {0.75, 0.1, 115},
}

func rgba(c ui.Color) string { return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A) }

func colorFields(v any) map[string]string {
	out := map[string]string{}
	rv := reflect.ValueOf(v)
	for i := range rv.NumField() {
		if c, ok := rv.Field(i).Interface().(ui.Color); ok {
			out[rv.Type().Field(i).Name] = rgba(c)
		}
	}
	return out
}

func TestColorsPinned(t *testing.T) {
	got := map[string]any{}
	for _, s := range modes() {
		tk := design.Derive(s)
		var accents []map[string]string
		for _, c := range pinnedChars {
			accents = append(accents, colorFields(tk.AccentOf(colorx.New(c[0], c[1], c[2]))))
		}
		got[fmt.Sprintf("dark=%v/%s/wallpaper=%v", s.Dark, s.Neutral, s.Wallpaper)] = map[string]any{
			"colors": colorFields(tk.Color), "diff-tint": tk.Num.DiffTint, "accents": accents,
		}
	}
	data, err := json.MarshalIndent(got, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	const path = "testdata/colors.json"
	if os.Getenv("DESIGN_UPDATE_VECTORS") == "1" {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v（第一次跑设 DESIGN_UPDATE_VECTORS=1 生成）", err)
	}
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), data) {
		t.Fatal("颜色和 testdata/colors.json 不一致：配色有意改动再设 DESIGN_UPDATE_VECTORS=1 重写并在提交说明写原因")
	}
}
