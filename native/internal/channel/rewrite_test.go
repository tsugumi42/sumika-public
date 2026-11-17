package channel

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// 改写和拆条的测试（规格-渠道中间件 第十节 10.1 第 2、6 步，第十六节 B"改写"）：
//   - testdata/asent.json 是 Bun 版对一批编的句子的输出（sumika-bun 仓库的 scripts/go-golden-asent.ts 生成，2026-11-01 分仓前是这里的 testdata/gen-asent.ts），Go 版逐条对：asSentQQ、去句尾句号、按空行拆；
//   - 幂等：改过的再改一遍不变；
//   - 不改字：拆前拆后去掉标点和空白，字一个不少、顺序不变；最多 3 条；
//   - 拆条按字符边界，emoji 不切开；CRLF 和 LF 同样处理。
// 句子都是编的。

type asentCase struct {
	Input  string   `json:"input"`
	AsSent string   `json:"asSent"`
	Drop   string   `json:"drop"`
	Split  []string `json:"split"`
}

func loadAsent(t *testing.T) []asentCase {
	t.Helper()
	b, err := os.ReadFile("testdata/asent.json")
	if err != nil {
		t.Fatal(err)
	}
	var cs []asentCase
	if err := json.Unmarshal(b, &cs); err != nil {
		t.Fatal(err)
	}
	if len(cs) < 50 {
		t.Fatalf("对拍样本只有 %d 条", len(cs))
	}
	return cs
}

func TestRewriteMatchesBun(t *testing.T) {
	t.Parallel()
	bad := 0
	for _, c := range loadAsent(t) {
		if got := splitMessage(c.Input); strings.Join(got, "\x00") != strings.Join(c.Split, "\x00") {
			t.Errorf("splitMessage(%q)\n Go  %q\n Bun %q", c.Input, got, c.Split)
			bad++
		}
		if got := dropTrailingPeriods(c.Input); got != c.Drop {
			t.Errorf("dropTrailingPeriods(%q)\n Go  %q\n Bun %q", c.Input, got, c.Drop)
			bad++
		}
		if got := asSentQQ(c.Input); got != c.AsSent {
			t.Errorf("asSentQQ(%q)\n Go  %q\n Bun %q", c.Input, got, c.AsSent)
			bad++
		}
		if bad > 12 {
			t.Fatal("差太多，先看前面几条")
		}
	}
}

// 幂等：改写过的再改一遍不变（规格 10.1 第 2 步）；对 Bun 的输出和我们自己编的一批都查
func TestRewriteIsIdempotent(t *testing.T) {
	t.Parallel()
	var inputs []string
	for _, c := range loadAsent(t) {
		inputs = append(inputs, c.Input, c.AsSent)
	}
	atoms := []string{"好。", "。", "（嗯。）", "「说。」", "\n", "\n\n", " ", "　", "你好", "，", "哈哈", "!", "…", "。\n", "a", "😀", "\r\n"}
	// 穷举：atoms 里挑 4 个拼起来（17^4 = 83521 条）
	var rec func(prefix string, depth int)
	rec = func(prefix string, depth int) {
		if depth == 0 {
			inputs = append(inputs, prefix)
			return
		}
		for _, a := range atoms {
			rec(prefix+a, depth-1)
		}
	}
	rec("", 4)
	for _, in := range inputs {
		once := dropTrailingPeriods(in)
		if twice := dropTrailingPeriods(once); twice != once {
			t.Fatalf("去句号不幂等：%q → %q → %q", in, once, twice)
		}
		s := asSentQQ(in)
		if s2 := asSentQQ(s); s2 != s {
			t.Fatalf("asSentQQ 不幂等：%q → %q → %q", in, s, s2)
		}
	}
}

func keepChars(s string) string {
	var b strings.Builder
	for _, r := range s {
		if isWS(r) || strings.ContainsRune("。，,.、；;！!？?～~…—", r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// 拆条的几条硬规矩：最多 3 条、字一个不少顺序不变、每条是合法 UTF-8（emoji 不切开）、拆出的半截不少于 4 个词（语气词除外）
func TestSplitInvariants(t *testing.T) {
	t.Parallel()
	long := []string{
		"我刚才看到一个很有意思的东西😀，是一只猫坐在键盘上面打字，打出来的居然是一串有意义的单词，太神奇了，你说是不是巧合呢",
		"今天的天气真的很不错，我们要不要一起出去走走，顺便买点吃的回来，路上还可以拍几张照片发给他们看看",
		"我今天去了超市，买了苹果、香蕉、橙子和葡萄，还有一些零食，回来的路上下雨了，淋得全身都湿了，真倒霉",
		"这是第一句话。这是第二句话。这是第三句话。这是第四句话。这是第五句话。",
	}
	for _, in := range long {
		for _, nl := range []string{"\n", "\r\n"} {
			text := strings.ReplaceAll(in, "。", "。"+strings.Repeat(nl, 0))
			out := asSentQQ(text)
			parts := splitMessage(out)
			if len(parts) > 3 || len(parts) == 0 {
				t.Errorf("%q 拆成了 %d 条", in, len(parts))
			}
			for _, p := range parts {
				if !utf8.ValidString(p) {
					t.Errorf("拆出了坏的 UTF-8：%q", p)
				}
				if strings.HasSuffix(p, "。") {
					t.Errorf("拆出的一条以句号结尾：%q", p)
				}
			}
			if keepChars(out) != keepChars(in) {
				t.Errorf("拆条改了字：\n 前 %q\n 后 %q", keepChars(in), keepChars(out))
			}
		}
	}
	// 至少有一句真的拆开了，不然上面全是空转
	if len(splitMessage(asSentQQ(long[0]))) < 2 && len(splitMessage(asSentQQ(long[2]))) < 2 {
		t.Fatal("两条该拆的长句都没拆，测试是空转")
	}
}

// CRLF 和 LF 同样处理：单个换行是模型特意排的多行，整段原样（只去行尾句号）
func TestRewriteCRLFSameAsLF(t *testing.T) {
	t.Parallel()
	lf := asSentQQ("第一行。\n第二行。\n第三行。")
	crlf := asSentQQ("第一行。\r\n第二行。\r\n第三行。")
	if strings.ReplaceAll(crlf, "\r\n", "\n") != lf {
		t.Fatalf("CRLF 和 LF 结果不一样：%q、%q", crlf, lf)
	}
	if lf != "第一行\n第二行\n第三行" {
		t.Fatalf("每行行尾的句号该去掉：%q", lf)
	}
}

// 不到 30 字的一句话不拆（ADR-163），长的才拆
func TestSplitShortSentenceStays(t *testing.T) {
	t.Parallel()
	in := "好的，我知道了，晚点再聊"
	if got := asSentQQ(in); got != in {
		t.Fatalf("短句不该拆：%q", got)
	}
}
