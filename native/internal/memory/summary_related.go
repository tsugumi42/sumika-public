package memory

import (
	"context"
	"sort"
	"strings"
	"time"
)

const summaryQueryRunes = 1200
const summaryRecallTimeout = time.Second

// relatedSummaryFacts 不再调用生成模型；复用全文/向量融合，失败仍有最近事实退路。
func (m *Memory) relatedSummaryFacts(ctx context.Context, a Audience, b batch) []Hit {
	var texts []string
	for _, ke := range b.kept {
		if ke.ev.ActorKind == "owner" || ke.ev.ActorKind == "human" {
			texts = append(texts, ke.body)
		}
	}
	query := strings.TrimSpace(strings.Join(texts, "\n"))
	if query == "" {
		return nil
	}
	if runes := []rune(query); len(runes) > summaryQueryRunes {
		query = string(runes[:summaryQueryRunes/2]) + "\n" + string(runes[len(runes)-(summaryQueryRunes/2-1):])
	}
	qctx, cancel := context.WithTimeout(ctx, summaryRecallTimeout)
	defer cancel()
	recalled := m.Recall(qctx, a, Query{Text: query, Mode: modeSummary, TopK: maxKnown + maxCandidateBlock, Now: m.now()})
	return recalled.Hits
}

// 保留每个类别的原上限；相关项在前，剩余名额按原最近顺序补足。
// 只按被索引的真实文字/向量命中提优先级，人物别名本身不代表相关话题。
func (m *Memory) summaryFacts(a Audience, status string, related []Hit) []Item {
	facts := m.visibleFacts(a, status)
	ranks := map[string]int{}
	for i, h := range related {
		if h.Lexical > 0 || h.Vector > 0 {
			ranks[h.Ref] = i + 1
		}
	}
	kept := facts[:0]
	now := m.now()
	for _, it := range facts {
		if !it.Expired(now) {
			kept = append(kept, it)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		x, y := ranks[kept[i].Ref], ranks[kept[j].Ref]
		if x == 0 {
			return false
		}
		if y == 0 {
			return true
		}
		return x < y
	})
	return kept
}
