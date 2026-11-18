package desksettings

import "testing"

func TestLookRoundTripAndDefaults(t *testing.T) {
	s := New(newDir(t))
	if got, err := s.Look(); err != nil || got != DefaultLook() {
		t.Fatalf("默认：%+v %v", got, err)
	}
	want := Look{Radius: 18, Density: 9.5, Neutral: 2, Wallpaper: false, Ligatures: false, GroupWidth: 80, PrivateWidth: 65}
	if err := s.SetLook(want); err != nil {
		t.Fatal(err)
	}
	if got, err := New(s.Dir()).Look(); err != nil || got != want {
		t.Fatalf("往返：%+v %v", got, err)
	}
	if err := s.SetLook(Look{Radius: 12, Density: 8, GroupWidth: 30, PrivateWidth: 0}); err == nil {
		t.Fatal("聊天区宽度 30 越界却被存下了")
	}
	if err := s.SetLook(Look{Radius: 99, Density: 8, GroupWidth: 100, PrivateWidth: 0}); err == nil {
		t.Fatal("越界的值被存下了")
	}
}

// 文件里缺字段按默认补；越界、类型错、不是对象：整份回默认并报错，不让坏值把界面弄花。
func TestLookBadFileFallsBack(t *testing.T) {
	s := New(newDir(t))
	writeFile(t, s, `{"look":{"radius":16}}`)
	if got, err := s.Look(); err != nil || got.Radius != 16 || got.Density != 8 || !got.Wallpaper || got.GroupWidth != 100 || got.PrivateWidth != 0 {
		t.Fatalf("缺字段：%+v %v", got, err)
	}
	for _, raw := range []string{`{"look":{"radius":99}}`, `{"look":{"density":0}}`, `{"look":{"neutral":7}}`, `{"look":{"radius":"大"}}`, `{"look":3}`, `{"look":{"radius":-1}}`, `{"look":{"groupWidth":10}}`, `{"look":{"privateWidth":150}}`} {
		writeFile(t, s, raw)
		got, err := s.Look()
		if err == nil || got != DefaultLook() {
			t.Fatalf("%s：%+v %v", raw, got, err)
		}
	}
}
