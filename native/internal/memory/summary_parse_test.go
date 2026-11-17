package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// 后台整理的输出怎么读（W4，13.2.6）：每种坏输入一行。格式级的坏处整批失败（ErrBadOutput），条目级的只丢那一条，超长的截断不丢

// opsOf 把一批动作的正文摘出来（对照用）
func opsOf(out *summaryOut) []string {
	var texts []string
	for _, o := range out.ops {
		if o.op == "revoke" {
			texts = append(texts, "撤销 "+o.id)
		} else {
			texts = append(texts, o.text)
		}
	}
	return texts
}

func mustParse(t *testing.T, raw string) *summaryOut {
	t.Helper()
	out, err := parseSummary(raw)
	if err != nil {
		t.Fatalf("应该读得出来：%v\n输入：%s", err, raw)
	}
	return out
}

// 一条好动作，混在坏的里面：坏的丢掉，这一条要留下
const goodOp = `{"op":"candidate","kind":"fact","text":"主人养了一只猫","subject":"owner","src":[1]}`

func TestParseSummaryFormatFailures(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, raw, code string }{
		{"空回复", ``, "no_json"},
		{"只有一句话没有 JSON", `好的，我整理好了。`, "no_json"},
		{"只有一个数组：取到的第一个对象是一条动作，不是整理的输出", `[{"op":"candidate","kind":"fact","text":"x"}]`, "no_fields"},
		{"没有 episode_record 也没有 ops", `{"episode":"今天聊了考试。"}`, "no_fields"},
		{"两栏都是 null", `{"episode_record":null,"ops":null}`, "no_fields"},
		{"空对象", `{}`, "no_fields"},
		{"ops 是字符串", `{"ops":"没有"}`, "ops_not_array"},
		{"ops 是对象", `{"ops":{"op":"candidate"}}`, "ops_not_array"},
		{"ops 是数字", `{"episode_record":"no","ops":3}`, "ops_not_array"},
		{"大括号里不是 JSON", `{这批没有要记的}`, "json_invalid"},
		{"值没有引号", `{"episode_record": no, "ops": []}`, "json_invalid"},
		{"单引号（没抄 jsonrepair 的这种修法）", `{'episode_record': 'no', 'ops': []}`, "json_invalid"},
		{"截在键名中间", `{"episode_record":"no","ep`, "json_invalid"},
		{"截在 episode 的字符串中间：修出来是半句话，不能当整理成功", `{"episode_record":"yes","episode":"今天主人说`, "truncated_no_ops"},
		{"截在 episode 之后、ops 之前", `{"episode_record":"yes","episode":"今天主人说了考试。",`, "truncated_no_ops"},
		{"截在 ops 这个键后面", `{"episode_record":"yes","episode":"主人说了考试。","ops":`, "truncated_no_ops"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, err := parseSummary(c.raw)
			if !errors.Is(err, ErrBadOutput) || out != nil {
				t.Fatalf("应该是格式级失败，得到 %v（%v）", out, err)
			}
			if !strings.Contains(err.Error(), "原因 "+c.code) {
				t.Errorf("原因码应该是 %s：%v", c.code, err)
			}
		})
	}
}

// 错误只写原因码，不带模型的输出（清单 ㉑）
func TestParseSummaryErrorNeverQuotesTheReply(t *testing.T) {
	t.Parallel()
	secret := "主人的暗号是橘子汽水"
	for _, raw := range []string{
		`{"episode":"` + secret + `"}`,
		`{"ops":"` + secret + `"}`,
		`{"episode_record":"yes","episode":"` + secret,
		`{` + secret + `}`,
	} {
		_, err := parseSummary(raw)
		if err == nil || strings.Contains(err.Error(), "橘子汽水") || strings.Contains(err.Error(), "暗号") {
			t.Errorf("错误里不该有模型输出：%v", err)
		}
	}
}

