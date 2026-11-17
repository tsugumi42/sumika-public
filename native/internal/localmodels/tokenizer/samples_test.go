package tokenizer

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// 对拍样本（规格-Go本地模型 第五节）：全是编的。cases.json 和 probes.json 是 Go 和 Python 两边共用的同一份，
// 随机串只有 Go 这边用（拿来证明紧凑词表树和上游原样逐个相同）。

var updateGolden = flag.Bool("update", false, "重写 testdata 里由测试生成的文件（探针集、迷你词表、上游 golden）")

// sample 是一条输入：Text 直接给，或者 Unit 重复 Count 次（大输入用食谱，不把几万字存进 git）
type sample struct {
	Cat   string `json:"cat"`
	Text  string `json:"text,omitempty"`
	Unit  string `json:"unit,omitempty"`
	Count int    `json:"count,omitempty"`
}

func (s sample) expand() string {
	if s.Unit != "" {
		return strings.Repeat(s.Unit, s.Count)
	}
	return s.Text
}

func readJSONFile(t testing.TB, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读 testdata/%s：%v", name, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("解析 testdata/%s：%v", name, err)
	}
}

func loadCases(t testing.TB) []string {
	t.Helper()
	var cs []sample
	readJSONFile(t, "cases.json", &cs)
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.expand()
	}
	return out
}

type probe struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

func loadProbes(t testing.TB) []string {
	t.Helper()
	var ps []probe
	readJSONFile(t, "probes.json", &ps)
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Text
	}
	return out
}

// probeChars 是探针要放进各种位置的单个字符：全部 Unicode 空白、C0 / C1 控制字符、DEL、几个常见的格式字符（Cf）。
func probeChars() []rune {
	var out []rune
	for r := rune(0); r <= 0x3000; r++ {
		if unicode.IsSpace(r) || unicode.Is(unicode.Cc, r) {
			out = append(out, r)
		}
	}
	cf := []rune{0x00AD, 0x061C, 0x180E, 0x200B, 0x200C, 0x200D, 0x200E, 0x200F, 0x2060, 0x2061, 0x2062, 0x2063, 0x2064, 0xFEFF}
	return append(out, cf...)
}

// probePatterns 是字符放的位置：{c} 换成被测字符
var probePatterns = []struct{ name, pat string }{
	{"夹在字母间", "我a{c}b你"},
	{"两侧带空格", "我a {c} b你"},
	{"开头", "{c}我a"},
	{"结尾", "我a{c}"},
	{"连写两个", "我a{c}{c}b你"},
	{"结尾再带空格", "我a{c} "},
}

func buildProbes() []probe {
	var ps []probe
	for _, c := range probeChars() {
		for _, p := range probePatterns {
			name := fmt.Sprintf("%s U+%04X", p.name, c)
			ps = append(ps, probe{Name: name, Text: strings.ReplaceAll(p.pat, "{c}", string(c))})
		}
	}
	// 下划线（U+2581）是 Metaspace 的替换符，字面出现的各种排法单独放几条
	for _, s := range []string{"▁", "▁▁", "a▁b", "a ▁ b", "▁a", "a▁", "▁ a", "a ▁", "a▁▁b", "a ▁▁ b"} {
		ps = append(ps, probe{Name: "下划线字面", Text: s})
	}
	return ps
}

// splitmix64：自己写，不用 math/rand，保证种子相同序列就相同，不随 Go 版本变
type rng struct{ s uint64 }

