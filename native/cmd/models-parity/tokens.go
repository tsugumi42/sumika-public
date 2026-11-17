package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"sumika-native/internal/embedinputs"
	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/localmodels/pool"
	"sumika-native/internal/localmodels/tokenizer"
)

// 分词 id 的对拍（规格 2.4、第五节前两行）。Go 的分词器直接调（不走 HTTP），对 Python 的 id：
//   - 编的 cases、probes、pairs：读 tokenizer/testdata 里 parity_ref.py 导好的参照（ref-ids-*.json、ref-pairs-bge.json），
//     不一致的要么"已解释"（归得到已登记的类别，去掉类别字符以后 Go 和 Python 一致），要么是真 bug；
//     差异的集合还要和 known-diffs.json 一模一样，多一条少一条都红。判法是 tokenizer.PresentCategories，和 TestTokenizerParity 同一份；
//   - 编的语料和真实样本：现场用 parity_ref.py real 导出 Python 的 id，同一套判法（真实样本里的未解释数就是 T5 的命中率之外的真 bug 数）。

type verdict struct {
	Same bool
	Cats []string // 已解释的差异属于哪些类别
	Bug  string   // 不为空 = 未解释的不一致（真的分词 bug），写原因（不含文字）
}

func (v verdict) explained() bool { return !v.Same && v.Bug == "" }

// fixable：已解释的差异里，属于"以后可能要做字素簇切分去消掉"的那些（T1、T5 等）。S1 是有意不同，不算：它不是缺陷，也不会因为做了切分就没了
func (v verdict) fixable() bool {
	for _, c := range v.Cats {
		if c != "S1" {
			return true
		}
	}
	return false
}

// tokenResult 是分词这一段的结果：每段文字 / 每一对的判定，后面向量和重排的比较要用它决定该套哪个门槛。
type tokenResult struct {
	Single map[string]verdict // gte 的单句，按文字
	Pair   map[[2]string]verdict
	Rows   []Row
}

func (t *tokenResult) single(text string) (verdict, bool) {
	v, ok := t.Single[text]
	return v, ok
}

type explainedRef struct {
	Cats  []string `json:"cats"`
	IDs   []int    `json:"ids"`
	Error string   `json:"error"`
}

type refIDs struct {
	Cases           [][]int         `json:"cases"`
	Probes          [][]int         `json:"probes"`
	CasesExplained  []*explainedRef `json:"cases_explained"`
	ProbesExplained []*explainedRef `json:"probes_explained"`
}

type refPairCell struct {
	IDs       []int         `json:"ids"`
	Error     string        `json:"error"`
	Explained *explainedRef `json:"explained"`
}

type knownDiff struct {
	Model  string `json:"model"`
	Source string `json:"source"`
	Index  int    `json:"index"`
	Go     []int  `json:"go"`
	Py     []int  `json:"py"`
}

func (d knownDiff) key() string { return fmt.Sprintf("%s/%s/%d", d.Model, d.Source, d.Index) }

type tokenizers struct {
	gte, bge       *tokenizer.Tokenizer
	gteSpec, bgeSp engine.Spec
}

func loadTokenizers(root string) (*tokenizers, error) {
	t := &tokenizers{}
	for _, s := range engine.BuiltinSpecs() {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(s.Dir), pool.TokenizerFile))
		if err != nil {
			return nil, fmt.Errorf("读 %s 的分词器：%w", s.ID, err)
		}
		tk, err := tokenizer.Load(tokenizer.Spec{SHA256: s.TokenizerSHA256}, raw)
		if err != nil {
			return nil, fmt.Errorf("加载 %s 的分词器：%w", s.ID, err)
		}
		switch s.ID {
		case gteID:
			t.gte, t.gteSpec = tk, s
		case bgeID:
			t.bge, t.bgeSp = tk, s
		}
	}
	if t.gte == nil || t.bge == nil {
		return nil, fmt.Errorf("登记表里没有 %s 或 %s", gteID, bgeID)
	}
	return t, nil
}

