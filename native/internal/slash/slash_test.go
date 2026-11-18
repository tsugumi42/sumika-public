package slash

import (
	"errors"
	"testing"
)

func testRegistry() *Registry {
	r := NewRegistry()
	r.Register(Command{Name: "goal", Subs: []Sub{
		{Name: "clear", Aliases: []string{"stop", "off"}, NoArg: true},
		{Name: "pause", NoArg: true},
	}})
	r.Register(Command{Name: "loop", Aliases: []string{"repeat"}})
	return r
}

func TestParse(t *testing.T) {
	r := testRegistry()
	cases := []struct {
		in, name, sub, arg string
	}{
		{"/goal 写个脚本", "goal", "", "写个脚本"},
		{"/goal", "goal", "", ""},
		{"  /GOAL  clear ", "goal", "clear", ""},
		{"/goal STOP", "goal", "clear", ""},
		{"/goal clear the cache dir", "goal", "", "clear the cache dir"},
		{"/goal 第一行\n第二行", "goal", "", "第一行\n第二行"},
		{"/repeat 5m x", "loop", "", "5m x"},
	}
	for _, c := range cases {
		inv, err := r.Parse(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if inv.Command.Name != c.name || inv.Sub != c.sub || inv.Arg != c.arg {
			t.Errorf("%q: 得到 %s/%q/%q", c.in, inv.Command.Name, inv.Sub, inv.Arg)
		}
	}
}

func TestParseNotAndUnknown(t *testing.T) {
	r := testRegistry()
	for _, in := range []string{"hello /goal x", "", "/", "/usr/bin/x 在哪", "/ goal", "//goal", "/1abc", "请 /goal"} {
		if _, err := r.Parse(in); !errors.Is(err, ErrNotCommand) {
			t.Errorf("%q 应当不是命令，得到 %v", in, err)
		}
	}
	_, err := r.Parse("/goalx do")
	var u *UnknownError
	if !errors.As(err, &u) || u.Name != "goalx" || err.Error() != "没有这个命令：/goalx" {
		t.Errorf("得到 %v", err)
	}
	if _, err := r.Parse("/nope"); !errors.As(err, &u) {
		t.Errorf("/nope 应为未知命令：%v", err)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("重复登记应 panic")
		}
	}()
	r := testRegistry()
	r.Register(Command{Name: "x", Aliases: []string{"goal"}})
}