func (r *rng) next() uint64 {
	r.s += 0x9E3779B97F4A7C15
	z := r.s
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (r *rng) intn(n int) int { return int(r.next() % uint64(n)) }

const commonHan = "的一是不了人我在有他这中大来上国个到说们为子和你地出道也时年得就那要下以生会自着去之过家学对可她里后小么心多天而能好都然没日于起还发成事只作当想看文无开手十用主行方又如前所本见经头面公同三已老从动两长知民样现分将外但身些与高意进把法此实回二理美点月明其种声全工已话儿者向情部正名定女问力机给等几很业最间新什写相关一"

var weirdChars = []string{
	"\u0000", "\u0001", "\u0009", "\u000a", "\u000b", "\u000c", "\u000d", "\u001c",
	"\u001f", "\u007f", "\u0085", "\u00a0", "\u00ad", "\u1680", "\u2000", "\u2003",
	"\u200a", "\u200b", "\u200c", "\u200d", "\u200e", "\u2028", "\u2029", "\u202f",
	"\u205f", "\u2060", "\u3000", "\ufeff", "\ufffd", "\u2581", "\u2581\u2581",
}

var specialLiterals = []string{"<s>", "</s>", "<pad>", "<unk>", "<mask>", "<mask", "s>"}

var badBytes = []string{"\x80", "\xbf", "\xc0", "\xc1", "\xf5", "\xff", "\xe4\xbd", "\xed\xa0\x80", "\xf0\x9f", "\xc3"}

func (r *rng) rune(lo, hi rune) rune {
	for {
		c := lo + rune(r.next()%uint64(hi-lo+1))
		if c >= 0xD800 && c <= 0xDFFF {
			continue
		}
		return c
	}
}

func (r *rng) segment(sb *strings.Builder) {
	n := 1 + r.intn(6)
	switch r.intn(16) {
	case 0: // 拉丁字母
		for range n {
			sb.WriteByte(byte('a' + r.intn(26)))
		}
	case 1: // 数字
		for range n {
			sb.WriteByte(byte('0' + r.intn(10)))
		}
	case 2: // ASCII 标点
		const punct = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
		sb.WriteByte(punct[r.intn(len(punct))])
	case 3: // 空格
		for range 1 + r.intn(3) {
			sb.WriteByte(' ')
		}
	case 4: // 常用汉字
		rs := []rune(commonHan)
		for range n {
			sb.WriteRune(rs[r.intn(len(rs))])
		}
	case 5: // 任意汉字（很多不在词表里，走未知字）
		for range n {
			sb.WriteRune(r.rune(0x4E00, 0x9FFF))
		}
	case 6: // 假名
		for range n {
			sb.WriteRune(r.rune(0x3041, 0x30FF))
		}
	case 7: // 韩文音节，偶尔夹单个字母
		for range n {
			sb.WriteRune(r.rune(0xAC00, 0xD7A3))
		}
	case 8: // 韩文字母（NFD）
		for range n {
			sb.WriteRune(r.rune(0x1100, 0x11FF))
		}
	case 9: // emoji，偶尔带变体选择符和连接符
		for range n {
			sb.WriteRune(r.rune(0x1F300, 0x1FAFF))
			if r.intn(4) == 0 {
				sb.WriteRune(0xFE0F)
			}
			if r.intn(6) == 0 {
				sb.WriteRune(0x200D)
			}
		}
	case 10: // 全角和半角假名
		for range n {
			sb.WriteRune(r.rune(0xFF01, 0xFF9F))
		}
	case 11: // 空白、控制、零宽、下划线
		for range 1 + r.intn(3) {
			sb.WriteString(weirdChars[r.intn(len(weirdChars))])
		}
	case 12: // 组合附加符
		sb.WriteByte(byte('a' + r.intn(26)))
		for range n {
			sb.WriteRune(r.rune(0x0300, 0x036F))
		}
	case 13: // 特殊符号的字面值
		sb.WriteString(specialLiterals[r.intn(len(specialLiterals))])
	case 14: // 非法 UTF-8
		sb.WriteString(badBytes[r.intn(len(badBytes))])
	default: // 别的文字：拉丁扩展、希腊、西里尔、阿拉伯、泰文，和补充平面
		for range n {
			if r.intn(4) == 0 {
				sb.WriteRune(r.rune(0x10000, 0x1FFFF))
			} else {
				sb.WriteRune(r.rune(0x80, 0x2FFF))
			}
		}
	}
}

// randomStrings 生成 n 条随机字符串：绝大多数 0～12 段，千分之二是 1500 段的长串
func randomStrings(seed uint64, n int) []string {
	r := &rng{s: seed}
	out := make([]string, n)
	for i := range out {
		segs := r.intn(13)
		if r.intn(500) == 0 {
			segs = 1500
		}
		var sb strings.Builder
		for range segs {
			r.segment(&sb)
		}
		out[i] = sb.String()
	}
	return out
}

// idsDigest 把一批 id 序列摘成一个 SHA-256：每条先写个数再写各个 id（都是 uvarint），所以条与条之间不会串
func idsDigest(rows [][]int) (digest string, tokens int) {
	h := sha256.New()
	var buf []byte
	for _, row := range rows {
		buf = binary.AppendUvarint(buf[:0], uint64(len(row)))
		for _, id := range row {
			buf = binary.AppendUvarint(buf, uint64(id))
		}
		h.Write(buf)
		tokens += len(row)
	}
	return hex.EncodeToString(h.Sum(nil)), tokens
}
