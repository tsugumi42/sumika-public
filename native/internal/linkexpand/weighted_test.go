package linkexpand

import (
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
)

// 推特官方加权长度（twitter-text 配置 v3）：U+0000–U+10FF、U+2000–U+200D、U+2010–U+201F、U+2032–U+2037 算 1，其余算 2；
// 表情序列算 2；http(s) 链接固定 23。
func TestWeightedLength(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     int
	}{
		{"空", "", 0},
		{"纯英文", "Hello rockets", 13},
		{"纯中文", "你好火箭和月亮", 14},
		{"日文假名和汉字", "ありがとう、世界", 16},
		{"片假名", "ロケット", 8},
		{"中英混排", "Hello 世界", 6 + 4},
		{"拉丁扩展和希腊字母算 1", "café Ωμέγα", 10},
		{"西里尔字母算 1", "Привет", 6},
		{"韩文算 2", "안녕", 4},
		{"全角标点算 2", "你好，。！", 10},
		{"弯引号和破折号算 1", "“quote” — it’s", 14}, // U+201C/201D/2019 在 0x2010–0x201F，U+2014 也在
		{"省略号 U+2026 算 2", "wait…", 4 + 2},
		{"单个表情算 2", "😀", 2},
		{"带肤色的表情算 2", "👍🏽", 2},
		{"零宽连接的家庭表情算 2", "👨‍👩‍👧", 2},
		{"国旗算 2", "🇯🇵", 2},
		{"表情夹在英文里", "ok 😀 ok", 3 + 2 + 3},
		{"链接固定 23", "https://example.invalid/a/very/long/path/that/goes/on/and/on?x=1&y=2", 23},
		{"链接前后的字", "看 https://example.invalid/abc 这个", 2 + 1 + 23 + 1 + 4},
		{"链接后紧跟句号不算进链接", "见https://example.invalid/abc。", 2 + 23 + 2},
		{"两条链接", "https://a.invalid/1 https://b.invalid/2", 23 + 1 + 23},
		{"换行算 1", "a\nb", 3},
	} {
		if got := WeightedLength(c.in); got != c.want {
			t.Errorf("%s：%q → %d，要 %d", c.name, c.in, got, c.want)
		}
	}
}

// 区间边界：每个区间的首尾码点和紧挨着的外面一个码点。
func TestWeightedLengthRangeEdges(t *testing.T) {
	for r, want := range map[rune]int{
		0x0000: 1, 0x10FF: 1, 0x1100: 2, // 谚文字母 U+1100 起是 2
		0x1FFF: 2, 0x2000: 1, 0x200D: 1, 0x200E: 2,
		0x200F: 2, 0x2010: 1, 0x201F: 1, 0x2020: 2,
		0x2031: 2, 0x2032: 1, 0x2037: 1, 0x2038: 2,
	} {
		if got := runesWeight(string(r)); got != want {
			t.Errorf("U+%04X：%d，要 %d", r, got, want)
		}
	}
}

// 帖子长短判断（x、telegram 的 forwardOver 默认 600，按加权长度比）：
// 400 个汉字（加权 800）是长，400 个英文字母（400）是短，700 个英文字母是长，300 个汉字（600）刚好是短；B 站动态仍按字数。
func TestAutoForwardUsesWeightedLength(t *testing.T) {
	shape := func(key string, text string) string {
		var r linkreader.Result
		if key == "telegram" {
			r = tgResult(text, 0)
		} else {
			r = xImages(0)
			r.Text = av(text)
			r.Blocks = []linkreader.Block{{Type: "text", Text: text}}
		}
		d := build(r, Options{})
		return strings.Join(kinds(d), ",")
	}
	for _, key := range []string{"x", "telegram"} {
		for _, c := range []struct {
			name, text, want string
		}{
			{"400 个汉字是长", strings.Repeat("字", 400), "forward"},
			{"400 个英文字母是短", strings.Repeat("a", 400), "message"},
			{"600 个英文字母刚好是短", strings.Repeat("a", 600), "message"},
			{"601 个英文字母是长", strings.Repeat("a", 601), "forward"},
			{"700 个英文字母是长", strings.Repeat("a", 700), "forward"},
			{"300 个汉字刚好是短", strings.Repeat("字", 300), "message"},
			{"301 个汉字是长", strings.Repeat("字", 301), "forward"},
			{"一条长链接只算 23", "https://example.invalid/" + strings.Repeat("p/", 400), "message"},
		} {
			if got := shape(key, c.text); got != c.want {
				t.Errorf("%s %s：%s，要 %s", key, c.name, got, c.want)
			}
		}
	}
	// B 站动态不变：按字数，默认 100 字
	dyn := caseNamed(t, "bili-dynamic")
	dyn.Text = av(strings.Repeat("字", 60)) // 60 个汉字：按字数是 60 ≤ 100 短；若误用加权长度是 120 > 100 就成了长
	dyn.Blocks = []linkreader.Block{{Type: "text", Text: dyn.Text.Value}}
	if got := strings.Join(kinds(build(dyn, Options{})), ","); got != "message" {
		t.Errorf("B 站动态 60 个汉字应仍是短：%s", got)
	}
	dyn.Text = av(strings.Repeat("字", 101))
	dyn.Blocks = []linkreader.Block{{Type: "text", Text: dyn.Text.Value}}
	if got := strings.Join(kinds(build(dyn, Options{})), ","); got != "forward" {
		t.Errorf("B 站动态 101 个汉字应是长：%s", got)
	}
}
