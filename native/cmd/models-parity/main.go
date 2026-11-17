// models-parity 是 Go 版本地模型（sumika-models）对 Python 版（tools/local-models/server.py）的对拍命令
// （规格-Go本地模型 GM6，门槛和做法在第五节）。
//
//	run       起一份 Go 版、一份 Python 版（各自随机端口和令牌，一次只跑一份：Go、Python、Go、Python……三遍交替），
//	          对同一批样本比：分词 id、嵌入向量、重排分数和排序、状态码、相似度差和翻转、速度、内存。
//	registry  比两边的登记表，和模型文件、dll 的哈希。
//
// 样本两批：编的（tokenizer/testdata 的 cases、probes、pairs，空白动物园，内嵌的编的语料）；真实的（-real，
// memory-eval -dump-embed-inputs 导出的文件，**只出数、不出原文**，导出文件和中间文件放临时目录，跑完默认删掉，-keep 才留）。
// 输出（终端和 -json）里只有计数和统计，没有样本文字；Python 回非 2xx 时丢弃响应体、只记状态码。
//
// 例（在 native/ 里；-python 和 -server 没有默认值，不写死 MaiBot 的路径）：
//
//	go run ./cmd/models-parity run -models-root <模型根> -go-exe <sumika-models.exe> -ort-library <onnxruntime.dll> `
//	    -python <python.exe> -server ..\tools\local-models\server.py
//
// 退出码：0 全过；1 有不过的；2 命令没法跑（参数、文件、起不来）；3 没有不过、但有"没量够"（门槛附近的对不到 50 对）或"待定"（余弦低于规格下限、已锁住实测值、等主人定）：都不能当通过。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/embedinputs"
	"sumika-native/internal/localmodels/tokenizer"
)

type options struct {
	modelsRoot string
	goExe      string
	ortLibrary string
	python     string
	server     string
	parityRef  string
	categories string
	testdata   string
	realFile   string
	refOut     string
	refIn      string
	jsonOut    string
	passes     int
	lineLimit  int
	long       bool
	keep       bool
	threads    bool
}

