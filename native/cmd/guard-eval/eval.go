package main

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"

	"sumika-native/internal/audience"
	"sumika-native/internal/memory"
)

type Memory struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Status string   `json:"status"`
	Scope  []string `json:"scope"`
	Text   string   `json:"text"`
}
type Sample struct {
	ID          string            `json:"id"`
	Category    string            `json:"category"`
	Ref         string            `json:"ref"`
	Context     string            `json:"context"`
	Audience    audience.Audience `json:"audience"`
	Parts       []string          `json:"parts"`
	ShouldBlock bool              `json:"should_block"`
}
type Dataset struct {
	Character string   `json:"character"`
	Memories  []Memory `json:"memories"`
	Samples   []Sample `json:"samples"`
}

// 范围沿用生产的纯函数；量测不打开任何记忆库，也不改默认可见性。
func forbidden(ds Dataset, s Sample) []Memory {
	allowed := memory.DisclosureScopes(s.Audience)
	var out []Memory
	for _, m := range ds.Memories {
		if (m.Status == "active" || (m.Kind == "fact" && m.Status == "candidate")) && !memory.CanSee(m.Scope, allowed) {
			out = append(out, m)
		}
	}
	return out
}

// 字面基线严格照 Bun leakedBy：空白、标点和符号去掉，大小写不折叠。
func normalize(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return -1
		}
		return r
	}, text)
}
func literal(text string, memories []Memory) bool {
	text = normalize(text)
	for _, m := range memories {
		chars := []rune(normalize(m.Text))
		if len(chars) < 6 {
			continue
		}
		n := min(10, len(chars))
		for i := 0; i+n <= len(chars); i++ {
			if strings.Contains(text, string(chars[i:i+n])) {
				return true
			}
		}
	}
	return false
}

type Evidence struct {
	Text      string  `json:"text"`
	Literal   bool    `json:"literal"`
	Score     float64 `json:"score"`
	Yes       bool    `json:"yes"`
	Asked     bool    `json:"asked"`
	LiteralMS float64 `json:"literal_ms"`
	EmbedMS   float64 `json:"embed_ms"`
	ModelMS   float64 `json:"model_ms"`
}
type Decision struct {
	Blocked           bool      `json:"blocked"`
	Calls             int       `json:"calls"`
	EmbedCalls        int       `json:"embed_calls"`
	MS                float64   `json:"ms"`
	SegmentMS         []float64 `json:"segment_ms"`
	PrefixFalseBlocks int       `json:"prefix_false_blocks"`
}

// reaches 调线上守卫用的同一个比较（memory.GuardReaches：得分本身是 float32，按 float32 比，"正好 0.70"算过门槛）。
func reaches(score, threshold float64) bool { return memory.GuardReaches(float32(score), threshold) }

// 每一档门槛都重新走放行状态；只把已放行的段接到后面，不能用最终全文代替。
func decide(s Sample, method string, threshold float64, get func(string) (Evidence, error)) (Decision, error) {
	var d Decision
	passed := make([]string, 0, len(s.Parts))
	for i, part := range s.Parts {
		before := d.MS
		full := strings.Join(append(passed, part), "\n")
		e, err := get(full)
		if err != nil {
			return d, err
		}
		block := false
		switch method {
		case "A":
			block = e.Literal
			d.MS += e.LiteralMS
		case "B":
			block = e.Yes
			if e.Asked {
				d.Calls++
			}
			d.MS += e.ModelMS
		case "C":
			d.MS += e.LiteralMS
			if e.Literal {
				block = true
			} else if reaches(e.Score, threshold) {
				if !e.Asked {
					return d, fmt.Errorf("缺少门槛 %.2f 的确认结果", threshold)
				}
				block = e.Yes
				d.Calls++
			}
			if !e.Literal && e.Score >= 0 {
				d.EmbedCalls++
				d.MS += e.EmbedMS
				if reaches(e.Score, threshold) {
					d.MS += e.ModelMS
				}
			}
		default:
			return d, fmt.Errorf("未知方法 %q", method)
		}
		d.SegmentMS = append(d.SegmentMS, d.MS-before)
		if i == len(s.Parts)-1 {
			d.Blocked = block
		} else if block {
			d.PrefixFalseBlocks++
		}
		if !block {
			passed = append(passed, part)
		}
	}
	return d, nil
}

type Counts struct {
	TP, Pos, FP, Neg, Calls, EmbedCalls, PrefixFalseBlocks int
	P50, P95                                               float64
}

func summarize(samples []Sample, decisions []Decision) Counts {
	var c Counts
	delays := make([]float64, 0, len(decisions))
	for i, d := range decisions {
		if samples[i].ShouldBlock {
			c.Pos++
			if d.Blocked {
				c.TP++
			}
		} else {
			c.Neg++
			if d.Blocked {
				c.FP++
			}
		}
		c.Calls += d.Calls
		c.EmbedCalls += d.EmbedCalls
		c.PrefixFalseBlocks += d.PrefixFalseBlocks
		delays = append(delays, d.SegmentMS...)
	}
	c.P50, c.P95 = percentile(delays, .50), percentile(delays, .95)
	return c
}
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[max(0, int(math.Ceil(float64(len(sorted))*p))-1)]
}
