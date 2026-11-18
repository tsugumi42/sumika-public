package linkplugin

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktranslate"
	"sumika-native/internal/taskrun"
)

// 每次没用上译文都记一行带原因标签的插件日志（ADR-391）；没配模型不记。
func TestTranslateSkipsAreLoggedWithReason(t *testing.T) {
	var rows []taskrun.Row
	var logs []string
	d := &taskrun.Degrader{
		Sink: rowSink(func(r taskrun.Row) { rows = append(rows, r) }),
		Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
	}
	p := &Plugin{opt: Options{Degrade: d}}
	run := &expansionRun{ctx: context.Background()}
	for _, c := range []struct {
		prefix string
		out    linktranslate.Outcome
	}{
		{"", linktranslate.Outcome{Status: linktranslate.StatusSkipped, Reason: linktranslate.SkipTooShort}},
		{"", linktranslate.Outcome{Status: linktranslate.StatusSkipped, Reason: linktranslate.SkipAlreadyTarget}},
		{"article:", linktranslate.Outcome{Status: linktranslate.StatusSkipped, Reason: linktranslate.SkipTooLong}},
		{"quote:", linktranslate.Outcome{Status: linktranslate.StatusUnneeded, Reason: linktranslate.WhyUnneeded}},
		{"", linktranslate.Outcome{Status: linktranslate.StatusUnneeded, Reason: "cached"}},
		{"", linktranslate.Outcome{Status: linktranslate.StatusFailed, Reason: "error"}},
		{"article:", linktranslate.Outcome{Status: linktranslate.StatusTimeout, Reason: "timeout"}},
		{"embed:", linktranslate.Outcome{Status: linktranslate.StatusFailed, Reason: linktranslate.SegmentFailure}},
	} {
		ch := make(chan linktranslate.Outcome, 1)
		ch <- c.out
		beforeRows, beforeLogs := len(rows), len(logs)
		if got := p.takeTranslation(run, ch, c.prefix); got != "" {
			t.Fatalf("没有译文应返回空串：%q", got)
		}
		if len(rows) != beforeRows+1 || len(logs) != beforeLogs+1 {
			t.Fatalf("%q：应记一行日志和一条运行记录，行 %d 日志 %d", c.out.Reason, len(rows)-beforeRows, len(logs)-beforeLogs)
		}
		if r := rows[len(rows)-1]; r.Module != taskrun.ModuleLink || r.Class != taskrun.ClassTranslate {
			t.Fatalf("%q：运行记录不对：%+v", c.out.Reason, r)
		}
		if line := logs[len(logs)-1]; !strings.Contains(line, c.prefix+c.out.Reason) || strings.Contains(line, "原文") {
			t.Fatalf("%q：日志标签不对或带了原文：%q", c.out.Reason, line)
		}
	}
	// 没配模型：不算失败，不记
	ch := make(chan linktranslate.Outcome, 1)
	ch <- linktranslate.Outcome{Status: linktranslate.StatusSkipped, Reason: "no_model"}
	beforeRows, beforeLogs := len(rows), len(logs)
	if got := p.takeTranslation(run, ch, ""); got != "" || len(rows) != beforeRows || len(logs) != beforeLogs {
		t.Fatalf("no_model 不该记：%q，行 %d 日志 %d", got, len(rows)-beforeRows, len(logs)-beforeLogs)
	}
}

// 译文结构对不上（投票选项行数、长文段数、被引长文段数）也各记一行带原因标签的日志。
func TestTranslateProblemLogsAreTagged(t *testing.T) {
	var logs []string
	d := &taskrun.Degrader{Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }}
	for _, why := range []string{"poll_lines", "article_sections", "quote_article_sections"} {
		d.Degrade(taskrun.ModuleLink, taskrun.ClassTranslate, nil, taskrun.Opts{Outcome: taskrun.OutcomeSkipped, Tag: why})
		if line := logs[len(logs)-1]; !strings.Contains(line, why) {
			t.Fatalf("%s：日志没有原因标签：%q", why, line)
		}
	}
}

// 旧键 forwardOver 还写着值时提示一次改用 forwardOverBytes（ADR-391），只提示一次。
func TestLegacyForwardOverWarnsOnce(t *testing.T) {
	var logs []string
	p := &Plugin{opt: Options{Warnf: func(format string, args ...any) {
		logs = append(logs, format)
	}}}
	n := 600
	o := config.LinkExpand{Sites: map[string]config.LinkSite{"x": {ForwardOver: &n}}}
	p.noteLegacyForwardOver(o)
	p.noteLegacyForwardOver(o)
	if len(logs) != 1 || !strings.Contains(logs[0], "forwardOverBytes") {
		t.Fatalf("应只提示一次并指出新键：%v", logs)
	}
	// 只写了新键：不提示
	p2 := &Plugin{opt: Options{Warnf: func(format string, args ...any) { t.Fatal("不该提示") }}}
	p2.noteLegacyForwardOver(config.LinkExpand{Sites: map[string]config.LinkSite{"x": {ForwardOverBytes: &n}}})
}

// 长文里嵌的推文最多同时翻 4 条，其余的排队；都完成后译文都在（ADR-391）。
func TestEmbedTranslateConcurrencyLimit(t *testing.T) {
	const embeds = 10
	started := make(chan struct{}, embeds)
	release := make(chan struct{})
	var running, peak atomic.Int32
	tr := func(ctx context.Context, _, text string, _ config.LinkTranslate) linktranslate.Outcome {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		running.Add(-1)
		return linktranslate.Outcome{Text: "译" + text, Status: linktranslate.StatusOK}
	}
	p := &Plugin{opt: Options{Expand: &ExpandOptions{Translate: tr}}}
	av := func(s string) linkreader.Field[string] { return linkreader.Field[string]{State: "available", Value: s} }
	r := linkreader.Result{Platform: "x", Kind: "article", ID: "90001", CleanURL: "https://example.invalid/x/90001"}
	for i := range embeds {
		q := linkreader.Result{Platform: "x", Kind: "post", ID: fmt.Sprintf("91%03d", i), Text: av(fmt.Sprintf("embed %d", i))}
		q.Blocks = []linkreader.Block{{Type: "text", Text: q.Text.Value}}
		r.Blocks = append(r.Blocks, linkreader.Block{Type: "quote", Quote: &q})
	}
	j := job{snapshot: Snapshot{Settings: config.DefaultLinks()}, run: &expansionRun{ctx: context.Background()}}
	trn := p.startTranslate(j, &r, nil)
	if trn == nil || len(trn.embeds) != embeds {
		t.Fatalf("应开始翻译 %d 条嵌入推文：%+v", embeds, trn)
	}
	for range maxEmbedTranslations {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("4 条没有都开始")
		}
	}
	select {
	case <-started:
		t.Fatalf("同时开始了超过 %d 条", maxEmbedTranslations)
	case <-time.After(100 * time.Millisecond):
	}
	if peak.Load() != maxEmbedTranslations {
		t.Fatalf("峰值并发 %d，要 %d", peak.Load(), maxEmbedTranslations)
	}
	close(release)
	for i, ch := range trn.embeds {
		select {
		case o := <-ch:
			if o.Text == "" {
				t.Fatalf("第 %d 条没有译文：%+v", i+1, o)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("第 %d 条没完成", i+1)
		}
	}
}
