package main

// 报告（规格-记忆写入上线 W6 技术路线第 7、8 点）：写在 -work/out/ 里，不进 git。
//
//	report.md     给主人自己看：有聊天原话、真实名字，号码只留末四位。第一行写明谁能打开
//	summary.json  给会话和子代理读：只有计数、条目编号、范围、状态、证据次数，没有聊天原文、名字、证据原话
//	marks.csv     待标清单：主人抽检时在"你的判断"一栏填，-score 读它算数
//
// 引用的原话先转义（Markdown 特殊字符、HTML 尖括号、反引号），免得原话里的格式破坏报告结构；号码在每个会被打印的字段里单独处理（maskNumbers），
// 不是写完整份文档再统一替换：条目编号（f:…、e:…）里偶尔有连续数字，不能被改

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// startupNotice 在拷库之前就打到标准错误：会话或子代理常把工具输出贴回来，提醒要赶在它们看到报告之前（report.go 的 reportWarning 是报告第一行）
const startupNotice = "提醒：这次运行出的 report.md 和 marks.csv 里有聊天原话和真实名字，只由主人自己打开。不要把它们贴给 Claude 会话或子代理：读了，内容就会进 Anthropic 或 OpenAI 的服务。要汇报请读同目录的 summary.json（只有计数、编号、范围）。"

const reportWarning = "**这份报告里有聊天原话和真实名字，只由主人自己打开。不要让 Claude 会话或子代理读它：读了，内容就会进 Anthropic 或 OpenAI 的服务。要汇报请读同目录的 summary.json（只有计数、编号、范围，没有正文）。**"