const defaultLineLimit = 2048 // 生产传 --max-line-tokens 2048（规格 2.5，暂定）

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "run", "registry":
	default:
		fmt.Fprintf(os.Stderr, "不认识的子命令 %q：只有 run、registry\n", cmd)
		os.Exit(2)
	}
	o, err := parseOptions(cmd, args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var code int
	if cmd == "registry" {
		code = runRegistry(ctx, o)
	} else {
		code = runParity(ctx, o)
	}
	stop()
	total, residue := stopAll()
	if len(residue) > 0 {
		fmt.Fprintf(os.Stderr, "起过 %d 份服务，还有残留进程 PID %v，请手动按 PID 结束\n", total, residue)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func parseOptions(cmd string, args []string) (*options, error) {
	o := &options{}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.StringVar(&o.modelsRoot, "models-root", "", "模型根目录（必填，没有默认）")
	fs.StringVar(&o.goExe, "go-exe", "", "sumika-models.exe 的路径（必填）")
	fs.StringVar(&o.ortLibrary, "ort-library", "", "onnxruntime.dll 的绝对路径（必填，哈希要在 ort.KnownDLLs 里）")
	fs.StringVar(&o.python, "python", "", "能 import tools/local-models/server.py 的 Python（没有默认值；不给就只跑 Go 这一边或要 -ref-in）")
	fs.StringVar(&o.server, "server", "", "tools/local-models/server.py 的路径（给了 -python 就必填）")
	fs.StringVar(&o.parityRef, "parity-ref", "", "tools/local-models/parity_ref.py 的路径（缺省取 -server 同一个目录里的）")
	fs.StringVar(&o.categories, "categories", "", "categories.json 的路径（缺省取 -testdata 下 tokenizer/testdata 那份）")
	fs.StringVar(&o.testdata, "testdata", filepath.Join("internal", "localmodels"), "编的样本在哪（native/internal/localmodels；在 native/ 里跑就用缺省）")
	fs.StringVar(&o.realFile, "real", "", "真实样本：memory-eval -dump-embed-inputs 导出的文件；不给就只比编的")
	fs.StringVar(&o.refOut, "ref-out", "", "把 Python 这一边的全部结果存成这个文件（只许临时目录或被 git 忽略的路径）")
	fs.StringVar(&o.refIn, "ref-in", "", "不起 Python，读之前 -ref-out 存的结果；样本清单对不上就拒绝。改坏检查用它：只重编 Go、只跑 Go")
	fs.StringVar(&o.jsonOut, "json", "", "把报告写成这个 JSON 文件（只有计数和统计）")
	fs.IntVar(&o.passes, "passes", 0, "几遍（Go、Python 交替）；缺省：起 Python 时 3，-ref-in 时 1")
	fs.IntVar(&o.lineLimit, "max-line-tokens", defaultLineLimit, "Go 版的单行 token 上限，超过的两边都不送")
	fs.BoolVar(&o.long, "long", false, "长行（不限制行长，Go 不传 --max-line-tokens，要 --max-bytes 放宽）：8192 token 一行 43 秒、峰值近 8 GB，单独跑、只跑这一样，跑之前先报给主人")
	fs.BoolVar(&o.keep, "keep", false, "不删临时目录（真实样本的导出文件和 id 参照在里面）")
	fs.BoolVar(&o.threads, "threads-check", false, "另跑 parity_ref.py threads：线程数 1 / 2 / 4 的输出差（只记录，不进门槛）")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("多余的参数 %q", fs.Arg(0))
	}
	var missing []string
	need := func(v, name string) {
		if v == "" {
			missing = append(missing, name)
		}
	}
	need(o.modelsRoot, "-models-root")
	need(o.goExe, "-go-exe")
	if cmd == "run" {
		need(o.ortLibrary, "-ort-library")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("缺参数：%s", strings.Join(missing, "、"))
	}
	if o.python != "" && o.server == "" {
		return nil, errors.New("给了 -python 就要给 -server")
	}
	if o.python != "" && o.refIn != "" {
		return nil, errors.New("-python 和 -ref-in 只能给一个：要么现场起 Python，要么读存好的结果")
	}
	if o.parityRef == "" && o.server != "" {
		o.parityRef = filepath.Join(filepath.Dir(o.server), "parity_ref.py")
	}
	if o.categories == "" {
		o.categories = filepath.Join(o.testdata, "tokenizer", "testdata", "categories.json")
	}
	if o.passes == 0 {
		o.passes = 3
		if o.refIn != "" {
			o.passes = 1
		}
	}
	if o.passes < 1 {
		return nil, errors.New("-passes 至少 1")
	}
	if o.long {
		o.lineLimit = 8192
	}
	for name, p := range map[string]string{"-models-root": o.modelsRoot, "-go-exe": o.goExe, "-ort-library": o.ortLibrary, "-server": o.server, "-testdata": o.testdata} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("%s %s：%v", name, p, err)
		}
	}
	for _, p := range []*string{&o.modelsRoot, &o.goExe, &o.ortLibrary, &o.server, &o.parityRef, &o.categories, &o.testdata, &o.realFile, &o.refIn, &o.refOut, &o.jsonOut} { // python 可能是 PATH 里的名字，不转
		if *p != "" {
			abs, err := filepath.Abs(*p)
			if err != nil {
				return nil, err
			}
			*p = abs
		}
	}
	return o, nil
}

func (o *options) summary() map[string]any {
	return map[string]any{"passes": o.passes, "maxLineTokens": o.lineLimit, "long": o.long, "real": o.realFile != "", "refIn": o.refIn != "", "python": o.python != "", "threadsCheck": o.threads}
}

func goLaunch(o *options) launch {
	args := []string{"--port", "{port}", "--models", gteID, "--lazy-models", bgeID, "--max-loaded", "2", "--idle-unload", "0", "--threads", "2",
		"--models-root", o.modelsRoot, "--ort-library", o.ortLibrary, "--exit-on-stdin-eof"}
	if o.long {
		args = append(args, "--max-bytes", strconv.FormatInt(16<<30, 10)) // 缺省行长的内存估计 11 GiB，超过缺省 4 GiB 会立刻 503
	} else {
		args = append(args, "--max-line-tokens", strconv.Itoa(o.lineLimit))
	}
	return launch{Engine: "go", Exe: o.goExe, Args: args, Stdin: true}
}

func pythonLaunch(o *options) launch {
	return launch{Engine: "python", Exe: o.python,
		Args: []string{"-I", "-B", "-X", "utf8", o.server, "--port", "{port}", "--max-loaded", "2", "--idle-unload", "0", "--threads", "2"},
		Env:  []string{"LOCAL_MODELS_ROOT=" + o.modelsRoot}}
}

