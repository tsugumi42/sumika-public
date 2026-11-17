package tokenizer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// 和 Python 的逐 id 对拍（规格-Go本地模型 GM1 验收 1、2、4）：
// testdata/ref-ids-{gte,bge}.json、ref-pairs-bge.json 是 tools/local-models/parity_ref.py 用服务实际走的那条路
// （server.build_tokenizer，含截断）生成的参考；这里的 Go 分词器对同一批输入给出的 id 必须逐个相同，
// 例外只能是 testdata/known-diffs.json 里登记的，并且每条都要"已解释"：
// 把它所属类别（testdata/categories.json）的字符去掉以后，Python 和 Go 对去掉之后的文本的 id 必须完全一致。
// 不一致、却归不到类别或去掉以后仍不一致的，就是真的分词 bug，测试红。登记的集合要和实际的一模一样，多一条少一条都红
// （少一条是上游修好了，该把登记删掉）。

func loadCategories(t testing.TB) map[string]Category {
	t.Helper()
	var cats map[string]Category
	readJSONFile(t, "categories.json", &cats)
	return cats
}

type explained struct {
	Cats  []string `json:"cats"`
	IDs   []int    `json:"ids"`
	Error string   `json:"error"`
}

type refIDs struct {
	Cases           [][]int      `json:"cases"`
	Probes          [][]int      `json:"probes"`
	CasesExplained  []*explained `json:"cases_explained"`
	ProbesExplained []*explained `json:"probes_explained"`
	// 全长超过 2048 的样本，Python 的分词器同样截到 2048 的 id（只有 gte；其余是 null）。S2：生产的 --max-line-tokens 2048
	CasesTrunc2048 [][]int `json:"cases_trunc2048"`
}

type refPair struct {
	Name        string     `json:"name"`
	QueryTokens int        `json:"query_tokens"`
	IDs         []int      `json:"ids"`
	Error       string     `json:"error"`
	Explained   *explained `json:"explained"`
}

type knownDiff struct {
	Model    string `json:"model"`
	Source   string `json:"source"` // cases、probes、pairs
	Index    int    `json:"index"`
	Category string `json:"category"` // 类别名用加号连起来
	Text     string `json:"text"`     // 给人看的（不可见字符写成转义）
	Go       []int  `json:"go"`
	Py       []int  `json:"py"`
}

func (d knownDiff) key() string { return fmt.Sprintf("%s/%s/%d", d.Model, d.Source, d.Index) }

const rerankMaxTokens = 1024

func singleMaxTokens(model string) int {
	if model == "bge" {
		return rerankMaxTokens
	}
	return 8192
}

// singleDiffs 对 cases 和 probes 逐条比；返回登记用的差异，和比较过的条数
func singleDiffs(t *testing.T, model string, tk *Tokenizer, cats map[string]Category) (diffs []knownDiff, compared, skipped int) {
	t.Helper()
	var ref refIDs
	readJSONFile(t, "ref-ids-"+model+".json", &ref)
	for _, set := range []struct {
		source string
		texts  []string
		ids    [][]int
		expl   []*explained
	}{
		{"cases", loadCases(t), ref.Cases, ref.CasesExplained},
		{"probes", loadProbes(t), ref.Probes, ref.ProbesExplained},
	} {
		if len(set.texts) != len(set.ids) || len(set.texts) != len(set.expl) {
			t.Fatalf("%s 的参考数据条数（%d、%d）和样本（%d）对不上：样本改过就要从 git 历史（提交 76f0488faa6a 及以前）取回 tools/local-models/parity_ref.py 重跑", set.source, len(set.ids), len(set.expl), len(set.texts))
		}
		for i, text := range set.texts {
			if set.ids[i] == nil {
				skipped++ // Python 抛错（bge 的截断策略是 only_second，单条超过上限它会抛）
				continue
			}
			compared++
			got, err := tk.EncodeSingle(text, singleMaxTokens(model))
			if err != nil {
				t.Fatalf("%s %s #%d：%v", model, set.source, i, err)
			}
			if slices.Equal(got, set.ids[i]) {
				continue
			}
			diffs = append(diffs, judge(t, model, set.source, i, text, cats, set.expl[i], got, set.ids[i], func(rest string) ([]int, error) {
				return tk.EncodeSingle(rest, singleMaxTokens(model))
			}))
		}
	}
	return diffs, compared, skipped
}

