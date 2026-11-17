package icons

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestTableSortedAndComplete(t *testing.T) {
	names := Names()
	if !sort.StringsAreSorted(names) {
		t.Fatal("table 没按名字排序：二分查找会找不到")
	}
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			t.Fatalf("重名：%s", names[i])
		}
	}
	// MingCute 2.9.72 共 1662 个图标，其中 8 个含渐变、内嵌位图没收；换版本后这个数可以变，但不能少得离谱
	if Count() < 1600 {
		t.Fatalf("图标只有 %d 个，生成可能出了问题", Count())
	}
	if Version() == "" {
		t.Fatal("没记来源版本")
	}
}

func TestLineAndFillBothThere(t *testing.T) {
	for _, name := range []string{"add", "close", "check", "search", "chat_3", "heart", "repeat", "chart_bar", "settings_3", "home_1", "delete_2", "arrow_down"} {
		for _, fill := range []bool{false, true} {
			if _, err := Body(name, fill); err != nil {
				t.Errorf("%s fill=%v：%v", name, fill, err)
			}
		}
		l, _ := Body(name, false)
		f, _ := Body(name, true)
		if l == f {
			t.Errorf("%s 的线性和实心是同一份", name)
		}
	}
}

func TestMarkupShape(t *testing.T) {
	m, err := Markup("add", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(m, `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24"><path fill="currentColor"`) || !strings.HasSuffix(m, "</svg>") {
		t.Fatalf("形状不对：%s", m)
	}
	// 颜色必须是 currentColor：写死了颜色的图标没法跟着 TextColor 走
	for _, e := range table {
		for _, b := range []string{e.line, e.fill} {
			if b != "" && (strings.Contains(b, `fill="#`) || strings.Contains(b, "09244B")) {
				t.Fatalf("%s 里还有写死的颜色", e.name)
			}
		}
	}
}

func TestSuffixNames(t *testing.T) {
	// MingCute 文件名带 _line、_fill，后缀说了算
	a, err := Body("add_fill", false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Body("add", true)
	if a != b {
		t.Fatal("add_fill 应当等于 add 的实心版")
	}
	c, _ := Body("add_line", true)
	d, _ := Body("add", false)
	if c != d {
		t.Fatal("add_line 应当等于 add 的线性版")
	}
}

func TestUnknownName(t *testing.T) {
	for _, name := range []string{"", "no_such_icon", "add_", "ADD", "../add"} {
		_, err := Body(name, false)
		if !errors.Is(err, ErrUnknown) {
			t.Errorf("%q：应当报 ErrUnknown，得到 %v", name, err)
		}
		if _, err := SVG(name, true); !errors.Is(err, ErrUnknown) {
			t.Errorf("%q：SVG 应当报 ErrUnknown，得到 %v", name, err)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Must 取不到应当 panic")
		}
	}()
	Must("no_such_icon", false)
}

func TestAllParse(t *testing.T) {
	for _, name := range Names() {
		for _, fill := range []bool{false, true} {
			if _, err := Body(name, fill); err != nil {
				continue // 某种样式没有
			}
			if _, err := SVG(name, fill); err != nil {
				t.Errorf("%s fill=%v：%v", name, fill, err)
			}
		}
	}
}

func TestSVGCached(t *testing.T) {
	a := Must("search", false)
	if a != Must("search", false) {
		t.Fatal("同一个图标应当返回同一个指针")
	}
	if a == Must("search", true) {
		t.Fatal("线性和实心不能共用缓存")
	}
}