// escMD 转义一段要放进 Markdown 行内的原话：换行并成空格；反斜杠、反引号、星号、下划线、方括号、尖括号、竖线、井号、波浪线、感叹号前面加反斜杠；& 转实体
func escMD(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '\\', '`', '*', '_', '[', ']', '|', '#', '~', '!':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// shown：要打印出来的一段原话 / 名字 / 会话 id：先打码号码，再转义
func shown(s string) string { return escMD(maskNumbers(s)) }

// wantsText：想取代的旧事实现在是什么情况，写在"旧 → 新"那一行后面
func wantsText(f factRow) string {
	switch f.WantsTarget {
	case wantsAI:
		return "旧事实是 AI 自己提升的，这条攒够证据时可以换掉它"
	case wantsReplaced:
		return "旧事实是 AI 自己提升的，已经被这条换掉了"
	case wantsProtected:
		return "旧事实受保护（导入的或你放行过的），这条提升时会被挡下，不会换掉它，要你来动"
	case wantsGone:
		return "旧事实已经不是有效的了，提升时不会取代任何东西"
	}
	return "库里找不到这个旧事实"
}

func scopesText(sc []string) string {
	if len(sc) == 0 {
		return "（没有范围）"
	}
	out := make([]string, len(sc))
	for i, s := range sc {
		out[i] = shown(s)
	}
	return strings.Join(out, "、")
}

func subjectText(s string) string {
	switch s {
	case "":
		return "没认出是谁"
	case "owner":
		return "主人"
	case "self":
		return "她自己"
	}
	return shown(s)
}

// 密钥形状（W5 的写前过滤同一批形状；这里是独立的第二次扫描，规格 W6 报告 G 节：宁严勿松）
var secretShapes = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`[0-9a-fA-F]{32,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`(?i)(密码|password|token|密钥)[\s:：=是为]*\S{6,}`),
}

func hasSecretShape(s string) bool {
	for _, re := range secretShapes {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// reportData 是写报告用的一份整理好的数据（从 runResult 来）
type reportFiles struct {
	Report  string
	Summary []byte
	Marks   []byte
}

// 抽检样本的大小（规格 W6 具体参数）
const (
	sampleFacts    = 50
	sampleEpisodes = 30
)

// kindKey 是 summary.json 里场合的英文键
func kindKey(k convKind) string {
	switch k {
	case kindLocalPrivate:
		return "local_private"
	case kindLocalGroup:
		return "local_group"
	case kindQQOwnerPrivate:
		return "qq_owner_private"
	case kindQQOtherPrivate:
		return "qq_other_private"
	case kindQQGroup:
		return "qq_group"
	}
	return "unknown"
}

func buildReport(r *runResult) reportFiles {
	return reportFiles{Report: reportMD(r), Summary: summaryJSON(r), Marks: marksCSV(r)}
}

func writeReport(r *runResult) error {
	files := buildReport(r)
	for name, b := range map[string][]byte{"report.md": []byte(files.Report), "summary.json": files.Summary, "marks.csv": files.Marks} {
		if err := os.WriteFile(filepath.Join(r.Out, name), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// ───────────── report.md ─────────────

func reportMD(r *runResult) string {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	p("# 影子整理报告")
	p("")
	p("%s", reportWarning)
	p("")
	p("模式：%s。号码只留末四位，名字照写。哪些数是真实数据上量的、哪些是自检：A、B、E 节的数来自这次在副本上跑的结果（%s）；F 节是自检，预期来自工具里手写的一份表，不是被测代码算的。",
		modeLabel(r.Mode), yesNo(r.Mode == modeEstimate, "估算模式没有真的写东西，B～G 节是空的", yesNo(r.Mode == modeFake, "假生成器写的东西是假的，只看结构", "真实的后台模型")))
	p("")

	p("## A 总览")
	p("")
	p("```")
	b.WriteString(estimateText(r))
	p("```")
	p("")

	ex := r.Extract
	newFacts, newEps := []factRow(nil), []epRow(nil)
	if ex != nil {
		newFacts, newEps = ex.newFacts(), ex.newEpisodes()
	}

	p("## B 每条新事实（%d 条）", len(newFacts))
	p("")
	if len(newFacts) == 0 {
		p("这次没有写出新事实。")
	}
	for _, f := range newFacts {
		p("")
		p("### %s", f.Ref)
		p("- 种类：%s；主体：%s；状态：%s%s", f.Kind, subjectText(f.Subject), f.Status, yesNo(f.ConfirmedBy != "", "；放行："+f.ConfirmedBy, ""))
		p("- 范围：%s", scopesText(f.Scopes))
		if f.AttributedTo != "" {
			p("- 听谁说的（attributed_to）：%s", shown(f.AttributedTo))
		}
		p("- 来自哪个动作：%s%s", yesNo(f.AddBy == "summary", "整理", f.AddBy), yesNo(f.AddReason != "", "（"+escMD(f.AddReason)+"）", ""))
		p("- 内容：%s", shown(f.Text))
		if len(f.Evidence) == 0 {
			p("- 证据：没有记（这条没有 src 或 src 不是真人的话）")
		}
		for _, e := range f.Evidence {
			who := yesNo(e.BySubject, "本人说", "转述")
			q := "「" + shown(e.Quote) + "」"
			if e.Missing {
				q = "（回查不到这条消息）"
			}
			p("- 证据：%s · 会话 %s · %s · 第 %d 个说话人 · 原话 %s", e.Day, shown(e.Conv), who, e.Ord, q)
		}
	}
	p("")

	// C
	var confirmed []factRow
	if ex != nil {
		confirmed = ex.confirmedNow()
	}
	p("## C 会提升的（副本里真提升了的）")
	p("")
	p("这一节是你抽检 W3 读法够不够的主要依据。副本里强制打开了 autoConfirm（正式配置里它默认关），所以下面是\"打开它以后\"的样子：打开的那次启动先对已经攒够证据的待确认事实补提升一遍（含导入的旧候选），之后每批整理里攒够的再提升。证据按 3 次不同场合算，且至少 1 次是本人说的；只提升 fact 和 preference。")
	p("")
	p("- 启动补提升（先于整理，不调模型）：%s。\"看了\"只数种类能自动提升、证据行数够、范围在 -places 放行的场合里的待确认；范围不放行的不进事务，计在\"没碰\"里", sweepText(r.Sweep))
	p("- 整理过程中：confirmed（真提升）计数 %d；confirm_blocked_protected（想取代的旧事实受保护、没提升）%d", r.Counts["confirmed"], r.Counts["confirm_blocked_protected"])
	p("- would_confirm（autoConfirm 关着时数的\"条件满足但没动手\"）计数：%d。副本里 autoConfirm 开着，这个数应该是 0；不是 0 说明开关没生效", r.Counts["would_confirm"])
	p("- 提升的条目共 %d 条（下面逐条列，含导入的旧候选）", len(confirmed))
	for _, f := range confirmed {
		p("")
		p("### %s（%s）", f.Ref, yesNo(f.New, "这次新写的", "导入的旧候选"))
		p("- 种类：%s；主体：%s；范围：%s", f.Kind, subjectText(f.Subject), scopesText(f.Scopes))
		p("- 内容：%s", shown(f.Text))
		if len(f.Evidence) == 0 {
			p("- 证据：没有记")
		}
		for _, e := range f.Evidence {
			p("- 证据：%s · 会话 %s · %s · 第 %d 个说话人 · 原话 %s", e.Day, shown(e.Conv), yesNo(e.BySubject, "本人说", "转述"), e.Ord,
				yesNo(e.Missing, "（回查不到这条消息）", "「"+shown(e.Quote)+"」"))
		}
	}
	if ex != nil {
		p("- 导入的待确认：共 %d 条，其中 %d 条在\"最近 20 条、主人私聊看得见的\"那一块里能被模型看到（W3a 的候选块只给模型这 20 条，其余的这一批补不了证据；统计的是一开始的状态，被启动补提升提走的也算）", ex.ImportedCandidates, ex.ImportedInTop20)
	}
	p("")

	// D
	p("## D 被保护挡下的取代、撤销、提升")
	p("")
	p("- supersede_deferred（候选想取代、延后到提升时）：%d；supersede_protected（取代被保护挡下）：%d；revoke_protected：%d；confirm_blocked_protected（候选因想取代的旧事实受保护而不提升）：%d；revoke_refused（撤销没有主人的话作依据）：%d；supersede_lost：%d",
		r.Counts["supersede_deferred"], r.Counts["supersede_protected"], r.Counts["revoke_protected"], r.Counts["confirm_blocked_protected"], r.Counts["revoke_refused"], r.Counts["supersede_lost"])
	oldText := map[string]string{}
	if ex != nil {
		for _, f := range ex.Facts {
			oldText[f.ID] = f.Text
		}
	}
	pairs := 0
	if ex != nil {
		for _, f := range ex.Facts {
			// 记下了"想取代谁"的候选：这次新写的、这次提升的，和还在待确认里的（想取代的旧事实受保护、一直提不上去的，正是这一节要让你看到的）
			if f.WantsSupersede == "" || (!f.New && !f.Confirmed && f.Status != "candidate") {
				continue
			}
			pairs++
			old := oldText[strings.TrimPrefix(f.WantsSupersede, "f:")]
			p("- 想取代：旧「%s」→ 新「%s」（%s）", yesNo(old == "", "（找不到旧事实 "+shown(f.WantsSupersede)+"）", shown(old)), shown(f.Text), wantsText(f))
		}
	}
	if pairs == 0 {
		p("- 没有记下\"想取代谁\"的候选。")
	}
	p("")
	p("受保护的有效事实（status = active 且 confirmed_by 不是 ai；W2 之后整理动不了它们）。\"别人\"那一格是 W2 里\"没人改得动\"的量。来源是按每条事实第一条 add 修订的 by 分的显示用标签，不当作\"谁写的\"的依据：")
	p("")
	p("| 来源 | 主人 | 她自己 | 别人 |")
	p("|---|---|---|---|")
	for _, src := range []string{srcBunSummary, srcOwnerOK, srcOther} {
		row := make([]int, 3)
		if ex != nil {
			for i, subj := range []string{subjOwner, subjSelf, subjOther} {
				row[i] = ex.Protected[protectedStat{src, subj}]
			}
		}
		p("| %s | %d | %d | %d |", src, row[0], row[1], row[2])
	}
	p("")

	// E
	p("## E 片段（写了 %d 个）", len(newEps))
	p("")
	for _, e := range newEps {
		p("- %s · 会话 %s · 范围 %s", e.Ref, shown(e.Conv), scopesText(e.Scopes))
		p("  - 内容：%s", shown(e.Text))
	}
	if len(newEps) == 0 {
		p("这次没有写出片段。")
	}
	p("")
	p("没写片段的批：episode_record 是 no 的 %d 批；写了 yes 却没有内容的 %d 批。", r.Counts["episode_no"], r.Counts["episode_yes_empty"])
	p("")
	p("**漏记抽查**：调了模型却什么都没写的批共 %d 个，随机抽 %d 个（种子 %d）。下面是这一批实际给过模型的聊天行，请看有没有\"明显该记\"的事被漏了：", r.EmptyTotal, len(r.Empties), r.ShadowSeed)
	p("")
	for _, e := range r.Empties {
		reason := yesNo(e.Reason == "", "（模型没写原因）", shown(e.Reason))
		p("### 批 b:%d · 会话 %s · 第 %d 到 %d 条", e.N, shown(e.Conv), e.From, e.To)
		p("- 模型说没写的原因：%s", reason)
		for _, line := range strings.Split(e.Transcript, "\n") {
			if t := strings.TrimSpace(line); t != "" {
				p("  - %s", shown(t))
			}
		}
	}
	if len(r.Empties) == 0 {
		p("没有这样的批。")
	}
	p("")

	// F
	p("## F 自检")
	p("")
	b.WriteString(selfCheckMD(r))
	p("")
	p("")

	// G
	p("## G 口径对 issue #17")
	p("")
	g := goalNumbers(r)
	p("**自动能算的：**")
	p("")
	p("- 越权次数：%s", g.overreach)
	p("- 重复事实数（同一主体同时有效的相似事实对）：%d 组；待确认里的相似对：%d 对", g.dupActive, g.dupCandidates)
	p("- 注入平均字数：这里算不了——要先有召回的题目（W6b 的 `-fixture issue17`）")
	p("- 密钥形状扫描（写出的事实和片段正文里还有没有 `sk-` 长串、32 位以上十六进制、`AKIA`、\"密码 / token / 密钥\"后面紧跟 6 位以上字符）：命中 %d 条（%s）；整理计数 secret_dropped = %d", len(g.secretRefs), yesNo(len(g.secretRefs) == 0, "无", strings.Join(g.secretRefs, "、")), r.Counts["secret_dropped"])
	p("")
	p("**要你标了才能算的**（工具不自称算出了这两个数）：应记事实入库率、最终回答正确率。待标清单是同目录的 marks.csv（事实 %d 条、片段 %d 个、漏记抽查 %d 批），填好\"你的判断\"一栏以后跑 `memory-shadow -score marks.csv`。", g.markFacts, g.markEpisodes, g.markMiss)
	return b.String()
}

