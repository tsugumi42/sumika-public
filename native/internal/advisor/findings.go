package advisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"sumika-native/internal/kernel"
)

type modelFinding struct {
	Severity string `json:"severity"`
	Text     string `json:"text"`
	Evidence []int  `json:"evidence"`
}
type criterionResult struct {
	Criterion string `json:"criterion"`
	Status    string `json:"status"`
	Evidence  []int  `json:"evidence"`
}
type response struct {
	Status   string            `json:"status"`
	Criteria []criterionResult `json:"criteria"`
	Findings []modelFinding    `json:"findings"`
}

func normalize(text string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(norm.NFKC.String(text)), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}
func (s *Service) parse(r kernel.ReviewRequest, x snapshot, text string, verify bool) (kernel.Verdict, error) {
	var body response
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		return kernel.Verdict{}, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return kernel.Verdict{}, fmt.Errorf("审阅正文不是单个 JSON")
	}
	if len(body.Findings) > s.cfg.Advisor.MaxFindings {
		return kernel.Verdict{}, fmt.Errorf("审阅 finding 超额，含 blocker")
	}
	v := kernel.Verdict{CheckpointID: r.CheckpointID, Status: body.Status}
	refs := func(indices []int) ([]kernel.Evidence, error) {
		out := make([]kernel.Evidence, 0, len(indices))
		for _, n := range indices {
			if n < 0 || n >= len(x.Evidence) {
				return nil, fmt.Errorf("虚构证据索引")
			}
			p := x.Evidence[n]
			found := false
			for _, e := range r.Evidence {
				if e.Ref.ID == p.ID && e.Start == p.Start && e.End == p.End && e.Claim == p.Claim {
					out = append(out, e)
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("证据快照不匹配")
			}
		}
		return out, nil
	}
	seen := map[string]int{}
	for _, f := range body.Findings {
		if f.Severity != "nit" && f.Severity != "concern" && f.Severity != "blocker" {
			return v, fmt.Errorf("未知严重度")
		}
		normalized := normalize(f.Text)
		if normalized == "" {
			return v, fmt.Errorf("空 finding")
		}
		evidence, err := refs(f.Evidence)
		if err != nil {
			return v, err
		}
		keys := make([]string, 0, len(evidence))
		for _, e := range evidence {
			b, _ := json.Marshal(e)
			keys = append(keys, string(b))
		}
		sort.Strings(keys)
		key := r.Invocation.RootID + "\x00" + r.CheckpointID + "\x00" + normalized + "\x00" + strings.Join(keys, "\x00")
		h := sha256.Sum256([]byte(key))
		id := hex.EncodeToString(h[:])
		if index, ok := seen[id]; ok {
			rank := map[string]int{"nit": 0, "concern": 1, "blocker": 2}
			if rank[f.Severity] > rank[v.Findings[index].Severity] {
				v.Findings[index].Severity = f.Severity
			}
		} else {
			seen[id] = len(v.Findings)
			v.Findings = append(v.Findings, kernel.Finding{ID: id, Severity: f.Severity, Text: f.Text, Evidence: evidence})
		}
	}
	if !verify {
		return v, nil
	}
	switch v.Status {
	case "done", "continue", "blocked", "partial":
	default:
		return v, fmt.Errorf("未知 verdict")
	}
	if len(body.Criteria) != len(r.Criteria) {
		return v, fmt.Errorf("缺逐项验收结果")
	}
	criteria := map[string]bool{}
	for _, c := range r.Criteria {
		if c == "" || criteria[c] {
			return v, fmt.Errorf("验收为空或重复")
		}
		criteria[c] = true
	}
	for _, c := range body.Criteria {
		if !criteria[c.Criterion] {
			return v, fmt.Errorf("验收不匹配或重复")
		}
		delete(criteria, c.Criterion)
		evidence, err := refs(c.Evidence)
		if err != nil {
			return v, err
		}
		switch c.Status {
		case "supported":
			if len(evidence) == 0 {
				return v, fmt.Errorf("验收无证据")
			}
		case "missing", "blocked":
			if v.Status == "done" {
				return v, fmt.Errorf("未通过验收不能 done")
			}
		default:
			return v, fmt.Errorf("未知验收结果")
		}
	}
	if v.Status == "done" {
		for _, f := range v.Findings {
			if f.Severity != "nit" {
				v.Status = "continue"
				break
			}
		}
	}
	if x.Partial && v.Status == "done" {
		v.Status = "partial"
	}
	return v, nil
}