// "这批什么都不记"是正常结果：ops 是空数组不算失败，修复出来的也一样（Bun 把它当失败，拆解坑 8，有意改）
func TestParseSummaryEmptyOpsIsLegit(t *testing.T) {
	t.Parallel()
	it := intended(t, "parse-empty-ops-is-legit")
	for _, c := range []struct{ name, raw string }{
		{"空数组", `{"episode_record":"no","episode_reason":"闲聊","ops":[]}`},
		{"只有 episode_record，没有 ops 这一栏", `{"episode_record":"no","episode_reason":"闲聊"}`},
		{"ops 是 null", `{"episode_record":"no","ops":null}`},
		{"截在 ops 数组刚开始：修出来是空数组", `{"episode_record":"no","episode_reason":"闲聊","ops":[`},
		{"截在 ops 第一条的中间：半截的那条整条丢掉，剩空数组", `{"episode_record":"no","ops":[{"op":"candidate","kind":"fa`},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := mustParse(t, c.raw)
			if len(out.ops) != 0 {
				t.Errorf("不该有动作：%v（%s）", opsOf(out), it.Go)
			}
		})
	}
}

// 模型回复的各种样子：围栏、前后说话、尾逗号、字符串里的换行、被截断
func TestParseSummaryReadsMessyReplies(t *testing.T) {
	t.Parallel()
	two := `{"op":"candidate","kind":"fact","text":"甲","src":[1]},{"op":"candidate","kind":"fact","text":"乙","src":[2]}`
	for _, c := range []struct {
		name, raw string
		want      []string
		repaired  bool
	}{
		{"正常", `{"episode_record":"no","ops":[` + two + `]}`, []string{"甲", "乙"}, false},
		{"代码块围栏", "```json\n{\"episode_record\":\"no\",\"ops\":[" + two + "]}\n```", []string{"甲", "乙"}, false},
		{"前后说话", "整理好了：\n{\"episode_record\":\"no\",\"ops\":[" + two + "]}\n以上，请查收。", []string{"甲", "乙"}, false},
		{"后面跟着带括号的说明", `{"episode_record":"no","ops":[` + two + `]} （注：{以上} 是全部）`, []string{"甲", "乙"}, false},
		{"数组尾逗号", `{"episode_record":"no","ops":[` + two + `,]}`, []string{"甲", "乙"}, true},
		{"对象尾逗号", `{"episode_record":"no","ops":[` + two + `],}`, []string{"甲", "乙"}, true},
		{"字符串里有真的换行和制表符", "{\"episode_record\":\"yes\",\"episode\":\"第一句。\n第二句\t结束。\",\"ops\":[" + two + "]}", []string{"甲", "乙"}, true},
		{"被截断：停在第三条中间，半截的那条整条丢掉", `{"episode_record":"no","ops":[` + two + `,{"op":"candidate","kind":"fact","text":"丙","src":[3`, []string{"甲", "乙"}, true},
		{"被截断：停在第三条的字符串里", `{"episode_record":"no","ops":[` + two + `,{"op":"candidate","kind":"fact","text":"丙是不完`, []string{"甲", "乙"}, true},
		{"被截断：停在两条之间的逗号后面", `{"episode_record":"no","ops":[` + two + `,`, []string{"甲", "乙"}, true},
		{"被截断：停在 ops 数组之后少了最后的括号", `{"episode_record":"no","ops":[` + two + `]`, []string{"甲", "乙"}, true},
		{"字符串里有括号和引号，不影响配括号", `{"episode_record":"no","ops":[{"op":"candidate","kind":"fact","text":"他说\"{好}\"]","src":[1]}]}`, []string{`他说"{好}"]`}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out := mustParse(t, c.raw)
			if got := opsOf(out); !reflect.DeepEqual(got, c.want) {
				t.Errorf("动作 %v，应该是 %v", got, c.want)
			}
			if (out.counts[CountRepaired] > 0) != c.repaired {
				t.Errorf("repaired 计数 %d，应该是 %v", out.counts[CountRepaired], c.repaired)
			}
		})
	}
}

