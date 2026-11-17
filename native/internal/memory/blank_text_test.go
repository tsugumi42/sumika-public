package memory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 事实正文判空要和嵌入服务是同一套空白（internal/pyspace：Go 的空白再加 U+001C～U+001F）。
// 只用 strings.TrimSpace 判空时，"\x1f" 这类字符算内容，写得进库；它到了嵌入那一侧是空白，整批 400，补不出向量
var blankTextCases = []struct {
	name string
	text string
	want string // 去掉首尾空白后该存下的正文；空串 = 该当成空的拒掉
}{
	{"只有 U+001F", "\x1f", ""},
	{"只有 U+001C", "\x1c", ""},
	{"U+001D 加 U+001E", "\x1d\x1e", ""},
	{"控制字符混在普通空白里", " \x1f\t\x1c\n", ""},
	{"各种 Unicode 空白", " \u0085   　", ""},
	{"U+001F 夹着 Unicode 空白", "　\x1f ", ""},
	{"首尾的 U+001F 要去掉", "\x1f主人在上班\x1f", "主人在上班"},
	{"首尾混着各种空白", "\x1c 　主人养了猫\x1f\n", "主人养了猫"},
	{"中间的 U+001F 不动", "主人\x1f在上班", "主人\x1f在上班"},
	{"零宽字符不是空白（Python 的 strip 也不剥）", "​", "​"},
	{"U+0000 不是空白", "\x00", "\x00"},
	{"U+007F 不是空白", "\x7f", "\x7f"},
	{"普通文字", "主人喜欢猫", "主人喜欢猫"},
	{"普通文字带普通空白", "  主人喜欢猫\n", "主人喜欢猫"},
}

func TestRememberTextBlankMatchesEmbeddingSide(t *testing.T) {
	t.Parallel()
	for _, c := range blankTextCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m, _ := openTools(t, newSQLEvents(t))
			res := remembered(t, m, ownerHere, RememberArgs{Kind: "fact", Text: c.text})
			if c.want == "" {
				if !res.IsError || res.Text != "要记的内容是空的，这次没有记：用第三人称一句话写清楚。" {
					t.Fatalf("该当成空的拒掉：%+v", res)
				}
				if w := writes(t, m); w != [4]int{} {
					t.Fatalf("拒掉的什么都不该写：%v", w)
				}
				return
			}
			if res != (ToolResult{Text: "记住了。"}) {
				t.Fatalf("该收下：%+v", res)
			}
			if got := str(t, m, `SELECT text FROM facts`); got != c.want {
				t.Fatalf("存下来的正文 %q，应该是 %q", got, c.want)
			}
		})
	}
}

func TestParseSummaryTextBlankMatchesEmbeddingSide(t *testing.T) {
	t.Parallel()
	for _, c := range blankTextCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			quoted, err := json.Marshal(c.text) // 控制字符写成 \u001f 这样的转义，进得了 JSON
			if err != nil {
				t.Fatal(err)
			}
			op := `{"op":"candidate","kind":"fact","text":` + string(quoted) + `,"subject":"owner","src":[1]}`
			// 坏的放在好的前面和后面各一次：该拒的只丢它自己
			for _, ops := range []string{op + "," + goodOp, goodOp + "," + op} {
				out := mustParse(t, `{"episode_record":"no","ops":[`+ops+`]}`)
				want := []string{"主人养了一只猫"}
				if c.want == "" {
					if out.counts[CountTextEmpty] != 1 {
						t.Errorf("%s 应该是 1，计数 %v", CountTextEmpty, out.counts)
					}
				} else if strings.HasPrefix(ops, op) {
					want = []string{c.want, "主人养了一只猫"}
				} else {
					want = []string{"主人养了一只猫", c.want}
				}
				if got := opsOf(out); !reflect.DeepEqual(got, want) {
					t.Fatalf("留下的正文 %q，应该是 %q", got, want)
				}
			}
		})
	}
}