// judgeDiff 判一个不一致的样本：归得到类别、参考数据里的类别对得上、去掉类别字符以后 Go 和 Python 一致，才是"已解释"。
func judgeDiff(cats map[string]tokenizer.Category, model string, texts []string, expl *explainedRef, encode func(rest []string) ([]int, error)) verdict {
	names, rest := tokenizer.PresentCategories(cats, model, texts...)
	switch {
	case len(names) == 0:
		return verdict{Bug: "归不到任何已登记的类别"}
	case expl == nil || !slices.Equal(expl.Cats, names):
		return verdict{Bug: "类别和参考数据里的对不上（categories.json 改过就要重新导参考）"}
	case expl.Error != "":
		return verdict{Bug: "去掉类别字符以后 Python 抛错了"}
	}
	got, err := encode(rest)
	if err != nil || !slices.Equal(got, expl.IDs) {
		return verdict{Bug: "去掉类别字符以后 Python 和 Go 仍不一致，不是类别造成的"}
	}
	return verdict{Cats: names}
}

// tokenStageCommitted：编的 cases、probes、pairs，对 tokenizer/testdata 里的参照。
func tokenStageCommitted(src sourceFiles, tks *tokenizers, res *tokenResult, pl *plan) error {
	catsRaw, err := os.ReadFile(src.path("tokenizer", "testdata", "categories.json"))
	if err != nil {
		return err
	}
	cats, err := tokenizer.ParseCategories(catsRaw)
	if err != nil {
		return err
	}
	var known struct {
		Diffs []knownDiff `json:"diffs"`
	}
	if err := readJSON(src.path("tokenizer", "testdata", "known-diffs.json"), &known); err != nil {
		return err
	}
	cases, err := src.cases()
	if err != nil {
		return err
	}
	caseTexts := make([]string, len(cases))
	for i, c := range cases {
		caseTexts[i] = c.expand()
	}
	probes, err := src.probes()
	if err != nil {
		return err
	}
	found := map[string]knownDiff{}
	for _, model := range []string{"gte", "bge"} {
		tk, maxTok := tks.gte, tks.gteSpec.MaxTokens
		if model == "bge" {
			tk, maxTok = tks.bge, tks.bgeSp.MaxTokens
		}
		var ref refIDs
		if err := readJSON(src.path("tokenizer", "testdata", "ref-ids-"+model+".json"), &ref); err != nil {
			return err
		}
		compared, same, skipped, bugs := 0, 0, 0, 0
		for _, set := range []struct {
			source string
			texts  []string
			ids    [][]int
			expl   []*explainedRef
		}{{"cases", caseTexts, ref.Cases, ref.CasesExplained}, {"probes", probes, ref.Probes, ref.ProbesExplained}} {
			if len(set.texts) != len(set.ids) || len(set.texts) != len(set.expl) {
				return fmt.Errorf("%s 的参考数据条数（%d）和样本（%d）对不上：样本改过就要重新跑 parity_ref.py", set.source, len(set.ids), len(set.texts))
			}
			for i, text := range set.texts {
				if set.ids[i] == nil {
					skipped++
					continue
				}
				compared++
				got, err := tk.EncodeSingle(text, maxTok)
				if err != nil {
					return fmt.Errorf("%s %s#%d：%v", model, set.source, i, err)
				}
				if slices.Equal(got, set.ids[i]) {
					same++
					if model == "gte" && set.source == "cases" {
						res.Single[text] = verdict{Same: true}
					}
					continue
				}
				v := judgeDiff(cats, model, []string{text}, set.expl[i], func(rest []string) ([]int, error) { return tk.EncodeSingle(rest[0], maxTok) })
				if v.Bug != "" {
					bugs++
				}
				if model == "gte" && set.source == "cases" {
					res.Single[text] = v
				}
				d := knownDiff{Model: model, Source: set.source, Index: i, Go: got, Py: set.ids[i]}
				found[d.key()] = d
			}
		}
		if model == "bge" {
			p, c, s, b, err := tokenPairsCommitted(src, tks, cats, ref, found, res)
			if err != nil {
				return err
			}
			compared, same, skipped, bugs = compared+c, same+p, skipped+s, bugs+b
		}
		diffs := compared - same
		res.Rows = append(res.Rows, Row{Stage: "分词", What: fmt.Sprintf("编的样本 %s：Go 的 id 对 Python 的", model),
			Value: fmt.Sprintf("%d/%d 逐个相同，登记的已解释差异 %d，未解释 %d（Python 抛错跳过 %d）", same, compared, diffs-bugs, bugs, skipped),
			Gate:  "未解释 0", Verdict: verdictOf(bugs == 0)})
	}
	// 差异的集合要和 known-diffs.json 一模一样：多一条、少一条、id 变了都不行
	wantKeys := map[string]knownDiff{}
	for _, d := range known.Diffs {
		wantKeys[d.key()] = d
	}
	var mismatch []string
	for k, d := range found {
		w, ok := wantKeys[k]
		switch {
		case !ok:
			mismatch = append(mismatch, "多了 "+k)
		case !slices.Equal(w.Go, d.Go) || !slices.Equal(w.Py, d.Py):
			mismatch = append(mismatch, "id 变了 "+k)
		}
	}
	for k := range wantKeys {
		if _, ok := found[k]; !ok {
			mismatch = append(mismatch, "少了 "+k)
		}
	}
	slices.Sort(mismatch)
	res.Rows = append(res.Rows, Row{Stage: "分词", What: "编的样本：差异的集合 = known-diffs.json 登记的",
		Value: fmt.Sprintf("实际 %d 条，登记 %d 条%s", len(found), len(wantKeys), listOrEmpty(mismatch)), Gate: "完全一样", Verdict: verdictOf(len(mismatch) == 0)})
	return nil
}

