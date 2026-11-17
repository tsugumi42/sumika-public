package desksettings

import "testing"

func TestPanelMaterialRoundTrip(t *testing.T) {
	s := New(newDir(t))
	if got, err := s.PanelMaterial(); err != nil || got != MaterialLiquid {
		t.Fatalf("默认应是液态玻璃：%q %v", got, err)
	}
	if err := s.SetAutoReceive(true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{MaterialBlur, MaterialSolid, MaterialLiquid} {
		if err := s.SetPanelMaterial(want); err != nil {
			t.Fatal(err)
		}
		if got, err := New(s.Dir()).PanelMaterial(); err != nil || got != want {
			t.Fatalf("往返：%q %v", got, err)
		}
	}
	if got, err := New(s.Dir()).AutoReceive(); err != nil || !got {
		t.Fatalf("别的设置丢了：%v %v", got, err)
	}
	if err := s.SetPanelMaterial("玻璃"); err == nil {
		t.Fatal("坏值被存下了")
	}
	if got, _ := s.PanelMaterial(); got != MaterialLiquid {
		t.Fatalf("坏值不该改动已存的：%q", got)
	}
}

// 文件里是坏值（不认识的名字、不是字符串）：回到默认的液态玻璃并报错，不让坏值把界面弄花。
func TestPanelMaterialBadFileFallsBack(t *testing.T) {
	s := New(newDir(t))
	for _, raw := range []string{`{"panelMaterial":"frosted"}`, `{"panelMaterial":3}`, `{"panelMaterial":null}`, `{"panelMaterial":""}`} {
		writeFile(t, s, raw)
		got, err := s.PanelMaterial()
		if got != MaterialLiquid || err == nil {
			t.Fatalf("%s：%q %v", raw, got, err)
		}
	}
}