// refFile 是 -ref-out / -ref-in 的文件：Python 这一边的全部结果（向量是真实文本算出来的，所以和导出文件一样当真实数据对待）。
type refFile struct {
	Version  int        `json:"version"`
	PlanHash string     `json:"planHash"`
	Python   []*sideRun `json:"python"`
}

func runParity(ctx context.Context, o *options) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
		return 2
	}
	work, err := os.MkdirTemp("", "models-parity-")
	if err != nil {
		return fail("建不了临时目录：%v", err)
	}
	if !o.keep {
		defer os.RemoveAll(work)
	} else {
		fmt.Fprintf(os.Stderr, "-keep：临时目录 %s 里有导出的真实样本和 id 参照，用完请自己删\n", work)
	}
	for _, p := range []string{o.refOut} {
		if p != "" {
			if err := embedinputs.CheckPrivatePath(p); err != nil {
				return fail("%v", err)
			}
		}
	}
	t0 := time.Now()
	tks, err := loadTokenizers(o.modelsRoot)
	if err != nil {
		return fail("%v", err)
	}
	tokLoad := time.Since(t0)
	var realIn *embedinputs.File
	if o.realFile != "" {
		f, err := embedinputs.Read(o.realFile)
		if err != nil {
			return fail("读 -real：%v", err)
		}
		realIn = &f
	}
	src := sourceFiles{root: o.testdata}
	pl, err := buildPlan(src, tks.gte, tks.gteSpec, o.lineLimit, realIn)
	if err != nil {
		return fail("排样本：%v", err)
	}
	rep := newReport("run", o.summary())
	rep.Counts = pl.counts()
	rep.Rows = append(rep.Rows, Row{Stage: "分词", What: "Go 分词器加载（gte + bge 两份 tokenizer.json，含哈希）", Value: fmt.Sprintf("%d ms", tokLoad.Milliseconds()), Verdict: vInfo})

	// 分词 id
	tok := &tokenResult{Single: map[string]verdict{}, Pair: map[[2]string]verdict{}}
	if err := tokenStageCommitted(src, tks, tok, pl); err != nil {
		return fail("分词（编的参照）：%v", err)
	}
	catsRaw, err := os.ReadFile(o.categories)
	if err != nil {
		return fail("%v", err)
	}
	cats, err := tokenizer.ParseCategories(catsRaw)
	if err != nil {
		return fail("%v", err)
	}
	live := liveLists(pl)
	if rr, err := parityRefReal(o, work, live); err != nil {
		return fail("%v", err)
	} else if rr != nil {
		tokenStageLive(tks, cats, rr, map[string][]string{"items": live.Items, "queries": live.Queries, "group": live.Group, "sentences": live.Sentences}, pl.Groups, tok, realIn != nil)
	} else {
		tok.Rows = append(tok.Rows, Row{Stage: "分词", What: "编的语料和真实样本对现场导出的 Python id", Value: "没给 -python / -parity-ref，没跑（编的 cases、probes、pairs 照参照比了）", Verdict: vNotRun})
	}
	rep.Rows = append(rep.Rows, tok.Rows...)

	// 服务：Go、Python 交替
	var goRuns, pyRuns []*sideRun
	if o.refIn != "" {
		var rf refFile
		if err := readJSON(o.refIn, &rf); err != nil {
			return fail("读 -ref-in：%v", err)
		}
		if rf.Version != 1 || rf.PlanHash != pl.Hash {
			return fail("-ref-in 的样本清单（%s）和这次的（%s）对不上：样本或 -max-line-tokens 变了，要重新存", rf.PlanHash, pl.Hash)
		}
		pyRuns = rf.Python
	}
	for pass := 1; pass <= o.passes; pass++ {
		run, err := oneSide(ctx, goLaunch(o), pl, pass)
		if err != nil {
			return fail("Go 第 %d 遍：%v", pass, err)
		}
		goRuns = append(goRuns, run)
		if o.python != "" {
			run, err := oneSide(ctx, pythonLaunch(o), pl, pass)
			if err != nil {
				return fail("Python 第 %d 遍：%v", pass, err)
			}
			pyRuns = append(pyRuns, run)
		}
	}
	for _, r := range append(append([]*sideRun{}, goRuns...), pyRuns...) {
		if len(r.Errors) > 0 {
			rep.Rows = append(rep.Rows, Row{Stage: "请求", What: fmt.Sprintf("%s 第 %d 遍有请求没送成", r.Engine, r.Pass), Value: strings.Join(r.Errors[:min(len(r.Errors), 5)], "；"), Gate: "0", Verdict: vFail})
		}
	}
	if o.refOut != "" && o.python != "" {
		b, err := json.Marshal(refFile{Version: 1, PlanHash: pl.Hash, Python: pyRuns})
		if err != nil {
			return fail("%v", err)
		}
		if err := os.WriteFile(o.refOut, b, 0o600); err != nil {
			return fail("写 -ref-out：%v", err)
		}
	}
	a := &analysis{o: o, pl: pl, tok: tok, goRuns: goRuns, pyRuns: pyRuns}
	rep.Rows = append(rep.Rows, a.all()...)
	if o.threads && o.python != "" {
		rep.Rows = append(rep.Rows, threadsRow(o))
	}
	total, residue := stopAll()
	rep.Started, rep.Residue = total, residue
	rep.Rows = append(rep.Rows, Row{Stage: "收尾", What: "起过的服务进程收尾时都不在了（按 PID 查 tasklist）", Value: fmt.Sprintf("起过 %d 份，残留 %d 个", total, len(residue)), Gate: "0", Verdict: verdictOf(len(residue) == 0)})
	rep.finish()
	rep.print(os.Stdout)
	if o.jsonOut != "" {
		if err := rep.writeJSON(o.jsonOut); err != nil {
			fmt.Fprintf(os.Stderr, "写 -json：%v\n", err)
			return 2
		}
	}
	return rep.exitCode()
}

