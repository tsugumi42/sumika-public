// bench-t2 是表二的统一执行器（规格-表二执行器）：读题库，给每一次（题 × 壳 × 第几次）建临时目录、起壳、量资源、判分，
// 跑完从网关账本把请求对回到每一次，结果写到主工作区的 data/dev/bench-t2/<开始时刻>/。
//
//	go run ./cmd/bench-t2 -work <scratchpad 下的目录> [-shells go,omp] [-repeat 2] [-only 正则] [-dry]
//
// -dry 不调模型：建目录、拷初始文件、拿初始文件判一遍分（必须全部不过，过了说明判分是恒真的）、报估算
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/bench"
)

type runRecord struct {
	Task       string `json:"task"`
	Group      string `json:"group"`
	Shell      string `json:"shell"`
	Rep        int    `json:"rep"`
	Tag        string `json:"tag"`
	Ran        bool   `json:"ran"` // 壳跑成了：结果写出来了、没有壳自己的错、没有哪一步的请求出错
	ShellError string `json:"shellError,omitempty"`
	// Interrupted：执行器被中断（Ctrl+C）时正在跑的那一次。不算"壳崩了"，汇总时不进任何一栏
	Interrupted bool                `json:"interrupted,omitempty"`
	WallMs      int64               `json:"wallMs"`
	Usage       bench.Usage         `json:"usage"`
	Verdict     bench.Verdict       `json:"verdict"`
	Result      bench.Result        `json:"result"`
	ProxyCalls  int                 `json:"proxyCalls"` // 记账代理数到的 POST
	Gateway     []bench.GatewayCall `json:"gateway"`    // 网关账本里对上的
	Warnings    []string            `json:"warnings,omitempty"`
}

type opts struct {
	root, tasks, work, out, gateway, model, thinking, character, attach, judge, reply, set string
	shells                                                                                 []string
	repeat, turn                                                                           int
	only                                                                                   *regexp.Regexp
	dry, approve                                                                           bool
}

func main() {
	var o opts
	shells := flag.String("shells", "go,omp", "哪几个壳，逗号分开：go、omp")
	only := flag.String("only", "", "只跑题号匹配这个正则的")
	flag.StringVar(&o.root, "root", "", "项目根；不给就从当前目录往上找")
	flag.StringVar(&o.set, "set", "", "固定题集：读 <项目根>/scripts/bench-t2/sets/<名字>.txt（一行一个题号，# 开头的是注释）；编程额度实跑用 coding")
	flag.StringVar(&o.tasks, "tasks", "", "题库目录；默认 <项目根>/scripts/bench-t2/tasks")
	flag.StringVar(&o.work, "work", "", "放每一次工作目录、数据目录的地方（必须给，不能在任何 git 仓库里）")
	flag.StringVar(&o.out, "out", "", "结果目录；默认主工作区的 data/dev/bench-t2/<开始时刻>")
	flag.StringVar(&o.gateway, "gateway", "http://127.0.0.1:3425", "Magpie 的地址（不带 /v1）")
	flag.StringVar(&o.model, "model", "auto/deepseek-v4.1-flash", "模型")
	flag.StringVar(&o.thinking, "thinking", "high", "思考档位")
	flag.StringVar(&o.character, "character", "suzu", "角色 id（Go 壳自己写一张编的卡）")
	flag.IntVar(&o.repeat, "repeat", 1, "每题每壳跑几次")
	flag.IntVar(&o.turn, "turn", 600, "一句话最多等几秒")
	flag.BoolVar(&o.dry, "dry", false, "不调模型，只查题目和判分、报估算")
	flag.BoolVar(&o.approve, "approve", false, "要问的审批里只在工作目录里的替主人批准一次（bench.ApproveInWorkdir；B2、B3 量长任务本身），别的照旧取消")
	flag.StringVar(&o.judge, "judge", "", "拿一个目录按 -only 选中的那一道题判一次分、打出结果（出题时验参考答案用；不调模型）")
	flag.StringVar(&o.reply, "reply", "", "和 -judge 一起用：每一句的回复，用 ||| 分开")
	flag.StringVar(&o.attach, "attach", "", "给一个已有的结果目录重新取网关账本、重写汇总（跑到一半崩了、取账本失败时补）")
	flag.Parse()
	o.shells = strings.Split(*shells, ",")
	if *only != "" {
		o.only = regexp.MustCompile(*only)
	}
	do := run
	if o.judge != "" {
		do = judgeDir
	}
	if o.attach != "" {
		do = attach
	}
	if err := do(o); err != nil {
		fmt.Fprintln(os.Stderr, "bench-t2：", err)
		os.Exit(1)
	}
}

