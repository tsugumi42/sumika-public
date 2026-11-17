package ctlproto

import (
	"strings"
	"testing"
)

func TestValidChatText(t *testing.T) {
	for s, want := range map[string]bool{
		"你好":                                    true,
		"多行\n文字\t和制表":                           true,
		"回车\r\n也行":                              true,
		"":                                      false,
		" \n\t ":                                false,
		"带ESC\x1b[2J":                           false,
		"带NUL\x00":                              false,
		"带DEL\x7f":                              false,
		"带C1\u0085":                             false,
		"方向覆盖‮":                                 false,
		"坏字节\xff":                               false,
		strings.Repeat("字", MaxChatText):        true,
		strings.Repeat("字", MaxChatText+1):      false,
		strings.Repeat("a", MaxChatText*3) + "": false, // 按字数算，不按字节
	} {
		if got := ValidChatText(s); got != want {
			t.Errorf("%q：得到 %v，应是 %v", Truncate(s, 20), got, want)
		}
	}
}

func TestValidCharacter(t *testing.T) {
	for s, want := range map[string]bool{
		"example": true, "示例": true, "a-b_c.d": true, strings.Repeat("a", 128): true,
		"": false, " ": false, "a b": false, "a:b": false, "a/b": false, `a\b`: false, "a\x00": false, strings.Repeat("a", 129): false,
	} {
		if got := ValidCharacter(s); got != want {
			t.Errorf("%q：得到 %v，应是 %v", s, got, want)
		}
	}
}

// CleanText：保留换行和制表符，其余控制字符换成 ?，清两遍等于清一遍
func TestCleanText(t *testing.T) {
	in := "行一\r\n行二\r行三 行四\x1b[2J\t制表‮\x00尾\xff"
	got := CleanText(in)
	if got != "行一\n行二\n行三\n行四?[2J\t制表??尾�" {
		t.Fatalf("得到 %q", got)
	}
	if again := CleanText(got); again != got {
		t.Fatalf("清两遍不等于清一遍：%q → %q", got, again)
	}
}
