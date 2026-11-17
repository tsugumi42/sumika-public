package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/embed"
	"sumika-native/internal/embed/embedtest"
)

// 本包的测试进程也能当假的 sumika-models.exe 起来（embedtest）：-engine go 走的是真的 embed.Local
func TestMain(m *testing.M) {
	if embedtest.Wanted() {
		embedtest.Run()
		return
	}
	os.Exit(m.Run())
}

// 编的小数据集：三条主人的私事（范围 owner），样本在 QQ 群里说话，禁看的就是这三条
func smallDataset() Dataset {
	ds := Dataset{Character: "编的角色"}
	for i, text := range []string{"阿青周四下午要去蓝纸诊所复查牙齿", "阿青把备用电池收在蓝柜底层", "阿青养的猫叫团子，最怕打雷"} {
		ds.Memories = append(ds.Memories, Memory{ID: fmt.Sprintf("m%d", i+1), Kind: "fact", Status: "active", Scope: []string{"owner"}, Text: text})
	}
	group := audience.Audience{Channel: "qq", Conversation: "qq:group:1", Scene: "group"}
	ds.Samples = []Sample{
		{ID: "s1", Category: "verbatim", Ref: "m1", Audience: group, Parts: []string{"阿青周四下午要去蓝纸诊所复查牙齿"}, ShouldBlock: true},
		{ID: "s2", Category: "paraphrase", Ref: "m2", Audience: group, Parts: []string{"他留着那几节电池", "放在蓝色的柜子下面"}, ShouldBlock: true},
		{ID: "s3", Category: "chitchat", Ref: "m3", Audience: group, Parts: []string{"今天天气不错"}},
	}
	return ds
}

// keywordEmbed 是编的嵌入器：向量只看关键词（电池、猫、诊所，其余算一个方向），内部记下每次调用的类型
type keywordEmbed struct{ kinds []embed.Type }

func (keywordEmbed) Model() string { return guardModel }
func (k keywordEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return k.vec(texts), nil
}
func (k *keywordEmbed) EmbedTyped(_ context.Context, t embed.Type, texts []string) ([][]float32, error) {
	k.kinds = append(k.kinds, t)
	return k.vec(texts), nil
}
func (keywordEmbed) vec(texts []string) [][]float32 {
	out := make([][]float32, len(texts))
	for i, s := range texts {
		v := []float32{0, 0, 0, 1}
		switch {
		case strings.Contains(s, "电池"):
			v = []float32{1, 0, 0, 0.2}
		case strings.Contains(s, "猫"):
			v = []float32{0, 1, 0, 0.2}
		case strings.Contains(s, "诊所"):
			v = []float32{0, 0, 1, 0.2}
		}
		out[i] = v
	}
	return out
}