type plannedRun struct {
	task  bench.BankTask
	shell string
	rep   int
}

func run(o opts) error {
	start := time.Now()
	stamp := start.Format("20060102-150405")
	prefix := "t2-" + stamp + "-"
	var err error
	if o.root == "" {
		if o.root, err = findRoot(); err != nil {
			return err
		}
	}
	if o.tasks == "" {
		o.tasks = filepath.Join(o.root, "scripts", "bench-t2", "tasks")
	}
	if o.work == "" {
		return errors.New("要给 -work（放工作目录的地方，用会话的 scratchpad，不能在 git 仓库里）")
	}
	if o.work, err = filepath.Abs(filepath.Join(o.work, stamp)); err != nil {
		return err
	}
	if err := os.MkdirAll(o.work, 0o755); err != nil {
		return err
	}
	if top, err := exec.Command("git", "-C", o.work, "rev-parse", "--show-toplevel").Output(); err == nil {
		return fmt.Errorf("-work 在 git 仓库 %s 里：她的 git 命令会打到真仓库上，换个地方", strings.TrimSpace(string(top)))
	}
	if o.out == "" {
		common, err := exec.Command("git", "-C", o.root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
		if err != nil {
			return fmt.Errorf("找主工作区：%w", err)
		}
		o.out = filepath.Join(filepath.Dir(strings.TrimSpace(string(common))), "data", "dev", "bench-t2", stamp)
	}
	for _, s := range o.shells {
		if !slices.Contains([]string{"go", "omp"}, s) {
			return fmt.Errorf("不认识的壳 %q", s)
		}
	}
	bank, err := bench.LoadBank(o.tasks)
	if err != nil {
		return err
	}
	if o.set != "" {
		if bank, err = pickSet(bank, o.root, o.set); err != nil {
			return err
		}
	}
	if o.only != nil {
		bank = slices.DeleteFunc(bank, func(t bench.BankTask) bool { return !o.only.MatchString(t.ID) })
	}
	if len(bank) == 0 {
		return errors.New("没有要跑的题")
	}
	// 按题排；每道题里各壳的先后轮换（不让某一个总赶上网络快的时候）
	var plan []plannedRun
	steps := 0
	for ti, t := range bank {
		for rep := 1; rep <= o.repeat; rep++ {
			k := (ti + rep - 1) % len(o.shells)
			for _, sh := range append(slices.Clone(o.shells[k:]), o.shells[:k]...) {
				plan = append(plan, plannedRun{t, sh, rep})
				steps += len(t.Steps)
			}
		}
	}
	fmt.Printf("题 %d 道、壳 %s、每题每壳 %d 次：共 %d 次、%d 句；按每句 5 次请求估，约 %d 次请求\n",
		len(bank), strings.Join(o.shells, ","), o.repeat, len(plan), steps, steps*5)
	fmt.Printf("工作目录：%s（不在 git 仓库里）\n结果目录：%s\n", o.work, o.out)
	if o.dry {
		return dryCheck(bank, o.work)
	}
	if err := os.MkdirAll(filepath.Join(o.out, "runs"), 0o755); err != nil {
		return err
	}
	goShell := filepath.Join(o.work, "bin", "bench-shell.exe")
	if slices.Contains(o.shells, "go") {
		b := exec.Command("go", "build", "-o", goShell, "./cmd/bench-shell")
		b.Dir = filepath.Join(o.root, "native")
		if out, err := b.CombinedOutput(); err != nil {
			return fmt.Errorf("编译 Go 壳：%v\n%s", err, out)
		}
	}
	writeJSON(filepath.Join(o.out, "meta.json"), meta(o, start, prefix, bank, len(plan)))
	proxy, err := bench.StartProxy(o.gateway)
	if err != nil {
		return err
	}
	defer proxy.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var recs []*runRecord
	for i, p := range plan {
		if ctx.Err() != nil {
			break
		}
		name := fmt.Sprintf("%s-%s-%d", p.task.ID, p.shell, p.rep)
		fmt.Printf("[%d/%d] %s … ", i+1, len(plan), name)
		r := runOne(ctx, o, p, prefix+name, name, goShell, proxy)
		if ctx.Err() != nil { // 跑到一半被中断：不是壳崩了，单独记
			r.Interrupted, r.Ran = true, false
		}
		recs = append(recs, r)
		writeJSON(filepath.Join(o.out, "runs", name+".json"), r)
		fmt.Printf("%s %s，%.0f 秒\n", map[bool]string{true: "过", false: "没过"}[r.Verdict.Pass], ranText(r), float64(r.WallMs)/1000)
	}
	time.Sleep(5 * time.Second) // 最后一次请求的账本行可能还没写完
	notes := progressNotes(recs, len(plan))
	notes = append(notes, attachLedger(o.out, start, prefix, recs)...)
	summary := summarize(recs, notes)
	os.WriteFile(filepath.Join(o.out, "summary.md"), []byte(summary), 0o644)
	fmt.Print(summary)
	return nil
}

// pickSet 按固定题集文件挑题：文件里每个题号都必须在题库里有，缺了就报错（不悄悄少跑一道）
func pickSet(bank []bench.BankTask, root, name string) ([]bench.BankTask, error) {
	if strings.ContainsAny(name, `/\.`) {
		return nil, fmt.Errorf("题集名 %q 不能带路径", name)
	}
	b, err := os.ReadFile(filepath.Join(root, "scripts", "bench-t2", "sets", name+".txt"))
	if err != nil {
		return nil, fmt.Errorf("读题集：%w", err)
	}
	byID := map[string]bench.BankTask{}
	for _, t := range bank {
		byID[t.ID] = t
	}
	var out []bench.BankTask
	for _, line := range strings.Split(string(b), "\n") {
		id := strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if id == "" {
			continue
		}
		t, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("题集 %s 里的 %s 在题库里没有", name, id)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("题集 %s 是空的", name)
	}
	return out, nil
}

// progressNotes：中断了的话，汇总表开头写明跑到第几次、没跑几次（各壳的题目集合不一样，不是配对比较）
func progressNotes(recs []*runRecord, planned int) []string {
	done := 0
	for _, r := range recs {
		if !r.Interrupted {
			done++
		}
	}
	if done == planned || planned == 0 { // 0：旧的结果目录没记计划次数
		return nil
	}
	return []string{fmt.Sprintf("中断了：计划 %d 次，跑完 %d 次，没跑 %d 次（各壳跑到的题不一样，下面的中位数不是配对比较）", planned, done, planned-done)}
}

// attachLedger 取网关账本（起点往前退一分钟），这一次的那几行存成 ledger.csv，按会话对回每一次、重写 runs/*.json。
// 取不到、读不了时返回一句注意（写进汇总表开头：请求数、token 不可用），不悄悄当成 0
func attachLedger(out string, start time.Time, prefix string, recs []*runRecord) []string {
	raw, err := bench.FetchLedger(context.Background(), start.Add(-time.Minute))
	if err == nil {
		var mine []byte
		if mine, err = bench.FilterLedger(raw, prefix); err == nil {
			err = os.WriteFile(filepath.Join(out, "ledger.csv"), mine, 0o644)
		}
	}
	var calls map[string][]bench.GatewayCall
	if err == nil {
		calls, err = bench.ReadLedger(bytes.NewReader(raw), prefix)
	}
	if err != nil {
		fmt.Println("网关账本：", err)
		return []string{"网关账本没取到（" + err.Error() + "）：请求数、token 这几栏不可用，用 -attach " + out + " 补"}
	}
	var notes []string
	for _, r := range recs {
		r.Gateway, r.Warnings = calls[r.Tag], slices.DeleteFunc(r.Warnings, func(w string) bool { return strings.HasPrefix(w, "代理数到") })
		if len(r.Gateway) != r.ProxyCalls {
			w := fmt.Sprintf("代理数到 %d 次请求，账本对上 %d 行", r.ProxyCalls, len(r.Gateway))
			r.Warnings = append(r.Warnings, w)
			notes = append(notes, r.Tag+"："+w)
		}
		writeJSON(filepath.Join(out, "runs", fmt.Sprintf("%s-%s-%d.json", r.Task, r.Shell, r.Rep)), r)
	}
	return notes
}

// attach 给已有的结果目录重新取账本、重写汇总（meta.json 里有开始时刻和会话前缀）
func attach(o opts) error {
	var m struct {
		Start   time.Time `json:"start"`
		Prefix  string    `json:"sessionPrefix"`
		Planned int       `json:"plannedRuns"`
	}
	b, err := os.ReadFile(filepath.Join(o.attach, "meta.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(o.attach, "runs", "*.json"))
	if err != nil {
		return err
	}
	var recs []*runRecord
	for _, f := range files {
		var r runRecord
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return fmt.Errorf("%s：%w", filepath.Base(f), err)
		}
		recs = append(recs, &r)
	}
	notes := progressNotes(recs, m.Planned)
	notes = append(notes, attachLedger(o.attach, m.Start, m.Prefix, recs)...)
	summary := summarize(recs, notes)
	fmt.Print(summary)
	return os.WriteFile(filepath.Join(o.attach, "summary.md"), []byte(summary), 0o644)
}

func ranText(r *runRecord) string {
	if r.Ran {
		return "（跑成）"
	}
	return "（没跑成：" + r.ShellError + "）"
}

// shellTask 是交给壳的这一次的题目：题库里和 Task 同名的字段原样抄过去。
// recentMessages 不能漏：B4 两组都要读到最前面的历史，bench-shell 的真实请求测试覆盖这一点。
func shellTask(o opts, bt bench.BankTask, tag, work, data, gateway string) bench.Task {
	return bench.Task{ID: tag, Workdir: work, DataDir: data, Gateway: gateway, Model: o.model, Thinking: o.thinking,
		Character: o.character, Seed: bt.Seed, Steps: bt.Steps, KeepTokens: bt.KeepTokens, RecentMessages: bt.RecentMessages, ApproveInWorkdir: o.approve, TurnSeconds: o.turn}
}

func runOne(ctx context.Context, o opts, p plannedRun, tag, name, goShell string, proxy *bench.Proxy) *runRecord {
	r := &runRecord{Task: p.task.ID, Group: p.task.Group, Shell: p.shell, Rep: p.rep, Tag: tag}
	if len(tag) > 128 {
		r.ShellError = "会话标签超过 128 字"
		return r
	}
	dir := filepath.Join(o.work, "runs", name)
	work, data := filepath.Join(dir, "work"), filepath.Join(dir, "data")
	if err := p.task.CopyFiles(work); err != nil {
		r.ShellError = "拷初始文件：" + err.Error()
		return r
	}
	os.MkdirAll(data, 0o755)
	t := shellTask(o, p.task, tag, work, data, proxy.URL()+"/v1")
	taskFile, resFile := filepath.Join(dir, "task.json"), filepath.Join(dir, "result.json")
	writeJSON(taskFile, t)
	stderr, _ := os.Create(filepath.Join(dir, "stderr.txt"))
	defer stderr.Close()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(len(t.Steps)*o.turn)*time.Second+3*time.Minute)
	defer cancel()
	proxy.SetTag(tag)
	begin := time.Now()
	var res bench.Result
	var shellErr error
	switch p.shell {
	case "omp":
		res, r.Usage, shellErr = bench.RunOmp(ctx, t, bench.OmpOptions{AgentDir: filepath.Join(dir, "agent"), Stderr: stderr, Interval: 250 * time.Millisecond})
	default:
		cmd := exec.CommandContext(ctx, goShell, "-task", taskFile, "-out", resFile, "-root", o.root)
		cmd.Dir, cmd.Stdout, cmd.Stderr = o.root, stderr, stderr
		r.Usage, shellErr = bench.RunInJob(cmd, 250*time.Millisecond)
		if b, err := os.ReadFile(resFile); err != nil {
			res.Error = "没写出结果"
		} else if err := json.Unmarshal(b, &res); err != nil {
			res.Error = "结果读不了：" + err.Error()
		}
	}
	r.WallMs = time.Since(begin).Milliseconds()
	proxy.SetTag("")
	r.ProxyCalls = proxy.Count(tag)
	r.Result = res
	var errs []string
	if shellErr != nil {
		errs = append(errs, shellErr.Error())
	}
	if res.Error != "" {
		errs = append(errs, res.Error)
	}
	if s := stepErrors(res); s != "" {
		errs = append(errs, s)
	}
	r.ShellError = strings.Join(errs, "；")
	r.Ran = r.ShellError == ""
	replies := make([]string, len(res.Steps))
	for i, s := range res.Steps {
		replies[i] = s.Reply
	}
	r.Verdict = p.task.CheckAll(context.Background(), work, replies)
	copyFinal(work, filepath.Join(o.out, "runs", name+"-final"))
	copyRaw(dir, filepath.Join(o.out, "runs", name+"-raw"))
	return r
}

// copyRaw 把这一次的原始输出拷进结果目录（-work 在会话的 scratchpad 里，会话结束可能就清了）：题目、壳写的结果、stderr、
// omp 的会话记录。Go 的库不拷（步骤和请求已经摘进结果里了），omp 配置目录里的模型缓存也不拷
func copyRaw(dir, dst string) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "work" || rel == "data" {
				return filepath.SkipDir
			}
			return nil
		}
		keep := rel == "task.json" || rel == "result.json" || rel == "stderr.txt" || strings.HasPrefix(rel, "agent/sessions/") && strings.HasSuffix(rel, ".jsonl")
		if !keep {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755)
		os.WriteFile(filepath.Join(dst, rel), b, 0o644)
		return nil
	})
}

