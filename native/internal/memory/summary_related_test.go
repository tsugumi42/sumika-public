package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSummaryFindsOldRelatedFactsOutsideRecentWindow(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune", rigOpts{seed: func(db *sql.DB) {
		seedFact(t, db, "FOLD", "备用键盘放在书房抽屉", "owner", 1)
		seedCand(t, db, "COLD", "主人曾提到机械键盘偏好", "owner", "owner", 1)
		seedFact(t, db, "EXPIRED", "备用键盘是假的过期记录", "owner", 1)
		if _, err := db.Exec(`UPDATE facts SET temporal='temporary', valid_to=1 WHERE id='EXPIRED'`); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 50; i++ {
			seedFact(t, db, fmt.Sprintf("F%03d", i), fmt.Sprintf("新话题%d种花", i), "owner", int64(i+2))
		}
		for i := 0; i < 25; i++ {
			seedCand(t, db, fmt.Sprintf("C%03d", i), fmt.Sprintf("新话题%d烘焙", i), "owner", "owner", int64(i+2))
		}
	}})
	r.say(ownerA, "备用键盘还在书房抽屉，机械键盘偏好也没变。")
	r.reply("no", "")
	r.run()
	prompt := r.gen.last().prompt
	known := section(prompt, "已知事实：\n", "\n\n听说过、还没确认的")
	cand := section(prompt, "听说过、还没确认的：\n", "\n\n说话的人")
	if !strings.HasPrefix(known, "FOLD｜") || !strings.HasPrefix(cand, "COLD｜") {
		t.Fatalf("old related facts missing: %s / %s", known, cand)
	}
	if strings.Contains(known, "EXPIRED") || len(strings.Split(strings.TrimSpace(known), "\n")) != maxKnown || len(strings.Split(strings.TrimSpace(cand), "\n")) != maxCandidateBlock {
		t.Fatal("expiry or budgets changed")
	}
	if r.gen.count() != 1 {
		t.Fatal("retrieval added a generation call")
	}
}

type unavailableSummaryEmbedding struct {
	t     *testing.T
	calls int
}

func (e *unavailableSummaryEmbedding) Model() string { return "test-unavailable" }
func (e *unavailableSummaryEmbedding) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.calls++
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > summaryRecallTimeout {
		e.t.Fatal("embedding missing bounded deadline")
	}
	if len(texts) != 1 || len([]rune(texts[0])) > summaryQueryRunes {
		e.t.Fatal("unbounded summary query")
	}
	return nil, errors.New("test embedding unavailable")
}

func TestSummaryRecallFallsBackWithoutRerank(t *testing.T) {
	t.Parallel()
	r := newRig(t, "console:suzune", rigOpts{seed: func(db *sql.DB) { seedFact(t, db, "OLD", "备用钥匙放在蓝色盒子", "owner", 1) }})
	e := &unavailableSummaryEmbedding{t: t}
	r.m.embed = e
	r.m.index.publish(e.Model(), map[string][]float32{"f:OLD": {1, 0}})
	rerank := &fakeRerank{fn: reversed}
	r.m.opt.Reranker = rerank
	r.say(ownerA, "备用钥匙放在蓝色盒子。"+strings.Repeat("今天的话题。", 300))
	r.reply("no", "")
	r.run()
	known := section(r.gen.last().prompt, "已知事实：\n", "\n\n听说过、还没确认的")
	if !strings.Contains(known, "OLD｜") || e.calls != 1 || rerank.calls != 0 {
		t.Fatalf("fallback: embed=%d rerank=%d known=%s", e.calls, rerank.calls, known)
	}
}