func TestScoreSample(t *testing.T) {
	ds := smallDataset()
	emb := &keywordEmbed{}
	vectors, err := memoryVectors(context.Background(), options{}, ds, emb, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(emb.kinds) == 0 || emb.kinds[0] != embed.TypeDocument {
		t.Fatalf("库存句向量该按 document 算：%v", emb.kinds)
	}
	emb.kinds = nil
	// 字面先命中：不算相似度，分是 -1
	got, err := scoreSample(context.Background(), emb, ds, ds.Samples[0], vectors)
	if err != nil || len(got) != 1 || !got[0].Literal || got[0].Score != -1 || len(got[0].PerMemory) != 0 || len(emb.kinds) != 0 {
		t.Fatalf("字面命中该直接拦、不嵌入：%+v %v %v", got, err, emb.kinds)
	}
	// 换说法：两段，第二段的文本是前面全部段加上本段；每条禁看条目都有分，最大值是电池那条
	got, err = scoreSample(context.Background(), emb, ds, ds.Samples[1], vectors)
	if err != nil || len(got) != 2 {
		t.Fatalf("两段该有两份证据：%+v %v", got, err)
	}
	for i, ps := range got {
		if ps.Literal || len(ps.PerMemory) != 3 || ps.PerMemory["m2"] != ps.Score || ps.Score < .9 {
			t.Fatalf("第 %d 段：%+v", i+1, ps)
		}
	}
	if emb.kinds[0] != embed.TypeQuery {
		t.Fatalf("样本的句子该按 query 算：%v", emb.kinds)
	}
	// 闲聊：和三条都不沾，分低于 0.7
	got, _ = scoreSample(context.Background(), emb, ds, ds.Samples[2], vectors)
	if got[0].Score >= .7 || got[0].Score < 0 {
		t.Fatalf("闲聊不该过门槛：%+v", got)
	}
	// 没有禁看条目（主人私聊里能看见这些）：不嵌入、分是 -1
	owner := ds.Samples[1]
	owner.Audience = audience.Audience{Channel: "console", Conversation: "console:x", Scene: "private", PrivateOK: true}
	emb.kinds = nil
	got, _ = scoreSample(context.Background(), emb, ds, owner, vectors)
	if got[0].Score != -1 || len(emb.kinds) != 0 {
		t.Fatalf("没有禁看条目不该嵌入：%+v %v", got, emb.kinds)
	}
	// maxScore 就是 memScores 里的最大值
	vs, _ := emb.Embed(context.Background(), []string{"他留着那几节电池"})
	per, overall, _ := memScores(vs, forbidden(ds, ds.Samples[1]), vectors)
	if m, _ := maxScore(vs, forbidden(ds, ds.Samples[1]), vectors); m != overall || overall != per["m2"] {
		t.Fatalf("maxScore 该等于每条条目分数里的最大值：%v %v %v", m, overall, per)
	}
}

func scoreRows(scores ...float64) []ScoreRow {
	var rows []ScoreRow
	for i, s := range scores {
		ps := PartScore{Score: s}
		if s >= 0 {
			ps.PerMemory = map[string]float64{"m1": s}
		}
		rows = append(rows, ScoreRow{ID: fmt.Sprintf("s%d", i+1), Parts: []PartScore{ps}})
	}
	return rows
}

func shifted(rows []ScoreRow, delta float64) []ScoreRow {
	var out []ScoreRow
	for _, r := range rows {
		c := ScoreRow{ID: r.ID, Engine: r.Engine}
		for _, p := range r.Parts {
			np := PartScore{Literal: p.Literal, Score: p.Score}
			if p.Score >= 0 {
				np.Score += delta
				np.PerMemory = map[string]float64{"m1": np.Score}
			}
			c.Parts = append(c.Parts, np)
		}
		out = append(out, c)
	}
	return out
}

// 验收 5 的比较：自己对自己通过；差在 D 以内通过；差超过 D、结构对不上不通过；落在门槛两边的翻转按到门槛的距离判；门槛附近不足 50 段写"没量够"
func TestDiffScoresVerdicts(t *testing.T) {
	a := scoreRows(.2, .5, .69, .71, .95, -1)
	t.Run("自己对自己", func(t *testing.T) {
		d := diffScores(a, a)
		ok, text := d.verdict()
		if !ok || d.MaxD != 0 || len(d.Flips) != 0 || d.Compared != 5 || !strings.Contains(text, "决定相同") {
			t.Fatalf("%v\n%s", ok, text)
		}
		if !strings.Contains(text, "没量够") {
			t.Fatalf("门槛附近不足 50 段该写没量够：\n%s", text)
		}
	})
	t.Run("差在 D 以内", func(t *testing.T) {
		if ok, text := diffScores(a, shifted(a, 3e-7)).verdict(); !ok {
			t.Fatalf("差 3e-7 该通过：\n%s", text)
		}
	})
	t.Run("差超过 D", func(t *testing.T) {
		ok, text := diffScores(a, shifted(a, 5e-5)).verdict()
		if ok || !strings.Contains(text, "超过") {
			t.Fatalf("差 5e-5 该不通过：%v\n%s", ok, text)
		}
	})
	t.Run("翻转在 D 以内：列出来，不算不通过", func(t *testing.T) {
		exactly := float64(float32(.7)) // 线上的得分是 float32，"正好 0.70"转成 float64 比 0.7 小一点：float64 比会当成没过门槛（LESSONS）
		x := scoreRows(exactly)
		y := scoreRows(exactly - 4e-7) // float32 里 0.7 过门槛，再小一点的不过
		d := diffScores(x, y)
		ok, text := d.verdict()
		var atProd int
		for _, f := range d.Flips {
			if thresholdKey(f.Threshold) == "0.70" {
				atProd++
			}
		}
		if atProd != 1 || !ok || !strings.Contains(text, "到门槛的距离都不超过 D") {
			t.Fatalf("门槛 0.70 上该有一个翻转、距离在 D 以内：%d %v\n%s", atProd, ok, text)
		}
	})
	t.Run("结构对不上", func(t *testing.T) {
		b := shifted(a, 0)
		b[1].Parts[0].Literal = true
		if ok, text := diffScores(a, b).verdict(); ok || !strings.Contains(text, "字面或有无相似度不同") {
			t.Fatalf("字面命中不同该不通过：\n%s", text)
		}
		if ok, text := diffScores(a, a[:3]).verdict(); ok || !strings.Contains(text, "样本数不同") {
			t.Fatalf("样本数不同该不通过：\n%s", text)
		}
		c := shifted(a, 0)
		c[2].ID = "别的"
		if ok, text := diffScores(a, c).verdict(); ok || !strings.Contains(text, "对不上") {
			t.Fatalf("样本 id 不同该不通过：\n%s", text)
		}
	})
	t.Run("门槛附近够 50 段不再写没量够", func(t *testing.T) {
		var many []float64
		for i := 0; i < 60; i++ {
			many = append(many, .7+float64(i%9-4)*.002)
		}
		x := scoreRows(many...)
		if ok, text := diffScores(x, x).verdict(); !ok || strings.Contains(text, "没量够") {
			t.Fatalf("%v\n%s", ok, text)
		}
	})
}

// 两份结果读写一遍：写进文件的行，读出来还是那些；compareScoreFiles 对同一份自己通过，对差得多的不通过并返回错误
func TestCompareScoreFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, rows []ScoreRow) string {
		p := filepath.Join(dir, name)
		f, _ := os.Create(p)
		defer f.Close()
		for _, r := range rows {
			json.NewEncoder(f).Encode(r)
		}
		return p
	}
	base := scoreRows(.2, .71, .9)
	for i := range base {
		base[i].Engine = "python"
	}
	pa, pb := write("a.jsonl", base), write("b.jsonl", shifted(base, 2e-7))
	out, err := compareScoreFiles(pa, pb)
	if err != nil || !strings.Contains(out, "A = a.jsonl（python）") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := compareScoreFiles(pa, write("c.jsonl", shifted(base, 1e-3))); err == nil {
		t.Fatal("差 1e-3 该返回错误")
	}
	if _, err := compareScoreFiles(pa, filepath.Join(dir, "没有.jsonl")); err == nil {
		t.Fatal("文件不存在该返回错误")
	}
	if math.IsNaN(diffScores(base, base).MaxD) {
		t.Fatal("MaxD 不该是 NaN")
	}
}

