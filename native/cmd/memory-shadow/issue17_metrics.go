package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/memory"
)

type issue17MatrixRow struct {
	Ref, Destination string
	Expected, Found  bool
}
type issue17Raw struct {
	Name, Character, Conversation, Keywords, Text string
	Pass                                          bool
}
type issue17Metrics struct {
	ExpectedFacts, StoredValueMatches, PositiveQuestions, RecallHits, AnswerValueMatches                          int
	CorrectionQuestions, CorrectionValueMatches, NegativeQuestions, LeakQuestions, RoleQuestions, SharedQuestions int
	PendingHuman                                                                                                  int
	RecallAt8Diagnostic                                                                                           float64
	WarmN                                                                                                         int
	WarmP50MS, WarmP95MS, GroupMeanRunes                                                                          float64
	GroupN, SourceLabelsMissing                                                                                   int
	CrossSceneChecks, CrossSceneMissing, SharedRecallMissing                                                      int
	ExactLeakValues, ExactRoleLeaks                                                                               int
}

func issue17Measure(r *issue17Round) {
	latencies := append([]float64(nil), r.LatencyMS...)
	sort.Float64s(latencies)
	m := issue17Metrics{WarmN: len(latencies), WarmP50MS: issue17Percentile(latencies, 0.5), WarmP95MS: issue17Percentile(latencies, 0.95)}
	for _, c := range r.Checks {
		if strings.HasPrefix(c.Name, "stored-diagnostic:") {
			m.ExpectedFacts++
			if c.Pass {
				m.StoredValueMatches++
			}
		}
	}
	totalRunes := 0
	for _, a := range r.Answers {
		m.PendingHuman++
		switch a.Question.Kind {
		case "positive":
			m.PositiveQuestions++
			if a.Hit {
				m.RecallHits++
			}
			if a.DiagnosticMatch {
				m.AnswerValueMatches++
			}
		case "correction":
			m.CorrectionQuestions++
			if a.DiagnosticMatch {
				m.CorrectionValueMatches++
			}
		case "negative":
			m.NegativeQuestions++
		case "leak":
			m.LeakQuestions++
			if a.Question.Old != "" && strings.Contains(a.Answer, a.Question.Old) {
				m.ExactLeakValues++
			}
		case "role":
			m.RoleQuestions++
			if a.Question.Old != "" && strings.Contains(a.Answer, a.Question.Old) {
				m.ExactRoleLeaks++
			}
		case "shared":
			m.SharedQuestions++
			if !a.Hit {
				m.SharedRecallMissing++
			}
		}
		if strings.HasPrefix(a.Question.Conversation, "qq:group:") {
			m.GroupN++
			totalRunes += issue17Runes(a.MemoryBlock)
		}
		for _, h := range a.Hits {
			if h.Conversation == "" || !strings.Contains(a.MemoryBlock, h.Conversation) {
				m.SourceLabelsMissing++
			}
		}
	}
	if m.PositiveQuestions > 0 {
		m.RecallAt8Diagnostic = float64(m.RecallHits) / float64(m.PositiveQuestions)
	}
	if m.GroupN > 0 {
		m.GroupMeanRunes = float64(totalRunes) / float64(m.GroupN)
	}
	m.CrossSceneChecks = len(r.Matrix)
	for _, row := range r.Matrix {
		if row.Expected && !row.Found {
			m.CrossSceneMissing++
		}
	}
	r.Metrics = m
	if m.WarmN > 0 {
		r.Checks = append(r.Checks, issue17Check{"hot_recall_p95", m.WarmP95MS < 1000, fmt.Sprintf("n=%d P50=%.3fms P95=%.3fms；含实际嵌入调用", m.WarmN, m.WarmP50MS, m.WarmP95MS)})
	}
	if m.GroupN > 0 {
		r.Checks = append(r.Checks, issue17Check{"group_injection_budget", m.GroupMeanRunes < 400, fmt.Sprintf("n=%d 平均 %.1f 字；含标题和来源标识的实际块", m.GroupN, m.GroupMeanRunes)})
	}
	r.Checks = append(r.Checks, issue17Check{"adr242_source_labels", m.SourceLabelsMissing == 0, fmt.Sprintf("缺来源会话标注的命中=%d；不得由夹具补上伪装成生产已实现", m.SourceLabelsMissing)})
	if m.CrossSceneChecks > 0 {
		r.Checks = append(r.Checks, issue17Check{"adr242_cross_scene", m.CrossSceneMissing == 0, fmt.Sprintf("应召回 %d 格，缺 %d 格；同角色跨场合缺失不算安全通过", m.CrossSceneChecks, m.CrossSceneMissing)})
	}
	if m.SharedQuestions > 0 {
		r.Checks = append(r.Checks, issue17Check{"adr162_shared_recall", m.SharedRecallMissing == 0, fmt.Sprintf("主人共用事实题 %d 道，缺 %d 道；专属隔离另判", m.SharedQuestions, m.SharedRecallMissing)})
	}
	if m.PositiveQuestions > 0 {
		r.Checks = append(r.Checks, issue17Check{"recall_at8_diagnostic", m.RecallAt8Diagnostic >= 0.9, fmt.Sprintf("预期值命中 %d/%d；关键词诊断，不能代替人工语义判题", m.RecallHits, m.PositiveQuestions)})
	}
}