// judge 登记一条不一致，并判它"已解释"：归得到类别，且去掉类别字符以后 Python 和 Go 一致
func judge(t *testing.T, model, source string, index int, text string, cats map[string]Category, expl *explained, got, want []int, encode func(rest string) ([]int, error)) knownDiff {
	t.Helper()
	names, rest := PresentCategories(cats, model, text)
	label := fmt.Sprintf("%s %s #%d %q", model, source, index, shorten(text))
	d := knownDiff{Model: model, Source: source, Index: index, Category: strings.Join(names, "+"), Text: shorten(text), Go: got, Py: want}
	switch {
	case len(names) == 0:
		t.Errorf("%s：和 Python 不一致，归不到任何已登记的类别（真的分词 bug）\n  Go %v\n  Py %v", label, head(got), head(want))
	case expl == nil || !slices.Equal(expl.Cats, names):
		t.Errorf("%s：类别 %v 和参考数据里的 %+v 对不上（categories.json 改过就要重新跑 parity_ref.py）", label, names, expl)
	case expl.Error != "":
		t.Errorf("%s：去掉类别字符以后 Python 抛错了（%s）", label, expl.Error)
	default:
		gotRest, err := encode(rest[0])
		if err != nil || !slices.Equal(gotRest, expl.IDs) {
			t.Errorf("%s：把类别 %v 的字符去掉以后，Python 和 Go 仍不一致（%v），这不是类别造成的\n  Go %v\n  Py %v", label, names, err, head(gotRest), head(expl.IDs))
		}
	}
	return d
}

func shorten(s string) string {
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}

func head(ids []int) []int { return ids[:min(len(ids), 24)] }

// pairDiffs 重排的一对：Python 的 server 在带特殊符号的查询超过 max-16 时直接 400，所以超预算的对要求 Go 报同样的错
func pairDiffs(t *testing.T, tk *Tokenizer, cats map[string]Category) (diffs []knownDiff, compared int) {
	t.Helper()
	var in struct {
		Pairs []struct {
			Name string `json:"name"`
			Q    sample `json:"q"`
			D    sample `json:"d"`
		} `json:"pairs"`
	}
	readJSONFile(t, "pairs.json", &in)
	var ref struct {
		Pairs []refPair `json:"pairs"`
	}
	readJSONFile(t, "ref-pairs-bge.json", &ref)
	if len(in.Pairs) != len(ref.Pairs) {
		t.Fatalf("pairs.json 有 %d 对，参考数据有 %d 对：改过就要重新跑 parity_ref.py", len(in.Pairs), len(ref.Pairs))
	}
	for i, p := range in.Pairs {
		r := ref.Pairs[i]
		q, d := p.Q.expand(), p.D.expand()
		got, err := tk.EncodePair(q, d, rerankMaxTokens)
		if r.QueryTokens > rerankMaxTokens-RerankSpecialReserve {
			var qerr *QueryTooLongError
			if !errors.As(err, &qerr) || qerr.Tokens != r.QueryTokens {
				t.Errorf("pair %q：查询有 %d 个 token，超出预算，该报 QueryTooLongError{%d}，得到 %v", p.Name, r.QueryTokens, r.QueryTokens, err)
			}
			continue
		}
		compared++
		if err != nil {
			t.Fatalf("pair %q：%v", p.Name, err)
		}
		if n, err := tk.Count(q); err != nil || n != r.QueryTokens {
			t.Errorf("pair %q：Count(查询) 是 %d，Python 是 %d（%v）", p.Name, n, r.QueryTokens, err)
		}
		if r.IDs == nil {
			t.Errorf("pair %q：Python 抛了 %s，Go 却给出了结果", p.Name, r.Error)
			continue
		}
		if slices.Equal(got, r.IDs) {
			continue
		}
		names, rest := PresentCategories(cats, "bge", q, d)
		dd := knownDiff{Model: "bge", Source: "pairs", Index: i, Category: strings.Join(names, "+"), Text: shorten(p.Name), Go: got, Py: r.IDs}
		switch {
		case len(names) == 0:
			t.Errorf("pair %q：和 Python 不一致，归不到任何已登记的类别（真的分词 bug）\n  Go %v\n  Py %v", p.Name, head(got), head(r.IDs))
		case r.Explained == nil || !slices.Equal(r.Explained.Cats, names) || r.Explained.Error != "":
			t.Errorf("pair %q：类别 %v 和参考数据里的 %+v 对不上", p.Name, names, r.Explained)
		default:
			gotRest, err := tk.EncodePair(rest[0], rest[1], rerankMaxTokens)
			if err != nil || !slices.Equal(gotRest, r.Explained.IDs) {
				t.Errorf("pair %q：把类别 %v 的字符去掉以后，Python 和 Go 仍不一致（%v）\n  Go %v\n  Py %v", p.Name, names, err, head(gotRest), head(r.Explained.IDs))
			}
		}
		diffs = append(diffs, dd)
	}
	return diffs, compared
}

func knownDiffsPath() string { return filepath.Join("testdata", "known-diffs.json") }