// ───────────── G 的自动数字、marks ─────────────

type goalNums struct {
	overreach                string
	dupActive, dupCandidates int
	secretRefs               []string
	markFacts, markEpisodes  int
	markMiss                 int
}

func goalNumbers(r *runResult) goalNums {
	g := goalNums{overreach: "（自检没跑）"}
	if r.Check != nil {
		g.overreach = fmt.Sprintf("%d（漏看 %d）", r.Check.Overreach, r.Check.Missed)
	}
	if ex := r.Extract; ex != nil {
		g.dupActive, g.dupCandidates = len(ex.Duplicates), ex.SimilarCandidates
		for _, f := range ex.auditFacts() {
			if hasSecretShape(f.Text) {
				g.secretRefs = append(g.secretRefs, f.Ref)
			}
		}
		for _, e := range ex.newEpisodes() {
			if hasSecretShape(e.Text) {
				g.secretRefs = append(g.secretRefs, e.Ref)
			}
		}
	}
	m := sampleMarks(r)
	for _, row := range m {
		switch row.Type {
		case "fact":
			g.markFacts++
		case "episode":
			g.markEpisodes++
		case "missbatch":
			g.markMiss++
		}
	}
	return g
}

// markRow 是 marks.csv 的一行
type markRow struct {
	ID, Type, Content string
}