func listOrEmpty(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return "；" + strings.Join(items[:min(len(items), 8)], "、")
}

func tokenPairsCommitted(src sourceFiles, tks *tokenizers, cats map[string]tokenizer.Category, _ refIDs, found map[string]knownDiff, res *tokenResult) (same, compared, skipped, bugs int, err error) {
	pairs, err := src.pairs()
	if err != nil {
		return
	}
	var ref struct {
		Pairs []struct {
			Name        string        `json:"name"`
			QueryTokens int           `json:"query_tokens"`
			IDs         []int         `json:"ids"`
			Error       string        `json:"error"`
			Explained   *explainedRef `json:"explained"`
		} `json:"pairs"`
	}
	if err = readJSON(src.path("tokenizer", "testdata", "ref-pairs-bge.json"), &ref); err != nil {
		return
	}
	if len(ref.Pairs) != len(pairs) {
		err = fmt.Errorf("pairs.json 有 %d 对，参考数据有 %d 对：改过就要重新跑 parity_ref.py", len(pairs), len(ref.Pairs))
		return
	}
	maxTok := tks.bgeSp.MaxTokens
	for i, pe := range pairs {
		r := ref.Pairs[i]
		q, d := pe.Q.expand(), pe.D.expand()
		key := [2]string{q, d}
		got, gerr := tks.bge.EncodePair(q, d, maxTok)
		if r.QueryTokens > maxTok-tokenizer.RerankSpecialReserve { // 查询超预算：两边都该拒绝
			skipped++
			var qe *tokenizer.QueryTooLongError
			if !errors.As(gerr, &qe) {
				bugs++
				res.Pair[key] = verdict{Bug: "查询超出预算，Go 却没报 QueryTooLongError"}
			} else {
				res.Pair[key] = verdict{Same: true}
			}
			continue
		}
		if r.IDs == nil { // Python 抛了（单条超 1024 之类）
			skipped++
			continue
		}
		compared++
		if gerr != nil {
			bugs++
			res.Pair[key] = verdict{Bug: "Go 报错：" + gerr.Error()}
			continue
		}
		if slices.Equal(got, r.IDs) {
			same++
			res.Pair[key] = verdict{Same: true}
			continue
		}
		v := judgeDiff(cats, "bge", []string{q, d}, r.Explained, func(rest []string) ([]int, error) { return tks.bge.EncodePair(rest[0], rest[1], maxTok) })
		if v.Bug != "" {
			bugs++
		}
		res.Pair[key] = v
		kd := knownDiff{Model: "bge", Source: "pairs", Index: i, Go: got, Py: r.IDs}
		found[kd.key()] = kd
	}
	// compared 里已经含了不一致的；same 单独数
	return same, compared, skipped, bugs, nil
}

