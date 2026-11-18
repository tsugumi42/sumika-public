package emoji

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 切表情：VS16、零宽连接、肤色、旗帜、键帽各成一段；不带 VS16 的 ㊗、❤ 和单个地区指示符仍是字
func TestSplit(t *testing.T) {
	got := func(s string) string {
		var parts []string
		for _, p := range Split(s) {
			mark := "字"
			if p.Emoji {
				mark = "表"
			}
			parts = append(parts, mark+":"+p.Text)
		}
		return strings.Join(parts, "|")
	}
	for _, c := range []struct{ in, want string }{
		{"㊗️1000万", "表:㊗️|字:1000万"},
		{"㊗ 没有 VS16", "字:㊗ 没有 VS16"},
		{"❤️❤", "表:❤️|字:❤"},
		{"おめでとう✨", "字:おめでとう|表:✨"},
		{"👍🏻👨‍👩‍👧", "表:👍🏻|表:👨‍👩‍👧"},
		{"🇯🇵🇨", "表:🇯🇵|字:🇨"},
		{"#️⃣1⃣", "表:#️⃣|表:1⃣"},
		{"🏳️‍🌈", "表:🏳️‍🌈"},
		{"🏴\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F", "表:🏴\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F"},
		{"A1#", "字:A1#"},
		{"", ""},
	} {
		if g := got(c.in); g != c.want {
			t.Errorf("%q：%s，应为 %s", c.in, g, c.want)
		}
	}
	// 切开再拼回去是原文
	for _, s := range []string{"「なぜ？謎？！ANSWER」\n㊗️1000万回再生おめでとうございます✨", "😀😂🥹👍🏻👨‍👩‍👧 🇯🇵 ❤️ ✨🎉"} {
		var b strings.Builder
		for _, p := range Split(s) {
			b.WriteString(p.Text)
		}
		if b.String() != s {
			t.Fatalf("拼不回原文：%q", b.String())
		}
	}
}

// 文件名：不含零宽连接符的去掉 fe0f；含的先试全名再试去掉的（chatui 回应表情和推特卡片原来各写一份，这里是合并后的并集）
func TestNames(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"👍", "1f44d.svg"},
		{"㊗️", "3297.svg"},
		{"❤️", "2764.svg"},
		{"❤", "2764.svg"},
		{"🇯🇵", "1f1ef-1f1f5.svg"},
		{"#️⃣", "23-20e3.svg"},
		{"👨‍👩‍👧", "1f468-200d-1f469-200d-1f467.svg"},
		{"🏳️‍🌈", "1f3f3-fe0f-200d-1f308.svg,1f3f3-200d-1f308.svg"},
	} {
		if g := strings.Join(Names(c.in), ","); g != c.want {
			t.Errorf("%q：%s，应为 %s", c.in, g, c.want)
		}
	}
}

const tinySVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 36 36"><circle cx="18" cy="18" r="16" fill="#f00"/></svg>`

// 查图：先全名后去掉 fe0f；没有的、坏的、空目录、nil 都是 nil；同一个序列同一个指针；For 同目录共享
func TestSetSVG(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("1f44d.svg", tinySVG)
	write("2764.svg", tinySVG)
	write("1f3f3-200d-1f308.svg", tinySVG) // 只有去掉 fe0f 的版本
	write("1f600.svg", "不是 SVG")
	s := NewSet(dir)
	for _, seq := range []string{"👍", "❤️", "❤", "🏳️‍🌈"} {
		if s.SVG(seq) == nil {
			t.Errorf("%q 应当取到图", seq)
		}
	}
	if s.SVG("👍") != s.SVG("👍") {
		t.Error("同一个序列应当缓存同一个指针")
	}
	for _, seq := range []string{"🎉", "😀", ""} {
		if s.SVG(seq) != nil {
			t.Errorf("%q 不应当取到图（没有这个文件、文件坏了或空串）", seq)
		}
	}
	if NewSet("").SVG("👍") != nil || NewSet(filepath.Join(dir, "没有")).SVG("👍") != nil {
		t.Error("空目录、不存在的目录应当什么都取不到")
	}
	if m, ok := s.Markup("👍"); !ok || !strings.HasPrefix(m, "<svg") {
		t.Errorf("Markup 应当给出文件原文：%q %v", m, ok)
	}
	if _, ok := s.Markup("🎉"); ok {
		t.Error("没有这张图不该有 Markup")
	}
	var nilSet *Set
	if _, ok := nilSet.Markup("👍"); ok {
		t.Error("nil 集合不该有 Markup")
	}
	if nilSet.SVG("👍") != nil || nilSet.Dir() != "" {
		t.Error("nil 集合应当当成没有目录")
	}
	if For(dir) != For(dir) || For(dir) == For(t.TempDir()) {
		t.Error("For 应当同目录共享、不同目录各一个")
	}
}