// 跨场合预期手写“都可召回”，不调用 AllowedScopes / CanSee 生成预期。
func issue17Matrix(ctx context.Context, w *issue17World, r *issue17Round) {
	for _, it := range w.mems[issue17Character].Items() {
		if it.Type != "fact" {
			continue
		}
		for i, conv := range issue17Convs {
			destination := issue17ExamConv(conv, 90+i)
			rec := w.mems[issue17Character].Recall(ctx, issue17Audience(destination, "owner"), memory.Query{Text: it.Text, Addressed: true, Now: time.Now()})
			found := false
			for _, h := range rec.Hits {
				if h.Ref == it.Ref {
					found = true
				}
			}
			r.Matrix = append(r.Matrix, issue17MatrixRow{Ref: it.Ref, Destination: destination, Expected: true, Found: found})
		}
	}
}
func issue17RawChecks(ctx context.Context, w *issue17World, r *issue17Round) error {
	// 一个完全未被任何角色观察的事件，另一个只属于另一角色；都不能被原话工具猜出来。
	for _, e := range []struct {
		char, text string
		seen       bool
	}{{issue17Character, "未见暗号雾笛", false}, {issue17Other, "另一角色原话暗号星笛", true}} {
		if _, err := w.append(e.char, "console:"+e.char+":topic:raw", "owner", e.text, e.seen); err != nil {
			return err
		}
	}
	cases := []struct{ name, char, conv, actor, key, want, forbid string }{
		{"local_private", issue17Character, issue17Convs[0], "owner", "云豆", "云豆", ""},
		{"local_group", issue17Character, issue17Convs[1], "owner", "青石八七", "青石八七", ""},
		{"qq_private", issue17Character, issue17Convs[2], "owner", "桂花乌龙", "桂花乌龙", ""},
		{"qq_group_peer_identity", issue17Character, issue17Convs[3], "qq:505", "栗球", "群友", "主人：我是编造群友甲"},
		{"unseen", issue17Character, issue17Convs[0], "owner", "未见暗号雾笛", "", "未见暗号雾笛"},
		{"other_role", issue17Character, issue17Convs[0], "owner", "星笛", "", "星笛"},
	}
	for _, c := range cases {
		a := issue17Audience(c.conv, c.actor)
		got, err := w.mems[c.char].RecallRaw(ctx, a, c.key)
		if err != nil {
			return err
		}
		pass := (c.want == "" || strings.Contains(got.Text, c.want)) && (c.forbid == "" || !strings.Contains(got.Text, c.forbid))
		r.Raw = append(r.Raw, issue17Raw{c.name, c.char, c.conv, c.key, got.Text, pass})
		r.Checks = append(r.Checks, issue17Check{"raw:" + c.name, pass, "原话 observations、身份和来源单独判，不套用长期记忆跨场合预期"})
	}
	// 验 source_seqs 确实指向本轮该角色观察的事件；不拿正文相似当来源正确。
	tables, err := issue17MemoryTables(w, issue17Character)
	if err != nil {
		return err
	}
	invalid := 0
	for _, f := range tables["facts"] {
		var seqs []int64
		if err = json.Unmarshal([]byte(fmt.Sprint(f["source_seqs"])), &seqs); err != nil {
			return err
		}
		if len(seqs) == 0 {
			invalid++
			continue
		}
		for _, seq := range seqs {
			found := false
			for _, e := range w.events {
				char := issue17Character
				if learned, ok := f["learned_by"].(string); ok && learned != "" {
					char = learned
				}
				if e.Character == char && e.Conversation == f["conversation"] && e.Seq == seq {
					found = true
				}
			}
			if !found {
				invalid++
			}
		}
	}
	r.Checks = append(r.Checks, issue17Check{"event_source_refs", invalid == 0, fmt.Sprintf("无对应角色观察事件的引用=%d", invalid)})
	return nil
}