// 一条坏不拖整批：每种条目级的坏处一行，和一条好动作放在一起，坏的丢掉、数在计数里，好的留下（Bun 任何一条不合规整批失败，拆解坑 7）
func TestParseSummaryOneBadOpDoesNotSinkTheBatch(t *testing.T) {
	t.Parallel()
	it := intended(t, "parse-one-bad-op-drops-one")
	long := strings.Repeat("字", 301)
	for _, c := range []struct{ name, bad, count string }{
		{"evidence 没有 id", `{"op":"evidence","src":[1]}`, CountEvidenceNoID},
		{"evidence 的 id 是空串", `{"op":"evidence","id":" ","src":[1]}`, CountEvidenceNoID},
		{"evidence 的 id 是数字", `{"op":"evidence","id":12,"src":[1]}`, CountEvidenceNoID},
		{"op 不认：update", `{"op":"update","kind":"fact","text":"x"}`, CountOpUnknown},
		{"op 不认：dispute", `{"op":"dispute","id":"x"}`, CountOpUnknown},
		{"op 不认：delete", `{"op":"delete","id":"x"}`, CountOpUnknown},
		{"op 没写", `{"kind":"fact","text":"x"}`, CountOpUnknown},
		{"op 是数字", `{"op":3,"kind":"fact","text":"x"}`, CountOpUnknown},
		{"这一项是字符串", `"主人喜欢猫"`, CountOpNotObject},
		{"这一项是数字", `3`, CountOpNotObject},
		{"这一项是 null", `null`, CountOpNotObject},
		{"这一项是数组", `[1,2]`, CountOpNotObject},
		{"kind 不认", `{"op":"candidate","kind":"gossip","text":"x"}`, CountKindInvalid},
		{"kind 没写", `{"op":"candidate","text":"x"}`, CountKindInvalid},
		{"kind 是数字", `{"op":"upsert_fact","kind":1,"text":"x"}`, CountKindInvalid},
		{"visibility 不认", `{"op":"upsert_fact","kind":"fact","text":"x","visibility":"secret"}`, CountVisibilityInvalid},
		{"visibility 是数字", `{"op":"upsert_fact","kind":"fact","text":"x","visibility":2}`, CountVisibilityInvalid},
		{"text 没写", `{"op":"candidate","kind":"fact"}`, CountTextEmpty},
		{"text 是空串", `{"op":"candidate","kind":"fact","text":""}`, CountTextEmpty},
		{"text 只有空白", `{"op":"candidate","kind":"fact","text":" \n\t "}`, CountTextEmpty},
		{"text 是数字", `{"op":"candidate","kind":"fact","text":5}`, CountTextEmpty},
		{"text 301 个字", `{"op":"candidate","kind":"fact","text":"` + long + `"}`, CountTextTooLong},
		{"text 301 个 emoji（按码点，不是字节）", `{"op":"candidate","kind":"fact","text":"` + strings.Repeat("🐱", 301) + `"}`, CountTextTooLong},
		{"subject 是数字", `{"op":"candidate","kind":"fact","text":"x","subject":7}`, CountSubjectInvalid},
		{"subject 是对象", `{"op":"candidate","kind":"fact","text":"x","subject":{"a":1}}`, CountSubjectInvalid},
		{"revoke 没有 id", `{"op":"revoke"}`, CountRevokeNoID},
		{"revoke 的 id 是空串", `{"op":"revoke","id":"  "}`, CountRevokeNoID},
		{"revoke 的 id 是数字", `{"op":"revoke","id":12}`, CountRevokeNoID},
	} {
		t.Run(c.name, func(t *testing.T) {
			// 坏的放在好的前面和后面各一次：丢掉哪一条都不影响另一条
			for _, ops := range []string{c.bad + "," + goodOp, goodOp + "," + c.bad} {
				out := mustParse(t, `{"episode_record":"no","ops":[`+ops+`]}`)
				if got := opsOf(out); !reflect.DeepEqual(got, []string{"主人养了一只猫"}) {
					t.Fatalf("坏的该丢、好的该留：%v（%s）", got, it.Go)
				}
				if out.counts[c.count] != 1 {
					t.Errorf("%s 应该是 1，计数 %v", c.count, out.counts)
				}
			}
		})
	}
}