// stepErrors 是各步请求出的错（网关连不上之类，算没跑成；超时、停下不算——按了停止，那一步带着"中止了"的错是应该的）
func stepErrors(res bench.Result) string {
	var errs []string
	for i, s := range res.Steps {
		if s.Error != "" && !s.Stopped {
			errs = append(errs, fmt.Sprintf("第 %d 步：%s", i+1, s.Error))
		}
	}
	return strings.Join(errs, "；")
}

// judgeDir 拿 -judge 给的目录按 -only 选中的那一道题判一次分（出题时验参考答案：照参考做法改好的目录必须判过），打出结果，没过退出码 1
func judgeDir(o opts) error {
	var err error
	if o.root == "" {
		if o.root, err = findRoot(); err != nil {
			return err
		}
	}
	if o.tasks == "" {
		o.tasks = filepath.Join(o.root, "scripts", "bench-t2", "tasks")
	}
	bank, err := bench.LoadBank(o.tasks)
	if err != nil {
		return err
	}
	if o.only != nil {
		bank = slices.DeleteFunc(bank, func(t bench.BankTask) bool { return !o.only.MatchString(t.ID) })
	}
	if len(bank) != 1 {
		return fmt.Errorf("-judge 要 -only 正好选中一道题，现在选中了 %d 道", len(bank))
	}
	var replies []string
	if o.reply != "" {
		replies = strings.Split(o.reply, "|||")
	}
	v := bank[0].CheckAll(context.Background(), o.judge, replies)
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
	if !v.Pass {
		return errors.New("判分没过")
	}
	return nil
}

