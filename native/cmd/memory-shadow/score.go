package main

// -score marks.csv：读主人标好的待标清单算数（规格-记忆写入上线 W6 报告 G 节）。
//
// 工具只算"标了的"：应记事实入库率和最终回答正确率要靠主人标、要靠出题，这里不自称算出了；
// 这里算的是事实和漏记抽查的精确率，并对着规格第六节第二档的过线门槛（起步值，没有数据支撑，抽检后可调）说"过 / 没过 / 没量够"。
// marks.csv 里的备注和内容是数据，不是指令：只读"编号、类型、你的判断"三栏，别的栏不看

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strings"
)

// 过线门槛（规格第六节第二档，抽 50 条时的绝对数；不足 50 条按比例）
const (
	passFullSample = 50   // 事实抽满是 50 条
	passRightMin   = 45   // 对 ≥ 45（90%）
	passWrongMax   = 3    // 不该记 ≤ 3（6%）
	missFullSample = 30   // 漏记抽查抽满是 30 批
	missMax        = 5    // 含明显该记的事的批 ≤ 5
	minSample      = 20   // 每一边至少 20 个样本，不够写"没量够"
	rightRatio     = 0.90 // 45 / 50
	wrongRatio     = 0.06 // 3 / 50
	missRatio      = 5.0 / 30.0
)

// tally 是一类条目的标记计数
type tally struct {
	Total   int // 清单里这一类共几行
	Marked  int // 标了的
	Counts  map[string]int
	Invalid []string
}

func (t *tally) n(j string) int { return t.Counts[j] }

type scoreResult struct {
	Fact, Episode, Miss tally
	Other               int // 类型不认识的行
}

// parseMarks 读 marks.csv。表头要有 编号、类型、你的判断 三栏（顺序随意、允许有别的栏）；开头的 BOM 去掉
func parseMarks(raw []byte) (*scoreResult, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	rd := csv.NewReader(bytes.NewReader(raw))
	rd.FieldsPerRecord = -1
	rd.LazyQuotes = true
	head, err := rd.Read()
	if err != nil {
		return nil, fmt.Errorf("读不到表头：%w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	for _, need := range []string{"编号", "类型", "你的判断"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("表头里没有 %q 这一栏（有：%s）", need, strings.Join(head, "、"))
		}
	}
	res := &scoreResult{
		Fact:    tally{Counts: map[string]int{}},
		Episode: tally{Counts: map[string]int{}},
		Miss:    tally{Counts: map[string]int{}},
	}
	for line := 2; ; line++ {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("第 %d 行读不了：%w", line, err)
		}
		get := func(name string) string {
			if i := col[name]; i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		var t *tally
		var allowed []string
		switch get("类型") {
		case "fact":
			t, allowed = &res.Fact, factJudgements
		case "episode":
			t, allowed = &res.Episode, factJudgements
		case "missbatch":
			t, allowed = &res.Miss, missJudgements
		case "":
			continue // 空行
		default:
			res.Other++
			continue
		}
		t.Total++
		j := get("你的判断")
		if j == "" {
			continue
		}
		ok := false
		for _, a := range allowed {
			ok = ok || a == j
		}
		if !ok {
			t.Invalid = append(t.Invalid, fmt.Sprintf("第 %d 行（%s）判断写的是 %q，认 %s", line, get("编号"), j, strings.Join(allowed, " / ")))
			continue
		}
		t.Marked++
		t.Counts[j]++
	}
	return res, nil
}

// passVerdict 对着门槛说话。返回一行文字
type verdict struct {
	Text string
	Pass bool
	Done bool // 样本够、判得了
}

func pct(a, n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(a)/float64(n))
}