// 边界上的"收"：300 个字收、301 个字丢；按码点算，不按字节；能收的写法不被误丢（B8 以外的邻居不变）
func TestParseSummaryAcceptsWhatItShould(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, op, text string }{
		{"正好 300 个字", `{"op":"candidate","kind":"fact","text":"` + strings.Repeat("字", 300) + `"}`, strings.Repeat("字", 300)},
		{"300 个 emoji（1200 字节）", `{"op":"candidate","kind":"fact","text":"` + strings.Repeat("🐱", 300) + `"}`, strings.Repeat("🐱", 300)},
		{"首尾空白去掉", `{"op":"candidate","kind":"fact","text":"  主人在上班\n"}`, "主人在上班"},
		{"candidate 带了 visibility（不认这一栏，也不因它丢）", `{"op":"candidate","kind":"fact","text":"甲","visibility":"nonsense"}`, "甲"},
		{"四种 kind：promise", `{"op":"upsert_fact","kind":"promise","text":"甲"}`, "甲"},
		{"四种 kind：preference", `{"op":"upsert_fact","kind":"preference","text":"甲"}`, "甲"},
		{"四种 kind：boundary", `{"op":"upsert_fact","kind":"boundary","text":"甲"}`, "甲"},
		{"subject 是 null", `{"op":"candidate","kind":"fact","text":"甲","subject":null}`, "甲"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out := mustParse(t, `{"ops":[`+c.op+`]}`)
			if got := opsOf(out); !reflect.DeepEqual(got, []string{c.text}) {
				t.Errorf("应该收下 %q，得到 %q", c.text, got)
			}
		})
	}
	// 时态：认的原样存，不认的记 null、不丢条、数下来
	out := mustParse(t, `{"ops":[
		{"op":"candidate","kind":"fact","text":"甲","temporal":"temporary"},
		{"op":"candidate","kind":"fact","text":"乙","temporal":"someday"},
		{"op":"candidate","kind":"fact","text":"丙","temporal":3},
		{"op":"candidate","kind":"fact","text":"丁"}]}`)
	var got []string
	for _, o := range out.ops {
		got = append(got, o.text+"="+o.temporal)
	}
	if want := []string{"甲=temporary", "乙=", "丙=", "丁="}; !reflect.DeepEqual(got, want) {
		t.Errorf("时态 %v，应该是 %v", got, want)
	}
	if out.counts[CountTemporalUnknown] != 2 {
		t.Errorf("不认的时态应该数 2 次：%v", out.counts)
	}
}

// 超长：截断而不丢（Bun 超过上限整批失败）
func TestParseSummaryClampsInsteadOfFailing(t *testing.T) {
	t.Parallel()
	it := intended(t, "parse-overlong-truncated")
	opN := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`{"op":"candidate","kind":"fact","text":"第%d条"}`, i+1)
		}
		return strings.Join(parts, ",")
	}
	for _, c := range []struct{ n, kept, over int }{{19, 19, 0}, {20, 20, 0}, {21, 20, 1}, {25, 20, 5}} {
		out := mustParse(t, `{"ops":[`+opN(c.n)+`]}`)
		if len(out.ops) != c.kept || out.counts[CountOpsOverLimit] != c.over {
			t.Errorf("%d 条动作：留 %d 条（应该 %d），丢 %d 条（应该 %d）（%s）", c.n, len(out.ops), c.kept, out.counts[CountOpsOverLimit], c.over, it.Go)
		}
	}
	if out := mustParse(t, `{"ops":[`+opN(21)+`]}`); out.ops[19].text != "第20条" {
		t.Errorf("留下的该是前 20 条，第 20 条是 %q", out.ops[19].text)
	}
	// 前 20 条里夹着坏的：算原始的前 20 条，不是"前 20 条好的"
	bad := strings.Repeat(`{"op":"evidence"},`, 5)
	if out := mustParse(t, `{"ops":[`+bad+opN(20)+`]}`); len(out.ops) != 15 || out.counts[CountOpsOverLimit] != 5 {
		t.Errorf("前 5 条是坏的：该留 15 条好的、丢 5 条超限的，得到 %d 和 %v", len(out.ops), out.counts)
	}

	ep := func(text string) *summaryOut {
		b, _ := json.Marshal(text)
		return mustParse(t, `{"episode_record":"yes","episode":`+string(b)+`,"ops":[]}`)
	}
	for _, c := range []struct {
		name, text string
		want       int
		truncated  bool
	}{
		{"799 字不动", strings.Repeat("好", 799), 799, false},
		{"800 字不动", strings.Repeat("好", 800), 800, false},
		{"801 字截到 800", strings.Repeat("好", 801), 800, true},
		{"801 个 emoji 截到 800 个，不留半个字符", strings.Repeat("🐱", 801), 800, true},
		{"800 字加一个句号：没有句末可截，硬切在第 800 个字上", strings.Repeat("好", 800) + "。", 800, true},
	} {
		out := ep(c.text)
		if n := utf8.RuneCountInString(out.episode); n != c.want || !utf8.ValidString(out.episode) {
			t.Errorf("%s：得到 %d 个码点（合法 %v），应该 %d", c.name, n, utf8.ValidString(out.episode), c.want)
		}
		if (out.counts[CountEpisodeTruncated] == 1) != c.truncated {
			t.Errorf("%s：truncated 计数 %d", c.name, out.counts[CountEpisodeTruncated])
		}
	}
	// 优先在句末截：后半段有句号就停在那里；句号太靠前（前一半）就不要，宁可硬切
	late := strings.Repeat("好", 600) + "。" + strings.Repeat("坏", 400)
	if out := ep(late); !strings.HasSuffix(out.episode, "。") || utf8.RuneCountInString(out.episode) != 601 {
		t.Errorf("该停在第 601 个字的句号上，得到 %d 个字、结尾 %q", utf8.RuneCountInString(out.episode), out.episode[len(out.episode)-3:])
	}
	early := strings.Repeat("好", 100) + "。" + strings.Repeat("坏", 900)
	if out := ep(early); utf8.RuneCountInString(out.episode) != 800 {
		t.Errorf("句号在前面，该硬切在 800 上，得到 %d 个字", utf8.RuneCountInString(out.episode))
	}
}