// dryCheck 拿初始文件判一遍分：每道题都必须不过
func dryCheck(bank []bench.BankTask, work string) error {
	var bad []string
	for _, t := range bank {
		dir := filepath.Join(work, "dry", t.ID)
		if err := t.CopyFiles(dir); err != nil {
			return err
		}
		v := t.CheckAll(context.Background(), dir, nil)
		if v.Pass {
			bad = append(bad, t.ID)
		}
		fmt.Printf("  %s：初始文件判 %s（%d 条没过）\n", t.ID, map[bool]string{true: "过 ✗", false: "不过 ✓"}[v.Pass], len(v.Fails))
	}
	if len(bad) > 0 {
		return fmt.Errorf("这几道题拿初始文件就判过了，判分是恒真的：%s", strings.Join(bad, "、"))
	}
	fmt.Println("判分自查通过：初始文件全部判不过")
	return nil
}

// copyFinal 把结束时的工作目录拷进结果目录（单个文件超过 1 MB 的跳过，留着看她最后做成什么样）
func copyFinal(src, dst string) {
	filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > 1<<20 {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755)
		os.WriteFile(filepath.Join(dst, rel), b, 0o644)
		return nil
	})
}

func meta(o opts, start time.Time, prefix string, bank []bench.BankTask, planned int) map[string]any {
	ver := func(name string, args ...string) string {
		out, err := exec.Command(name, args...).Output()
		if err != nil {
			return "取不到：" + err.Error()
		}
		return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	}
	commit, _ := exec.Command("git", "-C", o.root, "rev-parse", "HEAD").Output()
	ids := make([]string, len(bank))
	for i, t := range bank {
		ids[i] = t.ID
	}
	return map[string]any{
		"start": start.Format(time.RFC3339), "sessionPrefix": prefix, "commit": strings.TrimSpace(string(commit)),
		"shells": o.shells, "repeat": o.repeat, "model": o.model, "thinking": o.thinking, "turnSeconds": o.turn, "approveInWorkdir": o.approve,
		"tasks": ids, "plannedRuns": planned, "go": runtime.Version(), "omp": ver("omp", "--version"),
	}
}

