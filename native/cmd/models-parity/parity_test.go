package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sumika-native/internal/embed"
	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/memory"
)

const testdataRoot = "../../internal/localmodels"

func testPlan(t *testing.T, limit int) *plan {
	t.Helper()
	pl, err := buildPlan(sourceFiles{root: testdataRoot}, fakeCounter{}, engine.BuiltinSpecs()[0], limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

func rowsBy(rows []Row, stage, what string) []Row {
	var out []Row
	for _, r := range rows {
		if r.Stage == stage && strings.Contains(r.What, what) {
			out = append(out, r)
		}
	}
	return out
}

func verdictsOf(rows []Row) map[string]int {
	m := map[string]int{}
	for _, r := range rows {
		m[r.Verdict]++
	}
	return m
}

// 规格 GM6 验收 1：把同一份服务当两边 → 全绿，翻转数 0（"没量够"不算红，假数据里门槛附近的对本来就不到 50 对，命令要照实说）。
func TestParityPassesWhenIdentical(t *testing.T) {
	a := runFakes(t, &fakeService{t: t}, &fakeService{t: t}, 2)
	rows := a.all()
	for _, r := range rows {
		if r.Verdict == vFail && r.Stage != "速度" && r.Stage != "内存" { // 假服务没有真实的耗时和日志，速度、内存两段不看
			t.Errorf("同一份服务当两边，却有一行不过：%s\n  %s", r.What, r.Value)
		}
	}
	if got := rowsBy(rows, "嵌入", "第二层"); len(got) != 1 || got[0].Verdict != vPass || !strings.Contains(got[0].Value, "最大分量差 0.000e+00") {
		t.Errorf("嵌入第二层该过、差为 0：%+v", got)
	}
	sims := rowsBy(rows, "相似度", "")
	if len(sims) != 6 { // 编的语料 × 3 个门槛 × 2 列
		t.Fatalf("相似度该有 6 行，得到 %d", len(sims))
	}
	for _, r := range sims {
		if !strings.Contains(r.Value, "D = 0.000e+00") || !strings.Contains(r.Value, "翻转 0 个") {
			t.Errorf("同一份服务的相似度差和翻转都该是 0：%s\n  %s", r.What, r.Value)
		}
	}
	if got := rowsBy(rows, "噪声底", "Python 自对拍"); len(got) != 1 || got[0].Verdict != vPass || !strings.Contains(got[0].Value, "N = 0.000e+00") {
		t.Errorf("噪声底该是 0：%+v", got)
	}
	if got := rowsBy(rows, "状态码", ""); len(got) != 1 || got[0].Verdict != vPass {
		t.Errorf("状态码该全一致：%+v", got)
	}
	if got := rowsBy(rows, "重排", "排序"); len(got) != 1 || got[0].Verdict != vPass {
		t.Errorf("排序该全相同：%+v", got)
	}
}

// 命令得会红：向量差、分数差、名次倒过来、一边请求失败，各让它红一次。差在门槛之内的（1e-8）要仍然绿，免得门槛卡得过严。
func TestParityDetectsDifferences(t *testing.T) {
	type tc struct {
		name     string
		goSvc    *fakeService
		stage    string
		what     string
		want     string
		wantRows int
	}
	cases := []tc{
		{"向量差 1e-3", &fakeService{vecNoise: 1e-3}, "嵌入", "第二层", vFail, 1},
		{"向量差 1e-8（在门槛内）", &fakeService{vecNoise: 1e-8}, "嵌入", "第二层", vPass, 1},
		{"重排分数差 1e-4", &fakeService{scoreNoise: 1e-4}, "重排", "sigmoid", vFail, 1},
		{"重排分数差 1e-8（在门槛内）", &fakeService{scoreNoise: 1e-8}, "重排", "sigmoid", vPass, 1},
		{"名次倒过来", &fakeService{reverse: true}, "重排", "排序", vFail, 1},
		{"一边的 64 条一批回 500", &fakeService{failBatch: true}, "嵌入", "一边没出向量", vFail, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.goSvc.t = t
			a := runFakes(t, c.goSvc, &fakeService{t: t}, 1)
			got := rowsBy(a.all(), c.stage, c.what)
			if len(got) != c.wantRows || got[0].Verdict != c.want {
				t.Fatalf("该是 %s：%+v", c.want, got)
			}
		})
	}
	// 向量差 1e-3 时相似度的 D 也要红
	a := runFakes(t, &fakeService{t: t, vecNoise: 1e-2}, &fakeService{t: t}, 1)
	for _, r := range rowsBy(a.all(), "相似度", "") {
		if r.Verdict != vFail {
			t.Errorf("向量差很大时相似度该不过：%s\n  %s", r.What, r.Value)
		}
	}
}

// 噪声底：Python 自对拍要有两遍以上才算；N 超过门槛本身就是一行红（门槛低于噪声底就没有意义）。
func TestNoiseFloorRows(t *testing.T) {
	pl := testPlan(t, 2048)
	mk := func(noise float32) *sideRun {
		r, err := collect(context.Background(), fakeProc(t, "python", &fakeService{t: t, vecNoise: noise}), pl, 1)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a := &analysis{pl: pl, tok: &tokenResult{Single: map[string]verdict{}, Pair: map[[2]string]verdict{}}, pyRuns: []*sideRun{mk(0)}}
	if r := rowsBy(a.noiseRows(), "噪声底", "Python"); len(r) != 1 || r[0].Verdict != vNotRun {
		t.Errorf("只有一遍该是没跑：%+v", r)
	}
	a.pyRuns = append(a.pyRuns, mk(1e-3))
	if r := rowsBy(a.noiseRows(), "噪声底", "Python"); len(r) != 1 || r[0].Verdict != vFail {
		t.Errorf("两遍之间差 1e-3，噪声底超过门槛，该不过：%+v", r)
	}
}

// 空白输入的状态码也要比：两边一个 400 一个 200 就是一行红。
func TestStatusMismatchIsReported(t *testing.T) {
	a := runFakes(t, &fakeService{t: t}, &fakeService{t: t}, 1)
	a.goRuns[0].Zoo[3] = 200
	if r := rowsBy(a.statusRows(), "状态码", ""); len(r) != 1 || r[0].Verdict != vFail || !strings.Contains(r[0].Value, "zoo#3") {
		t.Errorf("该报 zoo#3 状态码不一致：%+v", r)
	}
}

// 输出里不能有样本文字：把一遍的结果和报告行全序列化，找清单里每一段（≥ 8 个字）的文字。非 2xx 的响应体（假服务会回显输入）也不能漏进来。
func TestNoSampleTextInOutput(t *testing.T) {
	// 两种跑法：一份没问题的，和一份处处出问题的（一边 64 条一批回 500、向量和分数都有偏差、名次倒过来）——出错时列出来的明细里也不能有文字
	for _, c := range []struct {
		name  string
		goSvc *fakeService
	}{{"没问题", &fakeService{t: t}}, {"处处出问题", &fakeService{t: t, failBatch: true, vecNoise: 1e-2, scoreNoise: 1e-2, reverse: true}}} {
		t.Run(c.name, func(t *testing.T) {
			a := runFakes(t, c.goSvc, &fakeService{t: t}, 1)
			rep := newReport("run", map[string]any{})
			rep.Rows = a.all()
			rep.finish()
			raw, _ := json.Marshal(rep)
			runRaw, _ := json.Marshal(a.goRuns[0])
			var sb strings.Builder
			rep.print(&sb)
			out := string(raw) + string(runRaw) + sb.String()
			if c.name == "处处出问题" && rep.Summary[vFail] < 5 {
				t.Fatalf("处处出问题的一份该有好几行不过，只有 %d 行", rep.Summary[vFail])
			}
			checked := 0
			for _, s := range a.pl.Texts {
				if len([]rune(s.Text)) >= 8 && len([]rune(s.Text)) < 200 && !strings.Contains(s.Text, `"`) && !strings.Contains(s.Text, `\`) {
					checked++
					if strings.Contains(out, s.Text) {
						t.Fatalf("输出里出现了样本文字：%s", s.label())
					}
				}
			}
			for _, g := range a.pl.Groups {
				if strings.Contains(out, g.Q) {
					t.Fatalf("输出里出现了重排的查询：%s#%d", g.Set, g.Idx)
				}
			}
			if checked < 300 {
				t.Fatalf("只查了 %d 段文字，太少", checked)
			}
		})
	}
}

// 客户端：非 2xx 的响应体丢掉，只留状态码（FastAPI 的 422 会把输入原样回显）。
func TestClientDropsErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"detail":[{"input":"SECRET-TEXT"}]}`))
	}))
	defer srv.Close()
	c := newClient(srv.URL, "t")
	res, err := c.embed(context.Background(), gteID, []string{"SECRET-TEXT"}, typeDoc)
	if err != nil || res.Status != 422 || res.Vecs != nil {
		t.Fatalf("该只留状态码 422：%+v %v", res, err)
	}
	status, raw, err := c.call(context.Background(), http.MethodPost, "/x", map[string]any{})
	if err != nil || status != 422 || raw != nil {
		t.Fatalf("call 该丢掉响应体：%d %q %v", status, raw, err)
	}
	if rr, err := c.rerank(context.Background(), bgeID, "q", []string{"d"}); err != nil || rr.Status != 422 || rr.Scores != nil {
		t.Fatalf("重排也一样：%+v %v", rr, err)
	}
}

func TestSamplePlan(t *testing.T) {
	pl := testPlan(t, 2048)
	c := pl.counts()
	for k, want := range map[string]int{setCases: 86, setCorpusItems: 326, setCorpusQ: 100, "pairs": 16, "groups": 10, "zoo-alone": 29} {
		if c[k] != want {
			t.Errorf("%s 该有 %d 个，得到 %d", k, want, c[k])
		}
	}
	if n := len(pl.set(setCorpusSent)); n < 326 { // 单句条目的守卫句子就是条目自己：同一段文字送一次，但两个角色都要有
		t.Errorf("守卫的句子该不少于条目数（单句条目的句子就是它自己）：%d", n)
	}
	// 超过单行上限的（5000 个字的长词等）两边都不送；空白的只比状态码
	var over, blank []string
	for _, s := range pl.Texts {
		switch s.Skip {
		case skipOverLimit:
			over = append(over, s.label())
			if s.Tokens != 0 && s.Tokens <= 2048 {
				t.Errorf("%s：只有 %d 个 token 却被跳过", s.label(), s.Tokens)
			}
		case skipBlank:
			blank = append(blank, s.label())
		default:
			if s.Tokens > 2048 {
				t.Errorf("%s：%d 个 token 超过上限却没跳过", s.label(), s.Tokens)
			}
		}
	}
	if len(over) < 4 || len(blank) < 3 {
		t.Errorf("该有几条超长的和几条空白的：over=%v blank=%v", over, blank)
	}
	// 清单的哈希：同样的输入同样的哈希，行长上限变了就不同（-ref-in 的参照因此对不上）
	if testPlan(t, 2048).Hash != pl.Hash || testPlan(t, 1000).Hash == pl.Hash {
		t.Error("清单哈希该只随样本和行长变")
	}
	// 同一段文字只记一次，先到的来源算数
	seen := map[string]bool{}
	for _, s := range pl.Texts {
		if seen[s.Text] {
			t.Errorf("重复的样本：%s", s.label())
		}
		seen[s.Text] = true
	}
	shorts := 0
	for _, s := range pl.Texts {
		if s.Short {
			shorts++
			if s.Set != setCases || s.Tokens > 64 {
				t.Errorf("单句计时的样本不对：%s %d", s.label(), s.Tokens)
			}
		}
	}
	if shorts < 30 {
		t.Errorf("单句计时的样本只有 %d 个", shorts)
	}
}

func TestSideRunJSONRoundTrip(t *testing.T) {
	a := runFakes(t, &fakeService{t: t}, &fakeService{t: t}, 1)
	raw, err := json.Marshal(refFile{Version: 1, PlanHash: a.pl.Hash, Python: a.pyRuns})
	if err != nil {
		t.Fatal(err)
	}
	var back refFile
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Python) != 1 || len(back.Python[0].Embed) != len(a.pyRuns[0].Embed) {
		t.Fatal("读回来的条数不对")
	}
	for i, e := range a.pyRuns[0].Embed {
		if !slices.Equal([]float32(e.Vec), []float32(back.Python[0].Embed[i].Vec)) || e.Status != back.Python[0].Embed[i].Status {
			t.Fatalf("样本 %d 读回来不一样", i)
		}
	}
	// 用读回来的参照再分析一遍，结果和直接分析一样
	b := &analysis{pl: a.pl, tok: a.tok, goRuns: a.goRuns, pyRuns: back.Python}
	x, y := a.embedRows(), b.embedRows()
	if len(x) != len(y) || x[0].Value != y[0].Value {
		t.Errorf("用读回来的参照分析结果不同：%q 对 %q", x[0].Value, y[0].Value)
	}
}

func TestSimRowVerdicts(t *testing.T) {
	ok := FlipReport{Name: "守卫", Threshold: 0.7, Pairs: 100, Near: 80, Diff: Dist{Max: 1e-7}}
	if r := simRow("s", "c", ok); r.Verdict != vPass {
		t.Errorf("该过：%+v", r)
	}
	thin := ok
	thin.Near, thin.NotEnough = 3, true
	if r := simRow("s", "c", thin); r.Verdict != vThin || !strings.Contains(r.Value, "没量够") {
		t.Errorf("门槛附近不到 50 对该是没量够：%+v", r)
	}
	big := ok
	big.Diff.Max = 2e-5
	if r := simRow("s", "c", big); r.Verdict != vFail {
		t.Errorf("D 超过 1e-5 该不过：%+v", r)
	}
	wrong := ok
	wrong.WrongFlip = 1
	if r := simRow("s", "c", wrong); r.Verdict != vFail {
		t.Errorf("翻转的距离超过 D（比较函数写错了）该不过：%+v", r)
	}
	bigAndThin := big
	bigAndThin.NotEnough = true
	if r := simRow("s", "c", bigAndThin); r.Verdict != vFail {
		t.Errorf("不过比没量够优先：%+v", r)
	}
}

func TestReportExitCode(t *testing.T) {
	mk := func(vs ...string) *report {
		r := &report{}
		for _, v := range vs {
			r.Rows = append(r.Rows, Row{Verdict: v})
		}
		r.finish()
		return r
	}
	for _, c := range []struct {
		rows []string
		code int
	}{
		{[]string{vPass, vInfo, vNotRun}, 0},
		{[]string{vPass, vThin}, 3},
		{[]string{vPass, vWait}, 3}, // 待定也不能当通过
		{[]string{vInfo, vNotRun}, 0},
		{[]string{vPass, vFail}, 1},
		{[]string{vThin, vFail}, 1},
	} {
		if got := mk(c.rows...).exitCode(); got != c.code {
			t.Errorf("%v：退出码 %d，该是 %d", c.rows, got, c.code)
		}
	}
}

func TestCompareRegistry(t *testing.T) {
	var ref struct {
		Models map[string]regEntry `json:"models"`
	}
	if err := readJSON(filepath.Join(testdataRoot, "engine", "testdata", "ref-registry.json"), &ref); err != nil {
		t.Fatal(err)
	}
	n, bad := compareRegistry(engine.BuiltinSpecs(), ref.Models)
	if len(bad) != 0 || n != 28 {
		t.Fatalf("Go 的登记和 Python 导出的参照该完全一致（2 个模型 × 14 个字段），得到 %d 个字段，对不上 %v", n, bad)
	}
	// 改坏参照里的一个字段、一个模型，必须报出来
	m := ref.Models[gteID]
	m.MaxTokens, m.QueryPrefix = 4096, "query: "
	ref.Models[gteID] = m
	delete(ref.Models, bgeID)
	_, bad = compareRegistry(engine.BuiltinSpecs(), ref.Models)
	want := []string{bgeID + "：Python 的表里没有", gteID + ".max_tokens", gteID + ".query_prefix"}
	slices.Sort(want)
	if !slices.Equal(bad, want) {
		t.Errorf("该报出 %v，得到 %v", want, bad)
	}
}

func TestParseLoadedAndLineBuf(t *testing.T) {
	b := &lineBuf{}
	b.Write([]byte("2026/11/02 06:00:00 LOADED model=gte-multilingual-base kind=embed dimension=768 load_ms=1234 hash_ms=600 estimated_mb=1\nEMBED model=x inputs=1\nLOADED model=bge-rer"))
	b.Write([]byte("anker kind=rerank dimension=- load_ms=2000 hash_ms=1100 estimated_mb=2\r\n"))
	got := parseLoaded(b.snapshot())
	if got["gte-multilingual-base"] != (loadInfo{1234, 600}) || got["bge-reranker"] != (loadInfo{2000, 1100}) || len(got) != 2 {
		t.Errorf("解析不对：%+v", got)
	}
	// UNLOADED 那一行里也有 "LOADED model=" 这几个字，不能把真的 LOADED 数覆盖成 0（final 第一次跑就栽在这）
	b.Write([]byte("2026/11/02 06:00:09 UNLOADED model=gte-multilingual-base\n"))
	got = parseLoaded(b.snapshot())
	if got["gte-multilingual-base"] != (loadInfo{1234, 600}) {
		t.Errorf("UNLOADED 行把 LOADED 的数覆盖了：%+v", got)
	}
	if n := len(b.snapshot()); n != 4 {
		t.Errorf("该有 4 行（半行接上了），得到 %d", n)
	}
}

func TestParseOptions(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	os.WriteFile(file, []byte("x"), 0o600)
	base := []string{"-models-root", dir, "-go-exe", file, "-ort-library", file, "-testdata", dir}
	if _, err := parseOptions("run", base); err != nil {
		t.Fatalf("最少的参数该能过：%v", err)
	}
	for name, args := range map[string][]string{
		"缺 models-root":      {"-go-exe", file, "-ort-library", file},
		"python 没配 server":   append(slices.Clone(base), "-python", "python"),
		"python 和 ref-in 同时": append(slices.Clone(base), "-python", "python", "-server", file, "-ref-in", file),
		"passes 为负":          append(slices.Clone(base), "-passes", "-2"),
		"文件不存在":              {"-models-root", dir, "-go-exe", filepath.Join(dir, "none"), "-ort-library", file},
	} {
		if _, err := parseOptions("run", args); err == nil {
			t.Errorf("%s：该报错", name)
		}
	}
	o, err := parseOptions("run", append(slices.Clone(base), "-ref-in", file))
	if err != nil || o.passes != 1 {
		t.Errorf("-ref-in 时缺省 1 遍：%+v %v", o, err)
	}
	o, _ = parseOptions("run", append(slices.Clone(base), "-long"))
	if o.lineLimit != 8192 {
		t.Errorf("-long 时行长是登记表的 8192：%d", o.lineLimit)
	}
}

// 登记的分词差异（T1、T5、S1）的向量余弦：≥ 0.95 通过；低于下限的要在 pinned-cosines.json 里锁住，锁住且没漂是"待定"（不是通过），
// 漂了（分词或引擎哪里变了）或者根本没登记是"不过"。
func TestRegisteredDiffVerdicts(t *testing.T) {
	a := runFakes(t, &fakeService{t: t}, &fakeService{t: t}, 1)
	find := func(set string, idx int) int {
		for i, s := range a.pl.Texts {
			if s.Set == set && s.Idx == idx && s.Skip == "" {
				return i
			}
		}
		t.Fatalf("清单里没有 %s#%d", set, idx)
		return -1
	}
	// 造一个和 py 向量余弦恰好是 cos 的 Go 向量：cos·p + sin·q，q 是和 p 垂直的单位向量
	withCos := func(i int, cos float64) {
		p := embedUnit(a.pyRuns[0].Embed[i].Vec)
		q := embedUnit(a.pyRuns[0].Embed[find(setCorpusItems, 0)].Vec)
		var dot float64
		for k := range p {
			dot += float64(p[k]) * float64(q[k])
		}
		perp := make([]float64, len(p))
		var n float64
		for k := range p {
			perp[k] = float64(q[k]) - dot*float64(p[k])
			n += perp[k] * perp[k]
		}
		v := make([]float32, len(p))
		for k := range p {
			v[k] = float32(cos*float64(p[k]) + math.Sqrt(1-cos*cos)*perp[k]/math.Sqrt(n))
		}
		a.goRuns[0].Embed[i].Vec = v
		for _, s := range []string{a.pl.Texts[i].Text} {
			a.tok.Single[s] = verdict{Cats: []string{"T1"}}
		}
	}
	row := func() Row {
		got := rowsBy(a.embedRows(), "嵌入", "id 不同的登记差异（T1、T5、S1）")
		if len(got) != 1 {
			t.Fatalf("该有一行：%+v", got)
		}
		return got[0]
	}
	// 锁住的（cases#22 = 0.8316）：值没漂 → 待定
	i22 := find(setCases, 22)
	withCos(i22, 0.8316)
	if r := row(); r.Verdict != vWait {
		t.Errorf("低于 0.95、锁住、没漂该是待定：%+v", r)
	}
	// 漂了
	withCos(i22, 0.8316+2e-3)
	if r := row(); r.Verdict != vFail || !strings.Contains(r.Value, "锁的值漂了 1 个") {
		t.Errorf("锁的值漂了该不过：%+v", r)
	}
	// 没登记锁的样本，余弦低于 0.95
	withCos(i22, 0.8316)
	i5 := find(setCases, 5)
	withCos(i5, 0.90)
	if r := row(); r.Verdict != vFail || !strings.Contains(r.Value, "又没登记锁 1 个") {
		t.Errorf("低于下限又没登记锁该不过：%+v", r)
	}
	// 不低于下限的，不管有没有锁，都不拦
	withCos(i5, 0.97)
	if r := row(); r.Verdict != vWait { // i22 仍然是待定
		t.Errorf("0.97 不该拦：%+v", r)
	}
	a.goRuns[0].Embed[i22] = a.pyRuns[0].Embed[i22] // 把 i22 也改回和 py 一样（分词也一样了），登记差异里就只剩 0.97 的
	delete(a.tok.Single, a.pl.Texts[i22].Text)
	if r := row(); r.Verdict == vFail {
		t.Errorf("都 ≥ 0.95 不该不过：%+v", r)
	}
	if got := regVerdict(false, false); got != vPass {
		t.Errorf("regVerdict(false,false) = %s", got)
	}
}

func embedUnit(v []float32) []float32 { return embed.Unit(v) }

// 三个门槛的声明和生产的比较函数要对得上：函数在 float32(有效门槛) 上判命中、在它低一格上判没命中；有效门槛：守卫是 float32(0.70) 的值，
// 不是 0.70（"到门槛的距离"用它算，不然恰好跨在 float32(0.70) 两侧的一对会被冤枉）。
func TestSimThresholdsMatchProduction(t *testing.T) {
	for _, th := range []simThreshold{simRecall, simGuard, simDup} {
		at := float32(th.eff)
		if !th.reaches(at) || th.reaches(math.Nextafter32(at, 0)) {
			t.Errorf("%s：比较函数的边界不在有效门槛 %v 上", th.name, th.eff)
		}
	}
	if simGuard.eff != float64(float32(memory.GuardThreshold)) || simRecall.eff != memory.SimilarityFloor || simDup.eff != memory.DupSimilarity {
		t.Errorf("有效门槛该是：守卫 float64(float32(0.70))、召回和近重复就是常量本身：%v %v %v", simGuard.eff, simRecall.eff, simDup.eff)
	}
}

// 起过的服务收尾时还在的 PID 要报出来：拿测试进程自己当"杀不掉的残留"（done 已经关了，stop 什么也不做，不会真的去杀它）。
func TestStopAllReportsResidue(t *testing.T) {
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	p := &serverProc{l: launch{Engine: "go"}, cmd: &exec.Cmd{Process: self}, done: done, log: &lineBuf{}}
	procMu.Lock()
	started = append(started, p)
	procMu.Unlock()
	defer func() {
		procMu.Lock()
		started = slices.DeleteFunc(started, func(x *serverProc) bool { return x == p })
		procMu.Unlock()
	}()
	_, residue := stopAll()
	if !slices.Contains(residue, os.Getpid()) {
		t.Errorf("还活着的进程该出现在残留里：%v", residue)
	}
}

// 规格第五节"分词 id，真实样本"一行：已解释的差异（不含有意不同的 S1）占该批条数不超过 1%，超过就停下来决定做不做 T5 的字素簇切分。
// 原来只有"未解释 0"，5% 的样本含 T1、T5 字符时分词那几行照样打"通过"，没有程序在算这个比例。
// 改坏检查：门槛改成 0.1 或把 S1 也算进分子，下面对应的格会红
func TestDifferenceShareRow(t *testing.T) {
	for _, c := range []struct {
		name                     string
		total, fixable, intended int
		real                     bool
		want                     string
		valueHas                 string
	}{
		{"刚好 1% 通过", 1000, 10, 0, true, vPass, "10/1000 = 1.00%"},
		{"超过 1% 待定", 1000, 11, 0, true, vWait, "1.10%"},
		{"S1 不进分子", 1000, 0, 400, true, vPass, "S1 另有 400 条"},
		{"一条都没有", 1000, 0, 0, true, vPass, "0/1000 = 0.00%"},
		{"编的语料只记录，不判", 100, 30, 10, false, vInfo, "只记录"},
		{"没有可比的条数是没量够，不是通过", 0, 0, 0, true, vThin, "0 条可比"},
	} {
		row := differenceShareRow(c.total, c.fixable, c.intended, c.real)
		if row.Verdict != c.want || !strings.Contains(row.Value, c.valueHas) {
			t.Errorf("%s：得到 %q %q，想要 %q 含 %q", c.name, row.Verdict, row.Value, c.want, c.valueHas)
		}
	}
	for _, c := range []struct {
		cats []string
		want bool
	}{{[]string{"S1"}, false}, {[]string{"T5"}, true}, {[]string{"S1", "T1"}, true}, {nil, false}} {
		if got := (verdict{Cats: c.cats}).fixable(); got != c.want {
			t.Errorf("verdict{Cats: %v}.fixable() = %v，想要 %v", c.cats, got, c.want)
		}
	}
}
