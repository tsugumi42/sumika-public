package textclean

// 性质测试（LESSONS：文本清理“清两遍 = 清一遍”）：不列举乱码样例，让生成器撞；-short 只跑下面的种子。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzCleanIdempotent 覆盖所有对外的清理函数：DropTailJunk（删 / 不删外文两种）、DropStrayBrackets、Trim、TrimEnd。
// 性质：清理两遍和清理一遍结果相同；合法 UTF-8 进来，清理后仍是合法 UTF-8。
func FuzzCleanIdempotent(f *testing.F) {
	seeds := []string{
		"",
		"主人晚安～",
		"这是一段很长的中文😀😀😀，末尾有乱码：\ue000\ue001",
		"mixed 中英日 English 日本語 😀\u200b\u200d",
		"\x00\x01\x02 control \n\t end \u2028",
		strings.Repeat("很长的一段话😀", 300) + "】【。",
		"“还没说完，她说“好”",
		"前面有【括号】后面】【。",
	}
	for _, s := range seeds {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, s string, dropForeign bool) {
		validIn := utf8.ValidString(s)
		o := Options{DropForeign: dropForeign}

		j1 := DropTailJunk(s, o)
		j2 := DropTailJunk(j1.Text, o)
		if j2.Text != j1.Text {
			t.Fatalf("DropTailJunk 两遍和一遍不一样：\n输入 %q\n一遍 %q\n两遍 %q", s, j1.Text, j2.Text)
		}
		if validIn && !utf8.ValidString(j1.Text) {
			t.Fatalf("DropTailJunk 把合法 UTF-8 清成了非法：%q → %q", s, j1.Text)
		}

		b1 := DropStrayBrackets(s)
		b2 := DropStrayBrackets(b1.Text)
		if b2.Text != b1.Text {
			t.Fatalf("DropStrayBrackets 两遍和一遍不一样：\n输入 %q\n一遍 %q\n两遍 %q", s, b1.Text, b2.Text)
		}
		if validIn && !utf8.ValidString(b1.Text) {
			t.Fatalf("DropStrayBrackets 把合法 UTF-8 清成了非法：%q → %q", s, b1.Text)
		}

		if t1 := Trim(s); Trim(t1) != t1 {
			t.Fatalf("Trim 两遍和一遍不一样：%q → %q → %q", s, t1, Trim(t1))
		}
		if e1 := TrimEnd(s); TrimEnd(e1) != e1 {
			t.Fatalf("TrimEnd 两遍和一遍不一样：%q → %q → %q", s, e1, TrimEnd(e1))
		}
	})
}