// liveLists 把清单里的文字按 embedinputs 的列表分开（给 parity_ref.py real 和分词的现场对拍用）。
func liveLists(pl *plan) embedinputs.File {
	var f embedinputs.File
	for _, s := range pl.Texts {
		switch s.Set {
		case setCorpusItems, setRealItems:
			f.Items = append(f.Items, s.Text)
		case setCorpusQ, setRealQ:
			f.Queries = append(f.Queries, s.Text)
		case setRealGroup:
			f.Group = append(f.Group, s.Text)
		case setCorpusSent, setRealSent:
			f.Sentences = append(f.Sentences, s.Text)
		}
	}
	for _, g := range pl.Groups {
		f.Rerank = append(f.Rerank, embedinputs.RerankInput{Query: g.Q, Docs: g.Docs})
	}
	return f
}

// oneSide 起一份服务、送一遍、收掉。出错也收。
func oneSide(ctx context.Context, l launch, pl *plan, pass int) (*sideRun, error) {
	p, err := startReady(ctx, l, 3*time.Minute)
	if err != nil {
		return nil, err
	}
	defer p.stop()
	fmt.Fprintf(os.Stderr, "[%s 第 %d 遍] 起了 PID %d，端口 %d\n", l.Engine, pass, p.pid(), p.port)
	run, err := collect(ctx, p, pl, pass)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "[%s 第 %d 遍] 送完：%d 条嵌入、%d 对、%d 组\n", l.Engine, pass, len(run.Embed), len(run.Pairs), len(run.Groups))
	return run, nil
}

func threadsRow(o *options) Row {
	cases := filepath.Join(o.testdata, "ort", "testdata", "cases.json")
	out, err := exec.Command(o.python, "-I", "-B", "-X", "utf8", o.parityRef, "threads", "--models-root", o.modelsRoot, "--cases", cases).Output()
	if err != nil {
		return Row{Stage: "噪声底", What: "线程数 1 / 2 / 4 的输出差", Value: "parity_ref.py threads 失败：" + err.Error(), Verdict: vFail}
	}
	var r struct {
		Samples int                `json:"samples"`
		Repeat  []float64          `json:"repeat_threads2_max_abs_diff"`
		Threads map[string]float64 `json:"threads_max_abs_diff_vs_2"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return Row{Stage: "噪声底", What: "线程数 1 / 2 / 4 的输出差", Value: "解析不了：" + err.Error(), Verdict: vFail}
	}
	return Row{Stage: "噪声底", What: "Python 进程内：线程数 1 / 4 对 2 的最大分量差，和同参数重新加载两次的差（编的 cases，不进门槛）",
		Value: fmt.Sprintf("%d 句；线程 1：%.3e，线程 4：%.3e；同参数重新加载：%v", r.Samples, r.Threads["1"], r.Threads["4"], r.Repeat), Verdict: vInfo}
}