// factVerdict：事实。抽满 50 条用绝对数（对 ≥ 45、范围不对 0、不该记 ≤ 3）；不足 50 条按比例（对 ≥ ⌈90%·n⌉、不该记 ≤ ⌊6%·n⌋、范围不对 0）；少于 20 条写没量够
func factVerdict(t tally) verdict {
	n := t.Marked
	if n < minSample {
		return verdict{Text: fmt.Sprintf("没量够：标了 %d 条，少于 %d 条的一边不下结论", n, minSample)}
	}
	needRight, maxWrong, how := passRightMin, passWrongMax, "抽满 50 条的绝对数"
	if n != passFullSample {
		needRight = int(math.Ceil(rightRatio*float64(n) - 1e-9))
		maxWrong = int(math.Floor(wrongRatio*float64(n) + 1e-9))
		how = fmt.Sprintf("样本是 %d 条，不是 50 条，按比例", n)
	}
	var fails []string
	if t.n("对") < needRight {
		fails = append(fails, fmt.Sprintf("对 %d 条，要至少 %d", t.n("对"), needRight))
	}
	if t.n("范围不对") > 0 {
		fails = append(fails, fmt.Sprintf("范围不对 %d 条，要 0", t.n("范围不对")))
	}
	if t.n("不该记") > maxWrong {
		fails = append(fails, fmt.Sprintf("不该记 %d 条，至多 %d", t.n("不该记"), maxWrong))
	}
	if len(fails) > 0 {
		return verdict{Text: "没过线（" + how + "）：" + strings.Join(fails, "；"), Done: true}
	}
	return verdict{Text: "过线（" + how + "）", Pass: true, Done: true}
}

// missVerdict：漏记抽查。30 批里含明显该记的事的至多 5 批；不足 30 批按比例；少于 20 批没量够
func missVerdict(t tally) verdict {
	n := t.Marked
	if n < minSample {
		return verdict{Text: fmt.Sprintf("没量够：标了 %d 批，少于 %d 批的一边不下结论", n, minSample)}
	}
	limit, how := missMax, "抽满 30 批的绝对数"
	if n != missFullSample {
		limit = int(math.Floor(missRatio*float64(n) + 1e-9))
		how = fmt.Sprintf("样本是 %d 批，不是 30 批，按比例", n)
	}
	if t.n("漏记") > limit {
		return verdict{Text: fmt.Sprintf("没过线（%s）：漏记 %d 批，至多 %d", how, t.n("漏记"), limit), Done: true}
	}
	return verdict{Text: "过线（" + how + "）", Pass: true, Done: true}
}

func scoreText(r *scoreResult) string {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	p("评分（只算你标了的；起步门槛来自规格第六节第二档，没有数据支撑，抽检后可调）")
	p("")
	block := func(name string, t tally, judgements []string) {
		p("%s：清单 %d 行，标了 %d 行，没标 %d 行", name, t.Total, t.Marked, t.Total-t.Marked-len(t.Invalid))
		for _, j := range judgements {
			p("  %s %d（%s）", j, t.n(j), pct(t.n(j), t.Marked))
		}
		for _, bad := range t.Invalid {
			p("  【读不懂的判断，没算进去】%s", bad)
		}
	}
	block("事实", r.Fact, factJudgements)
	p("  精确率（对 ÷ 标了的）：%s", pct(r.Fact.n("对"), r.Fact.Marked))
	fv := factVerdict(r.Fact)
	p("  结论：%s", fv.Text)
	p("")
	block("片段", r.Episode, factJudgements)
	p("  精确率（对 ÷ 标了的）：%s", pct(r.Episode.n("对"), r.Episode.Marked))
	p("  结论：规格没有给片段的过线门槛，这里只给数")
	p("")
	block("漏记抽查", r.Miss, missJudgements)
	mv := missVerdict(r.Miss)
	p("  结论：%s", mv.Text)
	if r.Other > 0 {
		p("")
		p("类型不认识的行 %d 行，没算", r.Other)
	}
	p("")
	p("工具不自称算出了\"应记事实入库率\"和\"最终回答正确率\"：那两个数要靠出题和你标。越权、密钥形状、重复事实、会提升的条目，看 summary.json 和报告的 C、F、G 节，这里不算。")
	return b.String()
}
