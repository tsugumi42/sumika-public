package tokenizer

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// miniLoaded 加载迷你词表：flavor 是 gte 或 bge，bge 带 Sequence 的 normalizer（Precompiled 用空的字符映射表，不改任何字）
func miniLoaded(t testing.TB, flavor string) (*Tokenizer, []byte) {
	t.Helper()
	tj := miniTokenizer(flavor)
	if flavor == "bge" {
		tj.Normalizer = miniSequenceNormalizer()
	}
	raw := marshalMini(tj)
	tk, err := Load(Spec{SHA256: sha256Hex(raw)}, raw)
	if err != nil {
		t.Fatal(err)
	}
	return tk, raw
}

func miniSequenceNormalizer() map[string]any {
	return map[string]any{"type": "Sequence", "normalizers": []any{
		map[string]any{"type": "Precompiled", "precompiled_charsmap": "AAAAAA=="},
		map[string]any{"type": "Strip", "strip_left": false, "strip_right": true},
		map[string]any{"type": "Replace", "pattern": map[string]any{"Regex": " {2,}"}, "content": "▁"},
	}}
}

func TestLoadChecksHashes(t *testing.T) {
	raw := miniTokenizerJSON("gte")
	good := Spec{SHA256: sha256Hex(raw)}
	if _, err := Load(good, raw); err != nil {
		t.Fatalf("对的登记该能加载：%v", err)
	}
	cases := map[string]struct {
		spec Spec
		raw  []byte
	}{
		"没有登记哈希":    {Spec{}, raw},
		"哈希不对":      {Spec{SHA256: strings.Repeat("0", 64)}, raw},
		"文件被改了一个字节": {good, append(slices.Clone(raw), ' ')},
		"哈希大写":      {Spec{SHA256: strings.ToUpper(good.SHA256)}, raw},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(c.spec, c.raw); err == nil {
				t.Fatal("该拒绝加载却成功了")
			}
		})
	}
}

// normalizer 的 Strip 和 Replace 按文件里写的顺序执行（bge）：结尾的空白（含全角空格、换行）被剥掉，两个以上的空格合成一个 ▁，
// 开头的空白不剥；gte 没有这两步。用迷你词表（空的字符映射表）看，和真文件的逐 id 对拍在 ref 数据那一步。
func TestNormalizerSequence(t *testing.T) {
	bge, _ := miniLoaded(t, "bge")
	gte, _ := miniLoaded(t, "gte")
	same := func(tk *Tokenizer, a, b string, want bool) {
		t.Helper()
		ca, _ := tk.Content(a)
		cb, _ := tk.Content(b)
		if slices.Equal(ca, cb) != want {
			t.Errorf("Content(%q) 与 Content(%q) 相同吗：得到 %v，该 %v（%v / %v）", a, b, !want, want, ca, cb)
		}
	}
	same(bge, "你好 ", "你好", true)
	same(bge, "你好 \n\u3000\t", "你好", true) // 结尾的各种空白都剥
	same(bge, "你好\u00a0", "你好", true)
	same(bge, "你好   世界", "你好 世界", true) // 两个以上的空格合成一个
	same(bge, "你好 世界", "你好世界", false)   // 单个空格还是分词边界
	same(bge, "a▁▁b", "a▁b", false)     // 字面的 ▁ 不合并（只合并空格）
	same(gte, "你好 ", "你好", true)        // gte 靠 WhitespaceSplit 丢掉空白，结果也一样
	same(gte, "你好\u00a0", "你好\u00a0\u00a0", true)
}