// episode_record 只认字符串 "yes" / "no"（ADR-168）；缺失或别的值有文字就当 yes，没有文字就不存；每次记一个计数（拆解坑 9）
func TestParseSummaryEpisodeRecord(t *testing.T) {
	t.Parallel()
	it := intended(t, "episode-record-bool-and-unknown")
	for _, c := range []struct {
		name, record, episode string // 原样放进 JSON，空串表示不写这一栏
		want                  string // 要存的片段正文，空 = 不存
		count                 string // 应该数到的计数（空格隔开），其余的都不该数
	}{
		{"yes 有文字", `"yes"`, `"今天聊了考试。"`, "今天聊了考试。", ""},
		{"no 有文字：不存", `"no"`, `"今天聊了考试。"`, "", CountEpisodeNo},
		{"no 没文字", `"no"`, ``, "", CountEpisodeNo},
		{"maybe 有文字：当 yes", `"maybe"`, `"今天聊了考试。"`, "", CountEpisodeUnknown},
		{"maybe 没文字：不存", `"maybe"`, ``, "", CountEpisodeUnknown},
		{"大写 YES 不认，有文字当 yes", `"YES"`, `"今天聊了考试。"`, "", CountEpisodeUnknown},
		{"数字 1 不认", `1`, `"今天聊了考试。"`, "", CountEpisodeUnknown},
		{"数组不认", `["no"]`, `"今天聊了考试。"`, "", CountEpisodeUnknown},
		{"没写这一栏，有文字：旧格式照存（B7）", ``, `"今天聊了考试。"`, "", CountEpisodeMissing},
		{"没写这一栏，没文字", ``, ``, "", CountEpisodeMissing},
		{"null 当没写", `null`, `"今天聊了考试。"`, "", CountEpisodeMissing},
		{"布尔 false 按 no（比 Bun 收紧）", `false`, `"今天聊了考试。"`, "", CountEpisodeBool + " " + CountEpisodeNo},
		{"布尔 true 按默认：有文字就存", `true`, `"今天聊了考试。"`, "", CountEpisodeBool},
		{"布尔 true 没文字", `true`, ``, "", CountEpisodeBool},
		{"yes 但 episode 是空串", `"yes"`, `""`, "", CountEpisodeYesEmpty},
		{"yes 但 episode 只有空白", `"yes"`, `"  \n "`, "", CountEpisodeYesEmpty},
		{"yes 但没写 episode", `"yes"`, ``, "", CountEpisodeYesEmpty},
		{"episode 是数字：不存，数下来", `"yes"`, `42`, "", CountEpisodeBadType + " " + CountEpisodeYesEmpty},
		{"episode 是对象", `"yes"`, `{"a":1}`, "", CountEpisodeBadType + " " + CountEpisodeYesEmpty},
		{"episode 是数组", `"yes"`, `["a"]`, "", CountEpisodeBadType + " " + CountEpisodeYesEmpty},
		{"episode 前后的空白去掉", `"yes"`, `"  今天聊了考试。\n"`, "今天聊了考试。", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fields := []string{`"ops":[]`}
			if c.record != "" {
				fields = append(fields, `"episode_record":`+c.record)
			}
			if c.episode != "" {
				fields = append(fields, `"episode":`+c.episode)
			}
			out := mustParse(t, `{`+strings.Join(fields, ",")+`}`)
			if out.episode != c.want {
				t.Errorf("要存的片段 %q，应该是 %q（%s）", out.episode, c.want, it.Go)
			}
			for _, k := range []string{CountEpisodeNo, CountEpisodeUnknown, CountEpisodeMissing, CountEpisodeBool, CountEpisodeBadType, CountEpisodeYesEmpty} {
				if (out.counts[k] == 1) != strings.Contains(" "+c.count+" ", " "+k+" ") {
					t.Errorf("计数 %s=%d，应该只有 %q 数到：%v", k, out.counts[k], c.count, out.counts)
				}
			}
		})
	}
	// episode_reason 只数，不存
	out := mustParse(t, `{"episode_record":"no","episode_reason":"大家在发表情包","ops":[]}`)
	if out.counts[CountEpisodeReason] != 1 || out.episode != "" {
		t.Errorf("episode_reason 该数 1 次、不存：%v", out.counts)
	}
}