// parityRefReal 用 parity_ref.py real 导出一份 embedinputs 文件的参照 id（Python 的），返回解析好的 ref-real.json。
// 输入和输出都在 workDir（临时目录）里，用完由调用方删。python 没给就没法做，返回 nil 不是错。
func parityRefReal(o *options, workDir string, in embedinputs.File) (*refReal, error) {
	if o.python == "" || o.parityRef == "" {
		return nil, nil
	}
	inPath, outDir := filepath.Join(workDir, "tok-in.json"), filepath.Join(workDir, "tok-out")
	if err := embedinputs.Write(inPath, in); err != nil {
		return nil, err
	}
	cmd := exec.Command(o.python, "-I", "-B", "-X", "utf8", o.parityRef, "real", "--models-root", o.modelsRoot, "--input", inPath, "--out", outDir)
	if o.categories != "" {
		cmd.Args = append(cmd.Args, "--categories", o.categories)
	}
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("parity_ref.py real 失败：%v\n%s", err, tailString(out.String(), 600))
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "ref-real.json"))
	if err != nil {
		return nil, err
	}
	var rr refReal
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, fmt.Errorf("解析 ref-real.json：%w", err)
	}
	return &rr, nil
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

type refReal struct {
	Lists map[string]struct {
		IDs       [][]int         `json:"ids"`
		Explained []*explainedRef `json:"explained"`
	} `json:"lists"`
	Pairs []struct {
		QueryTokens int           `json:"query_tokens"`
		Docs        []refPairCell `json:"docs"`
	} `json:"pairs"`
}