func TestTruncation(t *testing.T) {
	for _, flavor := range []string{"gte", "bge"} {
		t.Run(flavor, func(t *testing.T) {
			tk, _ := miniLoaded(t, flavor)
			long := strings.Repeat("今天晚上想吃火锅 ", 60)
			content, err := tk.Content(long)
			if err != nil || len(content) < 100 {
				t.Fatalf("样本该有足够多的 token：%d %v", len(content), err)
			}

			// 单条：内容截到 max-2，首 <s> 末 </s>，和不截断的前缀一致
			for _, max := range []int{2, 3, 10, 64, len(content) + 2, len(content) + 3} {
				ids, err := tk.EncodeSingle(long, max)
				if err != nil {
					t.Fatal(err)
				}
				wantLen := min(len(content)+2, max)
				if len(ids) != wantLen || ids[0] != tk.bos || ids[len(ids)-1] != tk.eos {
					t.Fatalf("max=%d：长度 %d（该 %d），首末 %d %d", max, len(ids), wantLen, ids[0], ids[len(ids)-1])
				}
				if !slices.Equal(ids[1:len(ids)-1], content[:len(ids)-2]) {
					t.Fatalf("max=%d：内容不是不截断时的前缀", max)
				}
			}
			if ids, _ := tk.EncodeSingle("", 8); !slices.Equal(ids, []int{tk.bos, tk.eos}) {
				t.Fatalf("空文本该是 [<s> </s>]：%v", ids)
			}
			if _, err := tk.EncodeSingle("a", 1); err == nil {
				t.Fatal("max 小于 2 该报错")
			}

			// 一对：总长不超过 max，查询不截，文档从尾部截，末尾是 </s>
			q := "今天晚上"
			qc, _ := tk.Content(q)
			dc, _ := tk.Content(long)
			for _, max := range []int{len(qc) + 2 + RerankSpecialReserve, 64, 200, 4000} {
				ids, err := tk.EncodePair(q, long, max)
				if err != nil {
					t.Fatal(err)
				}
				wantLen := min(len(qc)+len(dc)+4, max)
				if len(ids) != wantLen || ids[len(ids)-1] != tk.eos {
					t.Fatalf("max=%d：长度 %d（该 %d），末尾 %d", max, len(ids), wantLen, ids[len(ids)-1])
				}
				head := append(append([]int{tk.bos}, qc...), tk.eos, tk.eos)
				if !slices.Equal(ids[:len(head)], head) {
					t.Fatalf("max=%d：查询被改动或截断了", max)
				}
				if !slices.Equal(ids[len(head):len(ids)-1], dc[:len(ids)-len(head)-1]) {
					t.Fatalf("max=%d：文档不是不截断时的前缀", max)
				}
			}

			// 查询的预算：带两个特殊符号 ≤ max-16 通过，多 1 报错
			for _, n := range []int{5, 40} {
				query := strings.Repeat("今", n)
				qn, _ := tk.Content(query)
				max := len(qn) + 2 + RerankSpecialReserve
				if _, err := tk.EncodePair(query, "你好", max); err != nil {
					t.Fatalf("查询刚好在预算上该通过：%v", err)
				}
				var qerr *QueryTooLongError
				if _, err := tk.EncodePair(query, "你好", max-1); !errors.As(err, &qerr) || qerr.Tokens != len(qn)+2 || qerr.Budget != max-1-RerankSpecialReserve {
					t.Fatalf("查询多 1 该报 QueryTooLongError：%v", err)
				}
			}
			if n, _ := tk.Count(long); n != len(content)+2 {
				t.Fatalf("Count 该是内容数加 2：%d / %d", n, len(content)+2)
			}
		})
	}
}

// 特殊符号的字面值进来当普通文字切（S1，规格 2.4）：除首尾外不出现特殊符号的 id。
// 清单从文件的 added_tokens 读，不手写四个；id 3（<unk>）本来就可能由真的没见过的字符产生，所以只对这几条字面值断言。
func checkSpecialLiterals(t *testing.T, tk *Tokenizer, raw []byte) {
	t.Helper()
	fields, err := topLevelFields(raw)
	if err != nil {
		t.Fatal(err)
	}
	added, err := field(raw, fields, "added_tokens")
	if err != nil {
		t.Fatal(err)
	}
	var at []struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
		Special bool   `json:"special"`
	}
	if err := json.Unmarshal(added, &at); err != nil || len(at) != 5 {
		t.Fatalf("added_tokens 该有五个：%d %v", len(at), err)
	}
	ids := map[int]bool{}
	for _, a := range at {
		if !a.Special {
			t.Fatalf("%s 不是 special，这条测试的前提不成立", a.Content)
		}
		ids[a.ID] = true
	}
	for _, a := range at {
		for _, text := range []string{a.Content, "前 " + a.Content + " 后", a.Content + a.Content} {
			got, err := tk.EncodeSingle(text, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range got[1 : len(got)-1] {
				if ids[id] {
					t.Errorf("%q 里出现了特殊符号的 id %d：%v", text, id, got)
				}
			}
		}
	}
}

func TestSpecialLiteralsAreText(t *testing.T) {
	for _, flavor := range []string{"gte", "bge"} {
		t.Run("mini-"+flavor, func(t *testing.T) {
			tk, raw := miniLoaded(t, flavor)
			checkSpecialLiterals(t, tk, raw)
		})
	}
}

// 模糊测试：任意字符串不 panic、不报错，id 在词表范围里。迷你词表，所以没有真文件也能跑。
// 规格要求 `go test -fuzz` 跑 60 秒一次：go test ./internal/localmodels/tokenizer -run '^$' -fuzz FuzzEncodeGTE -fuzztime 60s -parallel 2
func fuzzEncode(f *testing.F, flavor string) {
	tk, _ := miniLoaded(f, flavor)
	vocab := len(miniTokenizer(flavor).Model.Vocab)
	for _, s := range loadCases(f)[:60] {
		f.Add(s)
	}
	for _, s := range randomStrings(3, 40) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ids, err := tk.EncodeSingle(s, 64)
		if err != nil {
			t.Fatalf("%q：%v", s, err)
		}
		for _, id := range ids {
			if id < 0 || id >= vocab {
				t.Fatalf("%q：id %d 不在词表范围 [0, %d)", s, id, vocab)
			}
		}
		if len(ids) < 2 || len(ids) > 64 || ids[0] != tk.bos || ids[len(ids)-1] != tk.eos {
			t.Fatalf("%q：结果的形状不对 %v", s, ids)
		}
	})
}

func FuzzEncodeGTE(f *testing.F) { fuzzEncode(f, "gte") }
func FuzzEncodeBGE(f *testing.F) { fuzzEncode(f, "bge") }