// src：要的是消息序号的整数数组；没写、写坏的都按"没有 src"算并数下来，不静默（13.2.6）
func TestParseSummarySrc(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, src string // 原样放进 JSON，空串 = 不写这一栏
		want      []int64
		count     string
	}{
		{"一个", `[3]`, []int64{3}, ""},
		{"两个", `[3,4]`, []int64{3, 4}, ""},
		{"重复的去掉", `[3,3,4,3]`, []int64{3, 4}, ""},
		{"12.0 也是整数", `[12.0]`, []int64{12}, ""},
		{"科学计数 1e2", `[1e2]`, []int64{100}, ""},
		{"没写", ``, nil, CountSrcMissing},
		{"null", `null`, nil, CountSrcMissing},
		{"空数组", `[]`, nil, CountSrcMissing},
		{"不是数组：数字", `3`, nil, CountSrcUnparsable},
		{"不是数组：字符串", `"3"`, nil, CountSrcUnparsable},
		{"元素是字符串", `["3"]`, nil, CountSrcUnparsable},
		{"元素是小数", `[3.5]`, nil, CountSrcUnparsable},
		{"元素是负数", `[-1]`, nil, CountSrcUnparsable},
		{"元素是 0", `[0]`, nil, CountSrcUnparsable},
		{"元素是 null", `[null]`, nil, CountSrcUnparsable},
		{"好的里面混着一个坏的：整个当没有", `[3,"x",4]`, nil, CountSrcUnparsable},
		{"超过 10 个取前 10", `[1,2,3,4,5,6,7,8,9,10,11,12]`, []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, CountSrcTrimmed},
		{"超大的数", `[1e300]`, nil, CountSrcUnparsable},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fields := `"op":"candidate","kind":"fact","text":"甲"`
			if c.src != "" {
				fields += `,"src":` + c.src
			}
			out := mustParse(t, `{"ops":[{`+fields+`}]}`)
			if len(out.ops) != 1 {
				t.Fatalf("src 坏了不丢这一条：%v", out.counts)
			}
			if !reflect.DeepEqual(out.ops[0].src, c.want) {
				t.Errorf("src %v，应该是 %v", out.ops[0].src, c.want)
			}
			for _, k := range []string{CountSrcMissing, CountSrcUnparsable, CountSrcTrimmed} {
				if (out.counts[k] == 1) != (k == c.count) {
					t.Errorf("计数 %s=%d，应该只有 %q 数到：%v", k, out.counts[k], c.count, out.counts)
				}
			}
		})
	}
	// revoke、evidence 的 src 读出来由 plan 查是不是恰好一个序号：没写不数 src_missing，写了就照样读
	out := mustParse(t, `{"ops":[{"op":"revoke","id":"A"},{"op":"revoke","id":"B","src":[5]},{"op":"revoke","id":"C","src":"x"}]}`)
	if len(out.ops) != 3 || out.counts[CountSrcMissing] != 0 || out.counts[CountSrcUnparsable] != 1 || !reflect.DeepEqual(out.ops[1].src, []int64{5}) {
		t.Errorf("revoke 的 src：%v %v", out.ops, out.counts)
	}
}
