package desksettings

import "testing"

func TestShellPanesRoundTripAndDefaults(t *testing.T) {
	s := New(newDir(t))
	if got, err := s.ShellPanes(); err != nil || got != (ShellPanes{}) {
		t.Fatalf("默认应是展开、右栏关：%+v %v", got, err)
	}
	// 旧键里存着 compact=true：新壳不读它
	writeFile(t, s, `{"panes":{"compact":true,"thirdOpen":true,"listRatio":0.3}}`)
	if got, err := s.ShellPanes(); err != nil || got.Compact || got.SideOpen {
		t.Fatalf("旧 panes 键不该影响新壳：%+v %v", got, err)
	}
	want := ShellPanes{ListRatio: 0.2, Compact: true, SideWidth: 340, SideOpen: true}
	if err := s.SetShellPanes(want); err != nil {
		t.Fatal(err)
	}
	if got, err := New(s.Dir()).ShellPanes(); err != nil || got != want {
		t.Fatalf("往返：%+v %v", got, err)
	}
	if got, _ := New(s.Dir()).Panes(); !got.Compact {
		t.Fatalf("写新壳的键不该动旧键：%+v", got)
	}
	if err := s.SetShellPanes(ShellPanes{ListRatio: 3}); err == nil {
		t.Fatal("越界的值被存下了")
	}
}

func TestShellPanesBadFileFallsBack(t *testing.T) {
	s := New(newDir(t))
	for _, raw := range []string{`{"shellPanes":{"listRatio":2}}`, `{"shellPanes":{"sideWidth":10}}`, `{"shellPanes":{"compact":"是"}}`, `{"shellPanes":5}`, `{"shellPanes":{"listRatio":-1}}`} {
		writeFile(t, s, raw)
		got, err := s.ShellPanes()
		if err == nil || got != (ShellPanes{}) {
			t.Fatalf("%s：%+v %v", raw, got, err)
		}
	}
}
