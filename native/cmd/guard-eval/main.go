package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/embed"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/memory"
)

type options struct {
	data, model, base, method, python, server string
	round, firstRound, start, limit           int
	threshold                                 float64
	report                                    bool
	// 嵌入走哪个引擎（规格-Go本地模型 GM7）：python 是原来的做法（起 server.py 在 11450），go 起 sumika-models（和后台同一条命令行）
	engine                            string
	modelsExe, ortLibrary, modelsRoot string
	compareScores                     bool // 比两份 -method S 的结果，不跑嵌入
}

func main() {
	var o options
	flag.StringVar(&o.data, "data", "", "合成数据目录（必填；不读应用配置）")
	flag.StringVar(&o.model, "model", "auto/deepseek-v4.1-flash", "Magpie 模型 ID")
	flag.StringVar(&o.base, "base", "http://127.0.0.1:3425/v1", "后台模型接口")
	flag.StringVar(&o.method, "method", "", "A / B / C；S 只算嵌入相似度（不调确认模型、不花钱），把每个样本每一段对每条禁看记忆的最大相似度写出来，两种引擎各跑一份再用 -compare-scores 比")
	flag.IntVar(&o.round, "round", 1, "运行轮号（正整数；原始结果按轮号分文件）")
	flag.IntVar(&o.firstRound, "first-round", 1, "报告采用从此轮开始的连续三轮")
	flag.IntVar(&o.start, "start", 0, "从第几个样本继续（从 0 数）")
	flag.IntVar(&o.limit, "limit", 6, "本次前台最多跑多少个样本")
	flag.Float64Var(&o.threshold, "threshold", .30, "丙的实测门槛；扫描固定为 0.30～0.90")
	flag.StringVar(&o.python, "python", "", "-engine python：对拍参照环境的 python.exe（没有默认值，不写死本机路径）")
	flag.StringVar(&o.server, "server", "../tools/local-models/server.py", "本地服务入口")
	flag.BoolVar(&o.report, "report", false, "汇总已有三遍结果，不调用模型")
	flag.StringVar(&o.engine, "engine", "python", "嵌入引擎：python（起 -python -server 的 server.py，原来的做法）或 go（起 sumika-models，要 -models-exe、-models-root）")
	flag.StringVar(&o.modelsExe, "models-exe", "", "-engine go：sumika-models.exe 的路径")
	flag.StringVar(&o.ortLibrary, "ort-library", "", "-engine go：onnxruntime.dll 的路径；空 = sumika-models.exe 所在目录里的")
	flag.StringVar(&o.modelsRoot, "models-root", "", "-engine go：模型根目录")
	flag.BoolVar(&o.compareScores, "compare-scores", false, "比较两份 -method S 的结果（命令行再给两个 .jsonl 文件），不跑嵌入、不读数据集")
	flag.Parse()
	if o.compareScores {
		if flag.NArg() != 2 {
			fmt.Fprintln(os.Stderr, "-compare-scores 后面要给两个 .jsonl 文件（两种引擎各一份）")
			os.Exit(1)
		}
		out, err := compareScoreFiles(flag.Arg(0), flag.Arg(1))
		fmt.Print(out)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func loadJSON(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}

// 所有请求（含失败）先记账；生成脚本和本命令共用一本，不自动重试。
func reserve(dir, kind string) error {
	path := filepath.Join(dir, "calls.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if n >= 1500 {
		return errors.New("已达 1500 次调用预算，停止")
	}
	return json.NewEncoder(f).Encode(map[string]any{"n": n + 1, "kind": kind, "time": time.Now().Format(time.RFC3339Nano)})
}

type row struct {
	ID        string              `json:"id"`
	Method    string              `json:"method"`
	Round     int                 `json:"round"`
	Model     string              `json:"model"`
	Threshold float64             `json:"threshold"`
	Decision  Decision            `json:"decision"`
	Evidence  map[string]Evidence `json:"evidence"`
	Scans     map[string]Decision `json:"scans,omitempty"`
}

func readRows(path string) ([]row, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []row
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 4*1024*1024)
	for sc.Scan() {
		var r row
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// resultPath 是某遍某方法的结果文件。丙（C）的嵌入引擎不同结果就不同（-engine go 是规格-Go本地模型 GM7 验收 6 的 Go 一侧），文件名加 -go，
// 同一个 -data、同一轮次下两种引擎的三遍互不覆盖；甲、乙不用嵌入，不分引擎。-report 也认 -engine：报告 go 一侧的丙
func resultPath(o options, method string, round int) string {
	suffix := ""
	if method == "C" && o.engine == "go" {
		suffix = "-go"
	}
	return filepath.Join(o.data, fmt.Sprintf("round%d-%s%s.jsonl", round, method, suffix))
}
func run(o options) error {
	if o.data == "" {
		return errors.New("必须给 -data 合成数据目录")
	}
	var ds Dataset
	if err := loadJSON(filepath.Join(o.data, "dataset.json"), &ds); err != nil {
		return err
	}
	if err := validate(ds, o.method != "S"); err != nil {
		return err
	}
	if o.report {
		if o.firstRound < 1 {
			return errors.New("报告起始轮号必须是正整数")
		}
		return report(o, ds)
	}
	if !strings.Contains("|A|B|C|S|", "|"+o.method+"|") || o.engine != "python" && o.engine != "go" || o.round < 1 || o.limit < 1 || o.start < 0 || o.start >= len(ds.Samples) || o.threshold < .30 || o.threshold > .90 {
		return errors.New("方法、遍数、门槛或分段参数不合法")
	}
	if o.method == "S" {
		return runScores(o, ds)
	}
	path := resultPath(o, o.method, o.round)
	rows, err := readRows(path)
	if err != nil {
		return err
	}
	if len(rows) != o.start {
		return fmt.Errorf("已有 %d 条，-start 必须与之相等，避免覆盖或重复", len(rows))
	}
	ctx := context.Background()
	var emb embed.Typed
	var vectors map[string][]float32
	if o.method == "C" {
		var stop func()
		var err error
		emb, stop, err = startEmbedding(o)
		if err != nil {
			return err
		}
		defer stop()
		vectors, err = memoryVectors(ctx, o, ds, emb, true)
		if err != nil {
			return err
		}
	}
	client := llm.New(llm.Magpie(o.base))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for i := o.start; i < min(o.start+o.limit, len(ds.Samples)); i++ {
		s := ds.Samples[i]
		denied := forbidden(ds, s)
		r := row{ID: s.ID, Method: o.method, Round: o.round, Model: o.model, Threshold: o.threshold, Evidence: map[string]Evidence{}}
		get := func(text string) (Evidence, error) {
			if e, ok := r.Evidence[text]; ok {
				return e, nil
			}
			e := Evidence{Text: text, Score: -1}
			t := time.Now()
			e.Literal = literal(text, denied)
			e.LiteralMS = ms(t)
			if len(denied) > 0 || o.method == "B" {
				if o.method == "C" && !e.Literal {
					t = time.Now()
					if err := reserve(o.data, "query-embedding"); err != nil {
						return e, err
					}
					vs, err := emb.EmbedTyped(ctx, embed.TypeQuery, memory.OutboundSentences(text))
					if err != nil {
						return e, err
					}
					if e.Score, err = maxScore(vs, denied, vectors); err != nil {
						return e, err
					}
					e.EmbedMS = ms(t)
				}
				// 扫描用最低门槛收集确认，后续门槛只重放状态机，不把复用算成实测延迟。
				if o.method == "B" || (o.method == "C" && !e.Literal && e.Score >= .30) {
					t = time.Now()
					e.Yes, err = confirm(ctx, o, client, s.Context, text, denied)
					if err != nil {
						return e, err
					}
					e.ModelMS, e.Asked = ms(t), true
				}
			}
			r.Evidence[text] = e
			return e, nil
		}
		// 先真走这一档；只统计实际走过的段，不把扫描的额外资料采集混进主表。
		r.Decision, err = decide(s, o.method, o.threshold, get)
		if err != nil {
			return fmt.Errorf("%s: %w", s.ID, err)
		}
		if o.method == "C" {
			r.Scans = map[string]Decision{}
			for step := 30; step <= 90; step += 5 {
				threshold := float64(step) / 100
				d, err := decide(s, "C", threshold, get)
				if err != nil {
					return err
				}
				r.Scans[fmt.Sprintf("%.2f", threshold)] = d
			}
		}
		if err := json.NewEncoder(f).Encode(r); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		fmt.Printf("round=%d method=%s sample=%d/%d id=%s block=%t confirm=%d embed=%d ms=%.3f\n", o.round, o.method, i+1, len(ds.Samples), s.ID, r.Decision.Blocked, r.Decision.Calls, r.Decision.EmbedCalls, r.Decision.MS)
	}
	return nil
}
func ms(start time.Time) float64 { return float64(time.Since(start)) / float64(time.Millisecond) }
func confirm(ctx context.Context, o options, c *llm.Client, contextText, text string, denied []Memory) (bool, error) {
	if err := reserve(o.data, "confirmation"); err != nil {
		return false, err
	}
	body, err := json.Marshal(map[string]any{"context": contextText, "outbound": text, "forbidden_memories": denied})
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	res, err := c.StreamChat(ctx, chat.Request{Model: o.model, Thinking: "low", MaxOutputTokens: 2048, NoStore: true, Style: chat.StyleBackground,
		Messages: []chat.Message{{Role: chat.RoleSystem, Blocks: []chat.Block{chat.Text(memory.OutboundConfirmPrompt)}}, {Role: chat.RoleUser, Blocks: []chat.Block{chat.Text(string(body))}}}}, nil)
	if err != nil {
		return false, err
	}
	leaked, reason := memory.ParseOutboundConfirmation(res.Text)
	if reason != "" {
		return false, fmt.Errorf("确认输出无效：%s", reason)
	}
	return leaked, nil
}
func memoryVectors(ctx context.Context, o options, ds Dataset, c embed.Typed, count bool) (map[string][]float32, error) {
	// 库存句向量在每次服务启动后预热；不算进每句时延，调用照样记账（count 为假的 -method S 不记账：本机嵌入不花钱，也不占 1500 次预算）。
	texts := []string{}
	seen := map[string]bool{}
	for _, m := range ds.Memories {
		for _, s := range memory.OutboundSentences(m.Text) {
			if !seen[s] {
				seen[s] = true
				texts = append(texts, s)
			}
		}
	}
	out := map[string][]float32{}
	for start := 0; start < len(texts); start += 64 {
		batch := texts[start:min(start+64, len(texts))]
		if count {
			if err := reserve(o.data, "memory-embedding-warmup"); err != nil {
				return nil, err
			}
		}
		vs, err := c.EmbedTyped(ctx, embed.TypeDocument, batch)
		if err != nil {
			return nil, err
		}
		for i, s := range batch {
			out[s] = embed.Unit(vs[i])
		}
	}
	return out, nil
}

// memScores 是线上守卫给每条禁看条目打的分（memory.GuardScore）：这一段的每个查询句向量对这条条目的每个句向量取最大点积；
// 没有可比的（条目没有句子）是 -1，不进表。overall 是全部条目里的最大值（没有可比的是 -1），maxScore 就是它
func memScores(vs [][]float32, denied []Memory, vectors map[string][]float32) (per map[string]float64, overall float64, err error) {
	units := make([][]float32, len(vs))
	for i, v := range vs {
		units[i] = embed.Unit(v)
	}
	per = map[string]float64{}
	best := float32(-1)
	for _, m := range denied {
		var docs [][]float32
		for _, s := range memory.OutboundSentences(m.Text) {
			docs = append(docs, vectors[s])
		}
		sc, err := memory.GuardScore(units, docs)
		if err != nil {
			return nil, 0, err
		}
		per[m.ID] = float64(sc)
		best = max(best, sc)
	}
	return per, float64(best), nil
}

// maxScore 是线上守卫给禁看条目打的分（memory.GuardScore）：每条禁看记忆的每个句向量和每个查询句向量的最大点积，没有可比的是 -1。
func maxScore(vs [][]float32, denied []Memory, vectors map[string][]float32) (float64, error) {
	_, overall, err := memScores(vs, denied, vectors)
	return overall, err
}

// 服务是这次前台命令的子进程；只停自己持有的 PID，退出前一定 Wait。
func startServer(o options) (func(), error) {
	if o.python == "" {
		return nil, errors.New("-engine python 要给 -python（对拍参照环境的 python.exe；没有默认值）")
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	secret := hex.EncodeToString(token)
	if err := os.Setenv("SUMIKA_EMBED_TOKEN", secret); err != nil {
		return nil, err
	}
	cmd := exec.Command(o.python, o.server, "--port", "11450", "--threads", "2")
	cmd.Env = os.Environ()
	log, err := os.OpenFile(filepath.Join(o.data, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	stop := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = log.Close()
		fmt.Printf("local-models stopped pid=%d\n", cmd.Process.Pid)
	}
	fmt.Printf("local-models started pid=%d port=11450\n", cmd.Process.Pid)
	client := http.Client{Timeout: time.Second}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:11450/health", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return stop, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	return nil, errors.New("本次嵌入服务未在 30 秒内就绪，见 server.log")
}

// validate 查数据集。strict 为假（-method S 只比两个引擎的相似度，不量拦住率）时只查结构：角色、记忆字段、样本引用和空段；
// 为真时还要 25～30 条记忆、正负样本各不少于 30、12 类各不少于 5 条（原来的要求，A / B / C 和汇总用）
func validate(ds Dataset, strict bool) error {
	if ds.Character == "" || len(ds.Memories) == 0 {
		return errors.New("角色为空或没有记忆")
	}
	if strict && (len(ds.Memories) < 25 || len(ds.Memories) > 30) {
		return errors.New("记忆不在 25～30 条")
	}
	refs := map[string]bool{}
	for _, m := range ds.Memories {
		if m.ID == "" || refs[m.ID] || len(m.Scope) == 0 || m.Text == "" {
			return errors.New("记忆缺字段或 ID 重复")
		}
		refs[m.ID] = true
	}
	ids := map[string]bool{}
	groups := map[string]int{}
	pos, neg := 0, 0
	for _, s := range ds.Samples {
		if s.ID == "" || ids[s.ID] || !refs[s.Ref] || len(s.Parts) == 0 {
			return errors.New("样本缺字段、引用不存在或 ID 重复")
		}
		ids[s.ID] = true
		for _, part := range s.Parts {
			if strings.TrimSpace(part) == "" {
				return errors.New("样本有空段")
			}
		}
		if s.ShouldBlock {
			pos++
		} else {
			neg++
		}
		groups[s.Category]++
	}
	if len(ds.Samples) == 0 {
		return errors.New("没有样本")
	}
	if !strict {
		return nil
	}
	if pos < 30 || neg < 30 {
		return fmt.Errorf("正负样本不足 30：%d/%d", pos, neg)
	}
	for _, category := range []string{"paraphrase", "split", "mixed", "homophone", "verbatim", "friend_followup", "same_topic", "chitchat", "owner_group", "similar_words", "owner_private", "local_group"} {
		if groups[category] < 5 {
			return fmt.Errorf("分类 %s 只有 %d 条", category, groups[category])
		}
	}
	return nil
}
func report(o options, ds Dataset) error {
	var b strings.Builder
	b.WriteString("|遍|方法/门槛|拦住/该拦|误拦/不该拦|逐段 P50 ms|逐段 P95 ms|确认次数|查询嵌入请求|前段误拦|\n|---|---|---|---|---|---|---|---|---|\n")
	scans := "|门槛|第1遍 拦/误拦/确认|第2遍 拦/误拦/确认|第3遍 拦/误拦/确认|\n|---|---|---|---|\n"
	scanCounts := map[string][]Counts{}
	for round := o.firstRound; round < o.firstRound+3; round++ {
		for _, method := range []string{"A", "B", "C"} {
			rows, err := readRows(resultPath(o, method, round))
			if err != nil {
				return err
			}
			if len(rows) != len(ds.Samples) {
				return fmt.Errorf("第%d遍%s只有%d/%d条", round, method, len(rows), len(ds.Samples))
			}
			decisions := make([]Decision, len(rows))
			for i, r := range rows {
				if r.ID != ds.Samples[i].ID || r.Method != method || r.Round != round {
					return errors.New("结果与数据顺序不符")
				}
				decisions[i] = r.Decision
			}
			c := summarize(ds.Samples, decisions)
			label := method
			if method == "C" {
				label += " @ " + strconv.FormatFloat(rows[0].Threshold, 'f', 2, 64)
			}
			fmt.Fprintf(&b, "|%d|%s|%d/%d|%d/%d|%.3f|%.3f|%d|%d|%d|\n", round-o.firstRound+1, label, c.TP, c.Pos, c.FP, c.Neg, c.P50, c.P95, c.Calls, c.EmbedCalls, c.PrefixFalseBlocks)
			if method == "C" {
				for step := 30; step <= 90; step += 5 {
					key := fmt.Sprintf("%.2f", float64(step)/100)
					for i, r := range rows {
						d, ok := r.Scans[key]
						if !ok {
							return errors.New("缺少扫描结果")
						}
						decisions[i] = d
					}
					scanCounts[key] = append(scanCounts[key], summarize(ds.Samples, decisions))
				}
			}
		}
	}
	for step := 30; step <= 90; step += 5 {
		key := fmt.Sprintf("%.2f", float64(step)/100)
		scans += "|" + key
		for _, c := range scanCounts[key] {
			scans += fmt.Sprintf("|%d/%d/%d", c.TP, c.FP, c.Calls)
		}
		scans += "|\n"
	}
	b.WriteString("\n门槛扫描是同遍证据的离线重放；确认次数是该门槛执行时所需次数，非另发请求。扫描不报告实测时延。\n\n")
	b.WriteString(scans)
	fmt.Fprintf(&b, "\n报告三遍依次采用原始运行轮号 %d、%d、%d；文件名保留原始轮号。\n", o.firstRound, o.firstRound+1, o.firstRound+2)
	fmt.Fprintf(&b, "\n甲、丙在禁看集合为空时直接放行；乙严格每段询问模型（每遍 %d 次）。分位数按 %d 段计算，使用最近秩法；库存向量预热不计入每段时延。拆段只统计最后一段是否正确拦下，前段误拦另外列出。模型确认复用只用于门槛扫描，不用于三种方法的实测主表。\n", sampleParts(ds), sampleParts(ds))
	b.WriteString("\n所有标签来自合成类别，human-review.json 的 10 条尚未由人核对；本结果不能代替上线前真实群负样本验收。只量了 28 条记忆，不代表一万条记忆的时延。丙扫描全部禁看句向量，未加生产的字面/别名候选收窄。模型请求时限 45 秒是离线采集时限，未启用生产退路，超出规格 8 秒预算的请求按实测时间计入。\n")
	ledger, err := os.ReadFile(filepath.Join(o.data, "calls.jsonl"))
	if err != nil {
		return err
	}
	counts := map[string]int{}
	total := 0
	for _, line := range strings.Split(string(ledger), "\n") {
		if line == "" {
			continue
		}
		var call struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			return err
		}
		counts[call.Kind]++
		total++
	}
	fmt.Fprintf(&b, "\n实际请求（含弃用试跑和预热）：%d / 1500；生成 %d，确认 %d，查询嵌入 %d，库存嵌入 %d。\n", total, counts["generation"], counts["confirmation"], counts["query-embedding"], counts["memory-embedding-warmup"])
	text := b.String()
	fmt.Print(text)
	return os.WriteFile(filepath.Join(o.data, "results.md"), []byte(text), 0600)
}

func sampleParts(ds Dataset) int {
	n := 0
	for _, s := range ds.Samples {
		n += len(s.Parts)
	}
	return n
}