func writeKnownDiffs(t *testing.T, diffs []knownDiff) {
	t.Helper()
	var b bytes.Buffer
	b.WriteString("{\n\"diffs\": [\n")
	for i, d := range diffs {
		var line bytes.Buffer
		enc := json.NewEncoder(&line)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(d); err != nil {
			t.Fatal(err)
		}
		b.Write(escapeJSONInvisible(bytes.TrimSpace(line.Bytes())))
		if i < len(diffs)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("]\n}\n")
	if err := os.WriteFile(knownDiffsPath(), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// escapeJSONInvisible：json.Marshal 不转义 U+2028 以外的不可见字符，存进文件前统一写成转义
func escapeJSONInvisible(j []byte) []byte {
	var sb strings.Builder
	for _, r := range string(j) {
		if r > 0x7e && (unicode.IsSpace(r) || unicode.In(r, unicode.Cf, unicode.Mn, unicode.Zs) || r == 0x2581) {
			fmt.Fprintf(&sb, "\\u%04x", r)
		} else {
			sb.WriteRune(r)
		}
	}
	return []byte(sb.String())
}

func TestTokenizerParity(t *testing.T) {
	cats := loadCategories(t)
	var all []knownDiff
	ran := map[string]bool{}
	for _, model := range []string{"gte", "bge"} {
		t.Run(model, func(t *testing.T) {
			tk := realLoaded(t, model) // 没设 SUMIKA_TEST_MODELS_ROOT 在这里跳过
			ran[model] = true
			diffs, compared, skipped := singleDiffs(t, model, tk, cats)
			total := compared
			if model == "bge" {
				pd, pc := pairDiffs(t, tk, cats)
				diffs = append(diffs, pd...)
				compared += pc
				total = compared
			}
			t.Logf("PARITY %s %d/%d 逐个相同（登记的已解释差异 %d 条，Python 抛错跳过 %d 条）", model, compared-len(diffs), total, len(diffs), skipped)
			all = append(all, diffs...)
		})
	}
	if t.Failed() {
		return
	}
	if len(ran) == 0 {
		t.Skip("没设 SUMIKA_TEST_MODELS_ROOT，没有跑任何一个模型")
	}
	if *updateGolden {
		if len(ran) != 2 {
			t.Fatal("重写 known-diffs.json 要 gte 和 bge 都跑过")
		}
		writeKnownDiffs(t, all)
		t.Logf("已重写 %s（%d 条）", knownDiffsPath(), len(all))
		return
	}
	var reg struct {
		Diffs []knownDiff `json:"diffs"`
	}
	readJSONFile(t, "known-diffs.json", &reg)
	want := map[string]knownDiff{}
	for _, d := range reg.Diffs {
		if ran[d.Model] { // 只比跑过的模型
			want[d.key()] = d
		}
	}
	got := map[string]knownDiff{}
	for _, d := range all {
		got[d.key()] = d
		w, ok := want[d.key()]
		switch {
		case !ok:
			t.Errorf("没登记的差异 %s（类别 %s）：Go %v，Python %v", d.key(), d.Category, head(d.Go), head(d.Py))
		case w.Category != d.Category || !slices.Equal(w.Go, d.Go) || !slices.Equal(w.Py, d.Py):
			t.Errorf("差异 %s 和登记的不一样：类别 %s / %s，Go %v / %v，Python %v / %v", d.key(), d.Category, w.Category, head(d.Go), head(w.Go), head(d.Py), head(w.Py))
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("登记的差异 %s 不再出现了（Go 或 Python 变了？确认是修好了就把登记删掉）", k)
		}
	}
}

// S2（规格 2.5、2.4）：生产传 --max-line-tokens 2048，超过的在 Go 一侧截断，Python 的服务没有这个开关，不截。
// 截断的语义必须逐位相同：Go 的 EncodeSingle(text, 2048) 对 Python 的分词器同样截到 2048（内容留 2046 个再补首尾）。
// Python 全长对 Go 截断的向量余弦不在这里（要跑模型），见规格 2.4 S2 一条的实测。
// 改坏检查：把 maxTokens 2048 改成 2049 或把截断多留一个，这条红
func TestTruncation2048MatchesPython(t *testing.T) {
	tk := realLoaded(t, "gte") // 没设 SUMIKA_TEST_MODELS_ROOT 在这里跳过
	var ref refIDs
	readJSONFile(t, "ref-ids-gte.json", &ref)
	cases := loadCases(t)
	if len(ref.CasesTrunc2048) != len(cases) {
		t.Fatalf("参考数据的 cases_trunc2048 有 %d 条，样本有 %d 条：样本改过就要重跑 parity_ref.py tokenizer", len(ref.CasesTrunc2048), len(cases))
	}
	compared := 0
	for i, want := range ref.CasesTrunc2048 {
		if want == nil {
			continue
		}
		compared++
		got, err := tk.EncodeSingle(cases[i], 2048)
		if err != nil {
			t.Fatalf("cases #%d：%v", i, err)
		}
		if len(want) != 2048 {
			t.Errorf("cases #%d：Python 截断后该正好 2048 个 id，得到 %d 个（参考数据不对）", i, len(want))
		}
		if !slices.Equal(got, want) {
			t.Errorf("cases #%d %q：Go 截到 2048 和 Python 不一致\n  Go %v …%v\n  Py %v …%v", i, shorten(cases[i]), head(got), got[max(0, len(got)-4):], head(want), want[max(0, len(want)-4):])
		}
	}
	if compared < 3 {
		t.Fatalf("全长超过 2048 token 的样本只有 %d 条，该有 3 条以上（长行、长词、聊天体长文等），不然这条什么也没比", compared)
	}
	t.Logf("S2：%d 条超过 2048 token 的样本，Go 截到 2048 和 Python 逐位相同", compared)
}