// tokenStageLive：编的语料和真实样本，对现场导出的 Python id。list 名和 embedinputs.File 的字段一致。
// texts 是按 list 名排好的文字，groups 是重排的（查询，候选）。返回每个来源的汇总行。
func tokenStageLive(tks *tokenizers, cats map[string]tokenizer.Category, rr *refReal, lists map[string][]string, groups []groupSample, res *tokenResult, real bool) {
	type tally struct{ n, same, explained, bugs, skipped, fixable, intended int }
	count := func(t *tally, v verdict) {
		t.explained++
		if v.fixable() {
			t.fixable++
		} else {
			t.intended++
		}
	}
	byList := map[string]*tally{}
	names := make([]string, 0, len(lists))
	for n := range lists {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, name := range names {
		t := &tally{}
		byList[name] = t
		ref, ok := rr.Lists[name]
		if !ok || len(ref.IDs) != len(lists[name]) {
			t.bugs++
			continue
		}
		for i, text := range lists[name] {
			if ref.IDs[i] == nil {
				t.skipped++
				continue
			}
			t.n++
			got, err := tks.gte.EncodeSingle(text, tks.gteSpec.MaxTokens)
			if err != nil {
				t.bugs++
				res.Single[text] = verdict{Bug: "Go 报错"}
				continue
			}
			if slices.Equal(got, ref.IDs[i]) {
				t.same++
				res.Single[text] = verdict{Same: true}
				continue
			}
			v := judgeDiff(cats, "gte", []string{text}, ref.Explained[i], func(rest []string) ([]int, error) { return tks.gte.EncodeSingle(rest[0], tks.gteSpec.MaxTokens) })
			res.Single[text] = v
			if v.Bug != "" {
				t.bugs++
			} else {
				count(t, v)
			}
		}
	}
	pt := &tally{}
	if len(rr.Pairs) == len(groups) {
		for gi, g := range groups {
			row := rr.Pairs[gi]
			if len(row.Docs) != len(g.Docs) {
				pt.bugs++
				continue
			}
			for di, d := range g.Docs {
				cell := row.Docs[di]
				key := [2]string{g.Q, d}
				if cell.IDs == nil {
					pt.skipped++
					continue
				}
				pt.n++
				got, err := tks.bge.EncodePair(g.Q, d, tks.bgeSp.MaxTokens)
				if err != nil {
					pt.bugs++
					res.Pair[key] = verdict{Bug: "Go 报错"}
					continue
				}
				if slices.Equal(got, cell.IDs) {
					pt.same++
					res.Pair[key] = verdict{Same: true}
					continue
				}
				v := judgeDiff(cats, "bge", []string{g.Q, d}, cell.Explained, func(rest []string) ([]int, error) { return tks.bge.EncodePair(rest[0], rest[1], tks.bgeSp.MaxTokens) })
				res.Pair[key] = v
				if v.Bug != "" {
					pt.bugs++
				} else {
					count(pt, v)
				}
			}
		}
	} else {
		pt.bugs++
	}
	for _, name := range names {
		t := byList[name]
		res.Rows = append(res.Rows, Row{Stage: "分词", What: "现场导出的 Python id：" + name,
			Value: fmt.Sprintf("%d/%d 逐个相同，已解释的差异 %d，未解释 %d（跳过 %d）", t.same, t.n, t.explained, t.bugs, t.skipped), Gate: "未解释 0", Verdict: verdictOf(t.bugs == 0)})
	}
	total, fixable, intended := pt.n, pt.fixable, pt.intended
	for _, t := range byList {
		total, fixable, intended = total+t.n, fixable+t.fixable, intended+t.intended
	}
	res.Rows = append(res.Rows, differenceShareRow(total, fixable, intended, real))
	res.Rows = append(res.Rows, Row{Stage: "分词", What: "现场导出的 Python id：重排的（查询，候选）对",
		Value: fmt.Sprintf("%d/%d 逐个相同，已解释的差异 %d，未解释 %d（跳过 %d）", pt.same, pt.n, pt.explained, pt.bugs, pt.skipped), Gate: "未解释 0", Verdict: verdictOf(pt.bugs == 0)})
}

// shareGate 是规格第五节"分词 id，真实样本"一行的起始值：已解释的差异（不含 S1）占该批条数的比例不超过 1%，
// 超过就要停下来决定做 T5 的字素簇切分（UAX #29，规格 2.4）。
const shareGate = 0.01

// differenceShareRow：已解释的差异占比这一行。total 是比较过的条数（单句加重排的对），fixable 是类别不止 S1 的已解释差异，
// intended 是只属 S1 的（有意不同，单列，不进分子）。real 为假（只有编的语料，T1、T5、S1 是特意放得很密的）只记录，不判；
// 为真时超过 1% 是"待定"：不是不过，是该停下来由主人定要不要做切分。
func differenceShareRow(total, fixable, intended int, real bool) Row {
	what := "已解释差异（T1、T5 等，不含有意不同的 S1）占分词条数的比例"
	if total == 0 {
		return Row{Stage: "分词", What: what, Gate: "≤ 1%", Value: "0 条可比", Verdict: vThin}
	}
	share := float64(fixable) / float64(total)
	value := fmt.Sprintf("%d/%d = %.2f%%；S1 另有 %d 条（有意不同，不进分子）", fixable, total, share*100, intended)
	row := Row{Stage: "分词", What: what, Gate: "≤ 1%（超过要停下来决定做 T5 的字素簇切分）", Value: value}
	switch {
	case !real:
		row.Verdict, row.Value = vInfo, value+"；这一批是编的语料，这些字符特意放得很密，只记录"
	case share > shareGate:
		row.Verdict = vWait
	default:
		row.Verdict = vPass
	}
	return row
}
