package desksettings

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPanesRoundTrip(t *testing.T) {
	s := New(newDir(t))
	want := Panes{ListRatio: 0.32, Compact: true, ThirdWidth: 350, ThirdOpen: true}
	if err := s.SetPanes(want); err != nil {
		t.Fatal(err)
	}
	if got, err := New(s.dir).Panes(); err != nil || got != want {
		t.Fatalf("重启偏好=%+v err=%v", got, err)
	}
	b, err := os.ReadFile(s.File())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "listWidth") || strings.Contains(string(b), "sideWidth") {
		t.Fatalf("仍保存旧键: %s", b)
	}
	if len(leftovers(t, s)) != 0 {
		t.Fatal("留下临时文件")
	}
}

func TestPanesReadBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		want       Panes
		bad        bool
	}{
		{"缺字段", `{}`, Panes{}, false},
		{"空对象", `{"panes":{}}`, Panes{}, false},
		{"坏文件", `{坏了`, Panes{}, true},
		{"数组", `[1,2]`, Panes{}, true},
		{"null", `{"panes":null}`, Panes{}, true},
		{"非对象", `{"panes":[]}`, Panes{}, true},
		{"类型不对保留其他项", `{"panes":{"listRatio":"宽","compact":true,"thirdWidth":350}}`, Panes{Compact: true, ThirdWidth: 350}, true},
		{"null字段", `{"panes":{"listRatio":null,"thirdOpen":null}}`, Panes{}, true},
		{"越界", `{"panes":{"listRatio":2,"thirdWidth":999,"thirdOpen":true}}`, Panes{ListRatio: 1, ThirdWidth: 392, ThirdOpen: true}, false},
		{"负数", `{"panes":{"listRatio":-1,"thirdWidth":-5}}`, Panes{ThirdWidth: 292}, false},
		{"旧默认不覆盖", `{"panes":{"listWidth":280,"sideWidth":320}}`, Panes{}, false},
		{"旧自选保留", `{"panes":{"listWidth":300,"listIcons":true,"sideWidth":350,"sideOpen":true}}`, Panes{ListRatio: 300.0 / 1568, Compact: true, ThirdWidth: 350, ThirdOpen: true}, false},
		{"新键优先", `{"panes":{"listRatio":0.4,"listWidth":300,"compact":false,"listIcons":true,"thirdWidth":360,"sideWidth":350,"thirdOpen":false,"sideOpen":true}}`, Panes{ListRatio: 0.4, ThirdWidth: 360}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(newDir(t))
			writeFile(t, s, tc.file)
			got, err := s.Panes()
			if got != tc.want || (err != nil) != tc.bad {
				t.Fatalf("got=%+v err=%v want=%+v bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}

func TestPanesWriteBoundary(t *testing.T) {
	s := New(newDir(t))
	writeFile(t, s, `{"autoReceive":true,"future":{"a":1}}`)
	for _, tc := range []struct{ in, want Panes }{
		{Panes{ListRatio: 3, ThirdWidth: 100}, Panes{ListRatio: 1, ThirdWidth: 292}},
		{Panes{ListRatio: float32(math.NaN()), ThirdWidth: float32(math.Inf(1))}, Panes{}},
	} {
		if err := s.SetPanes(tc.in); err != nil {
			t.Fatal(err)
		}
		got, err := New(s.dir).Panes()
		if err != nil || got != tc.want {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	}
	if err := s.SetAutoReceive(false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.File())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"future"`) {
		t.Fatal("丢了其他键")
	}
}

func TestSetPanesOnCorruptFileKeepsHandleKey(t *testing.T) {
	s := New(newDir(t))
	key, err := s.HandleKey()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s, `{坏了`)
	want := Panes{ListRatio: 0.3, ThirdWidth: 300}
	if err := s.SetPanes(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Panes()
	if err != nil || got != want {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	again, err := New(s.dir).HandleKey()
	if err != nil || string(again) != string(key) {
		t.Fatal("密钥改变")
	}
}

func TestSetPanesNeedsAnInitialisedDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "没初始化")
	if err := New(dir).SetPanes(DefaultPanes()); err == nil {
		t.Fatal("未初始化目录写入成功")
	}
	if _, err := os.Stat(filepath.Join(dir, "go")); err == nil {
		t.Fatal("创建了目录")
	}
}