func writeJSON(path string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	os.WriteFile(path, b, 0o644)
}

// findRoot 从当前目录往上找项目根（有 prompts/ 和 native/ 的那层）
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if isDir(filepath.Join(dir, "prompts")) && isDir(filepath.Join(dir, "native")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("找不到项目根，用 -root 指定")
		}
		dir = parent
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

// b3StopState 不从 stopped 推断题设停止点：超时也会 stopped，旧结果没记则明确写“未记录”。
func b3StopState(r *runRecord) string {
	if r.Group != "b3" {
		return "不适用"
	}
	known := false
	for _, s := range r.Result.Steps {
		if s.StopTriggered == nil {
			continue
		}
		known = true
		if *s.StopTriggered {
			return "已触发"
		}
	}
	if known {
		return "未触发"
	}
	return "未记录"
}

// summarize 写一张粗表：每个壳过了几次、跑成几次、中位数的耗时 / 请求数 / token / CPU / 内存（细的统计到 B5 再做）。
// notes 写在表前面（中断了、账本没取到、对账对不上）；中断时正在跑的那一次不进任何一栏
func summarize(recs []*runRecord, notes []string) string {
	var b strings.Builder
	for _, n := range notes {
		b.WriteString("> 注意：" + n + "\n")
	}
	if len(notes) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("| 壳 | 结束状态过 / 跑成 / 共 | 违反约束 | 耗时中位（秒） | 请求数中位 | 输入 token 中位 | 输出 token 中位 | CPU 中位（秒） | 内存峰值中位（MB，私有） |\n|---|---|---|---|---|---|---|---|---|\n")
	byShell := map[string][]*runRecord{}
	var shells []string
	for _, r := range recs {
		if r.Interrupted {
			continue
		}
		if _, ok := byShell[r.Shell]; !ok {
			shells = append(shells, r.Shell)
		}
		byShell[r.Shell] = append(byShell[r.Shell], r)
	}
	for _, sh := range shells {
		var pass, ran, viol int
		var wall, calls, in, out, cpu, mem []float64
		for _, r := range byShell[sh] {
			if r.Verdict.Violation {
				viol++
			}
			if !r.Ran {
				continue
			}
			ran++
			if r.Verdict.Pass {
				pass++
			}
			var ti, to float64
			for _, c := range r.Gateway {
				ti += float64(c.Input + c.CacheRead + c.CacheWrite)
				to += float64(c.Output)
			}
			wall = append(wall, float64(r.WallMs)/1000)
			calls = append(calls, float64(len(r.Gateway)))
			in, out = append(in, ti), append(out, to)
			cpu = append(cpu, float64(r.Usage.CPUMs)/1000)
			mem = append(mem, float64(r.Usage.PeakPrivate)/(1<<20))
		}
		fmt.Fprintf(&b, "| %s | %d / %d / %d | %d | %.1f | %.0f | %.0f | %.0f | %.1f | %.0f |\n", sh, pass, ran, len(byShell[sh]), viol,
			median(wall), median(calls), median(in), median(out), median(cpu), median(mem))
	}
	var b3 strings.Builder
	for _, sh := range shells {
		var triggered, missed, unknown, ran, pass int
		for _, r := range byShell[sh] {
			switch b3StopState(r) {
			case "已触发":
				triggered++
				if r.Ran {
					ran++
					if r.Verdict.Pass {
						pass++
					}
				}
			case "未触发":
				missed++
			case "未记录":
				unknown++
			}
		}
		if triggered+missed+unknown > 0 {
			fmt.Fprintf(&b3, "| %s | %d | %d | %d | %d / %d |\n", sh, triggered, missed, unknown, pass, ran)
		}
	}
	if b3.Len() > 0 {
		b.WriteString("\nB3 打断后续做只统计题设停止点已触发的样本；未触发、未记录均不进入分母。\n\n| 壳 | 中断已触发 | 中断未触发 | 中断未记录 | 打断后过 / 跑成 |\n|---|---|---|---|---|\n")
		b.WriteString(b3.String())
	}
	b.WriteString(runTable(recs))
	b.WriteString("\n| 题 | 壳 | 次 | 结果 | B3 中断 | 没过的 |\n|---|---|---|---|---|---|\n")
	for _, r := range recs {
		state := "过"
		switch {
		case r.Interrupted:
			state = "中断（不算）"
		case !r.Ran:
			state = "没跑成：" + r.ShellError
		case !r.Verdict.Pass:
			state = "没过"
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s |\n", r.Task, r.Shell, r.Rep, state, b3StopState(r), strings.Join(r.Verdict.Fails, "；"))
	}
	return b.String()
}

// runTable 是每一次的额度账：请求数、工具数、累计 token（输入含缓存读、缓存写，加输出，和预算账本 Settle 的口径一样）、
// 其中缓存读、输出 token、耗时、每次请求的结束原因（网关账本的 upstream_stop）、回合结局。编程额度实跑（2026-11-09）按它定默认值
func runTable(recs []*runRecord) string {
	var b strings.Builder
	b.WriteString("\n| 题 | 壳 | 次 | 请求数 | 工具数 | 累计 token | 其中缓存读 | 输出 token | 耗时（秒） | 各次请求的结束原因 | 回合结局 | 判分 |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range recs {
		if r.Interrupted {
			continue
		}
		var total, cacheRead, out int64
		var stops []string
		for _, c := range r.Gateway {
			total += c.Input + c.CacheRead + c.CacheWrite + c.Output
			cacheRead += c.CacheRead
			out += c.Output
			stops = append(stops, orDash(c.Stop))
		}
		tools := 0
		var outcomes []string
		for _, s := range r.Result.Steps {
			tools += len(s.Tools)
			outcomes = append(outcomes, orDash(s.Outcome))
		}
		verdict := "过"
		if !r.Verdict.Pass {
			verdict = "没过"
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %d | %d | %.0f | %s | %s | %s |\n", r.Task, r.Shell, r.Rep, len(r.Gateway), tools, total, cacheRead, out,
			float64(r.WallMs)/1000, strings.Join(stops, " "), strings.Join(outcomes, " "), verdict)
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