// sampleMarks 抽检样本：事实最多 50 条、片段最多 30 个（不足就全抽）、漏记抽查的批（run 里已经抽好）。抽样用 runResult 里的种子，所以同一份结果两次抽出来一样
func sampleMarks(r *runResult) []markRow {
	rng := rand.New(rand.NewSource(r.ShadowSeed))
	var out []markRow
	if ex := r.Extract; ex != nil {
		facts := ex.newFacts()
		for _, i := range pick(rng, len(facts), sampleFacts) {
			out = append(out, markRow{facts[i].Ref, "fact", facts[i].Text})
		}
		eps := ex.newEpisodes()
		for _, i := range pick(rng, len(eps), sampleEpisodes) {
			out = append(out, markRow{eps[i].Ref, "episode", eps[i].Text})
		}
	}
	for _, e := range r.Empties {
		out = append(out, markRow{fmt.Sprintf("b:%d", e.N), "missbatch", "调了模型却什么都没写的批：看报告 E 节同编号那一批的聊天行"})
	}
	return out
}

// pick 从 0..n-1 里抽 k 个（n 不够就全要），按原顺序返回
func pick(rng *rand.Rand, n, k int) []int {
	idx := rng.Perm(n)
	if k < n {
		idx = idx[:k]
	}
	sort.Ints(idx)
	return idx
}

