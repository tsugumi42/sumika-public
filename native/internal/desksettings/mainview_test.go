package desksettings

import "testing"

func TestMainViewRoundTrip(t *testing.T) {
	s := New(newDir(t))
	if got, err := s.MainView(); err != nil || got != MainViewLegacy {
		t.Fatalf("default: %q %v", got, err)
	}
	if err := s.SetAutoReceive(true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{MainViewWorkbench, MainViewLegacy} {
		if err := s.SetMainView(want); err != nil {
			t.Fatal(err)
		}
		if got, err := New(s.Dir()).MainView(); err != nil || got != want {
			t.Fatalf("round trip: %q %v", got, err)
		}
	}
	if got, err := New(s.Dir()).AutoReceive(); err != nil || !got {
		t.Fatalf("other setting lost: %v %v", got, err)
	}
	if err := s.SetMainView("unknown"); err == nil {
		t.Fatal("invalid view accepted")
	}
	writeFile(t, s, `{"mainView":"unknown"}`)
	if got, err := s.MainView(); err == nil || got != MainViewLegacy {
		t.Fatalf("invalid value: %q %v", got, err)
	}
}