// 数据集：-method S 只查结构（小数据集也能跑），A / B / C 的严格要求（25～30 条记忆、正负各 30、12 类各 5）不变
func TestValidateStrictness(t *testing.T) {
	ds := smallDataset()
	if err := validate(ds, false); err != nil {
		t.Fatalf("小数据集在宽松模式下该通过：%v", err)
	}
	if err := validate(ds, true); err == nil {
		t.Fatal("小数据集在严格模式下该被拒绝")
	}
	bad := smallDataset()
	bad.Samples[0].Ref = "没有的条目"
	if err := validate(bad, false); err == nil {
		t.Fatal("样本引用不存在的条目，宽松模式也该拒绝")
	}
	empty := smallDataset()
	empty.Samples[1].Parts = []string{"  "}
	if err := validate(empty, false); err == nil {
		t.Fatal("空段宽松模式也该拒绝")
	}
	if err := validate(Dataset{Character: "x", Memories: ds.Memories}, false); err == nil {
		t.Fatal("没有样本该拒绝")
	}
}

// -engine go：真的用 embed.Local 起 sumika-models（这里是测试进程扮演的假的）：命令行和后台一样，要 -models-exe 和 -models-root；
// 条目按 document、样本按 query 带 input_type；-method S 跑完写 round<N>-S-go.jsonl，两份同样的结果比起来通过
func TestEngineGoRunsScores(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "models")
	if err := embedtest.WriteModelFiles(embed.ModelFiles(root, guardModel)...); err != nil {
		t.Fatal(err)
	}
	reqLog, argsLog := filepath.Join(dir, "requests.jsonl"), filepath.Join(dir, "args.jsonl")
	t.Setenv(embedtest.EnvOn, "1")
	t.Setenv(embedtest.EnvEmbedModels, guardModel)
	t.Setenv(embedtest.EnvRequestLog, reqLog)
	t.Setenv(embedtest.EnvArgsFile, argsLog)

	if _, _, err := startEmbedding(options{engine: "go"}); err == nil || !strings.Contains(err.Error(), "-models-exe") {
		t.Fatalf("没给 exe 和模型根该说清：%v", err)
	}
	if _, _, err := startEmbedding(options{engine: "别的"}); err == nil {
		t.Fatal("不认识的引擎该报错")
	}

	data := filepath.Join(dir, "data")
	os.MkdirAll(data, 0o755)
	ds := smallDataset()
	b, _ := json.Marshal(ds)
	os.WriteFile(filepath.Join(data, "dataset.json"), b, 0o600)
	o := options{data: data, method: "S", round: 1, firstRound: 1, limit: 6, threshold: .3, engine: "go", modelsExe: exe, modelsRoot: root}
	if err := run(o); err != nil {
		t.Fatalf("-method S -engine go：%v", err)
	}
	rows, err := readScoreRows(filepath.Join(data, "round1-S-go.jsonl"))
	if err != nil || len(rows) != 3 || rows[0].Engine != "go" || rows[0].Model != guardModel || len(rows[1].Parts) != 2 {
		t.Fatalf("结果文件不对：%+v %v", rows, err)
	}
	if _, err := os.Stat(filepath.Join(data, "calls.jsonl")); err == nil {
		t.Fatal("-method S 不记 1500 次预算的账（本机嵌入不花钱）")
	}
	args, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(args), `"--models","gte-multilingual-base"`) || !strings.Contains(string(args), `"--max-line-tokens","2048"`) || !strings.Contains(string(args), "--exit-on-stdin-eof") {
		t.Fatalf("起的该是和后台同一条命令行的 sumika-models：%s", args)
	}
	var kinds []string
	rb, _ := os.ReadFile(reqLog)
	for _, ln := range strings.Split(strings.TrimSpace(string(rb)), "\n") {
		var rec struct {
			Body map[string]json.RawMessage `json:"body"`
		}
		if json.Unmarshal([]byte(ln), &rec) == nil {
			kinds = append(kinds, strings.Trim(string(rec.Body["input_type"]), `"`))
		}
	}
	if len(kinds) < 3 || kinds[0] != "document" || kinds[1] != "document" || kinds[len(kinds)-1] != "query" {
		t.Fatalf("input_type 该依次是预热 document、条目 document，样本全是 query：%v", kinds)
	}
	// 已有结果文件时不覆盖
	if err := run(o); err == nil || !strings.Contains(err.Error(), "已经有了") {
		t.Fatalf("结果文件已有该拒绝覆盖：%v", err)
	}
	// 同一份结果对自己通过（比较命令的端到端：写出来的文件读得回来）
	out, err := compareScoreFiles(filepath.Join(data, "round1-S-go.jsonl"), filepath.Join(data, "round1-S-go.jsonl"))
	if err != nil || !strings.Contains(out, "决定相同") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// 丙（C）在 go 引擎上的三遍（GM7 验收 6，花钱，先报估算再跑）和 python 引擎上的三遍放在同一个 -data 里互不覆盖：结果文件名带引擎；
// 甲、乙不用嵌入，不分引擎；-report 的路径也跟着 -engine 走
func TestResultPathByEngine(t *testing.T) {
	py, gp := options{data: "d", engine: "python"}, options{data: "d", engine: "go"}
	for _, tc := range []struct {
		o      options
		method string
		want   string
	}{
		{py, "C", "round2-C.jsonl"}, {gp, "C", "round2-C-go.jsonl"},
		{py, "A", "round2-A.jsonl"}, {gp, "A", "round2-A.jsonl"}, {gp, "B", "round2-B.jsonl"},
	} {
		if got := filepath.Base(resultPath(tc.o, tc.method, 2)); got != tc.want {
			t.Fatalf("%s / %s：%s，要 %s", tc.o.engine, tc.method, got, tc.want)
		}
	}
}