// 填写判断时认的词
var (
	factJudgements = []string{"对", "记错了", "不该记", "范围不对"}
	missJudgements = []string{"漏记", "没漏"}
)

// csvSafe：单元格以 = + - @ 开头会被表格软件当公式执行，前面补一个单引号。内容里有号码的打码
func csvSafe(s string) string {
	s = strings.Join(strings.Fields(maskNumbers(s)), " ")
	if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
		return "'" + s
	}
	return s
}

func marksCSV(r *runResult) []byte {
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf") // UTF-8 BOM：Excel 才认中文
	w := csv.NewWriter(&buf)
	w.Write([]string{"编号", "类型", "内容", "你的判断", "备注"})
	for _, m := range sampleMarks(r) {
		w.Write([]string{m.ID, m.Type, csvSafe(m.Content), "", ""})
	}
	w.Flush()
	return buf.Bytes()
}

// ───────────── summary.json ─────────────

// summaryJSON：只有计数、条目编号、范围、状态、证据次数。会话 id 和范围里的号码只留末四位；没有聊天原文、名字、证据原话、attributed_to
func summaryJSON(r *runResult) []byte {
	type kindRow struct {
		Kind     string `json:"kind"`
		Sessions int    `json:"sessions"`
		Allowed  int    `json:"allowed"`
		Behind   int64  `json:"behind"`
		People   int    `json:"people"`
	}
	type sessRow struct {
		Conv      string `json:"conv"`
		Kind      string `json:"kind"`
		HasCursor bool   `json:"has_cursor"`
		Cursor    int64  `json:"cursor"`
		LastSeq   int64  `json:"last_seq"`
		Behind    int64  `json:"behind"`
		Batches   int    `json:"batches"`
		CapHit    bool   `json:"cap_hit,omitempty"`
		NotRun    bool   `json:"not_run,omitempty"`
		PlaceSkip bool   `json:"place_skipped,omitempty"`
		Err       string `json:"err,omitempty"`
	}
	type evSum struct {
		Occasions        int `json:"occasions"`
		BySubject        int `json:"by_subject"`
		DistinctSpeakers int `json:"distinct_speakers"`
	}
	type factSum struct {
		Ref         string   `json:"ref"`
		Kind        string   `json:"kind"`
		Subject     string   `json:"subject"` // owner / self / other（不放号码）
		Status      string   `json:"status"`
		ConfirmedBy string   `json:"confirmed_by,omitempty"`
		Scopes      []string `json:"scopes"`
		Evidence    evSum    `json:"evidence"`
		Action      string   `json:"action"`
		Confirmed   bool     `json:"confirmed,omitempty"`
		WantsSup    bool     `json:"wants_supersede,omitempty"`
	}
	type epSum struct {
		Ref    string   `json:"ref"`
		Scopes []string `json:"scopes"`
	}
	type protSum struct {
		Source  string `json:"source"`
		Subject string `json:"subject"`
		Count   int    `json:"count"`
	}
	out := map[string]any{
		"schema":      1,
		"note":        "只有计数、编号、范围、状态、证据次数；没有聊天原文、名字、证据原话。完整报告 report.md 只由主人自己打开。",
		"mode":        string(r.Mode),
		"char":        r.Char,
		"cursor_mode": r.CursorMode,
		"places":      r.Places,
		"prompt":      map[string]string{"source": r.PromptSource, "sha": r.PromptSHA},
		"batches":     len(r.Batches),
		"model_calls": r.Calls, "prompt_runes": r.PromptRunes, "reply_runes": r.ReplyRunes,
		"call_cap": r.CallCap, "truncated": r.Truncated,
		"counts":        r.Counts,
		"empty_batches": map[string]int{"total": r.EmptyTotal, "sampled": len(r.Empties)},
		"seed":          r.ShadowSeed,
	}
	if r.Snap != nil {
		out["snapshot"] = map[string]any{"files": len(r.Snap.Files), "attempts": r.Snap.Attempts, "elapsed_ms": r.Snap.Elapsed.Milliseconds(), "integrity_ok": r.Snap.IntegrityOK}
	}
	var kinds []kindRow
	for _, k := range summarizeKinds(r.Sessions) {
		kinds = append(kinds, kindRow{kindKey(k.Kind), k.Sessions, k.Allowed, k.Behind, k.People})
	}
	out["by_kind"] = kinds
	var sess []sessRow
	for _, s := range r.Sessions {
		if s.Allowed {
			sess = append(sess, sessRow{maskNumbers(s.Conv), kindKey(s.Kind), s.HasCursor, s.Cursor, s.LastSeq, s.Behind, s.Batches, s.CapHit, s.NotRun, s.PlaceSkipped, s.Err})
		}
	}
	out["sessions"] = sess
	if ex := r.Extract; ex != nil {
		var facts []factSum
		for _, f := range ex.newFacts() {
			speakers := map[string]bool{}
			bySubj := 0
			for _, e := range f.Evidence {
				speakers[e.Speaker] = true
				if e.BySubject {
					bySubj++
				}
			}
			subj := "other"
			switch f.Subject {
			case "owner", "self":
				subj = f.Subject
			}
			facts = append(facts, factSum{f.Ref, f.Kind, subj, f.Status, f.ConfirmedBy, maskAll(f.Scopes), evSum{len(f.Evidence), bySubj, len(speakers)},
				yesNo(f.AddBy == "summary", "summary", f.AddBy), f.Confirmed, f.WantsSupersede != ""})
		}
		out["new_facts"] = facts
		// 这次提升成有效的（含导入的旧候选）：只有编号、种类、主体类、范围、证据次数，没有正文
		var promoted []factSum
		for _, f := range ex.confirmedNow() {
			speakers := map[string]bool{}
			bySubj := 0
			for _, e := range f.Evidence {
				speakers[e.Speaker] = true
				if e.BySubject {
					bySubj++
				}
			}
			subj := "other"
			switch f.Subject {
			case "owner", "self":
				subj = f.Subject
			}
			promoted = append(promoted, factSum{f.Ref, f.Kind, subj, f.Status, f.ConfirmedBy, maskAll(f.Scopes), evSum{len(f.Evidence), bySubj, len(speakers)},
				yesNo(f.New, "new", "imported"), true, f.WantsSupersede != ""})
		}
		out["promoted"] = promoted
		out["sweep"] = map[string]int{"checked": r.Sweep.Checked, "confirmed": r.Sweep.Confirmed, "blocked": r.Sweep.Blocked, "skipped": r.Sweep.Skipped}
		var eps []epSum
		for _, e := range ex.newEpisodes() {
			eps = append(eps, epSum{e.Ref, maskAll(e.Scopes)})
		}
		out["new_episodes"] = eps
		var prot []protSum
		keys := make([]protectedStat, 0, len(ex.Protected))
		for k := range ex.Protected {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].Source+keys[i].Subject < keys[j].Source+keys[j].Subject })
		for _, k := range keys {
			prot = append(prot, protSum{k.Source, k.Subject, ex.Protected[k]})
		}
		out["protected"] = prot
		out["imported_candidates"] = map[string]int{"total": ex.ImportedCandidates, "in_top20": ex.ImportedInTop20}
	}
	g := goalNumbers(r)
	out["g"] = map[string]any{"duplicates_active": g.dupActive, "similar_candidates": g.dupCandidates, "secret_shape_hits": len(g.secretRefs),
		"marks": map[string]int{"facts": g.markFacts, "episodes": g.markEpisodes, "missbatches": g.markMiss}}
	if r.Check != nil {
		out["self_check"] = r.Check.summary()
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return append(b, '\n')
}

func maskAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = maskNumbers(s)
	}
	return out
}
