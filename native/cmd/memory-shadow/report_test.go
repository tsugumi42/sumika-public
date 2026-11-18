package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

// 长号码一律运行时拼，不把完整号码写进源码（隐私扫描和 check:docs 的 D9 都查长数字串）
func bigNum(d string) string { return strings.Repeat(d, 9) }

// 真的跑一遍（假生成器），报告里：每条新事实都有范围、证据和证据原话；漏记抽查的行只取这一批实际给过模型的行
func TestReportFromFakeRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ev(convOwner, "auto", "AUTOMARK 插件自动发的")
	f.ev(convOwner, "owner", "OWNERMARK 我下周三考试")
	f.ev(convOwner, "her", "加油")
	f.seedMem(func(db *sql.DB) {
		f.exec(db, `INSERT INTO summary_state (conversation, last_seq, updated_at) VALUES (?, 0, 1000)`, convOwner)
	})
	f.closeStores()
	res, err := runShadow(context.Background(), runConfig{Data: f.Data, Char: testChar, Mode: modeFake, Gen: &recGen{}, Ignore: ignoredAlways, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	facts := res.Extract.newFacts()
	if len(facts) != 1 {
		t.Fatalf("假生成器每批写一条候选：%d", len(facts))
	}
	rep := reportMD(res)
	for _, fa := range facts {
		if len(fa.Scopes) == 0 || len(fa.Evidence) != 1 || fa.Evidence[0].Missing || !strings.Contains(fa.Evidence[0].Quote, "OWNERMARK") {
			t.Fatalf("每条新事实都要有范围和证据原话：%+v", fa)
		}
		for _, must := range []string{fa.Ref, "范围：owner", "OWNERMARK 我下周三考试", "本人说", "第 1 个说话人"} {
			if !strings.Contains(rep, must) {
				t.Errorf("报告里应该有 %q", must)
			}
		}
	}
	if !strings.HasPrefix(strings.SplitN(rep, "\n", 3)[2], "**这份报告里有聊天原话") {
		t.Errorf("报告开头要写明谁能打开：\n%s", rep[:200])
	}
	if strings.Contains(rep, "AUTOMARK") {
		t.Error("自动消息会被整理丢掉，不该出现在报告里的证据原话")
	}
}

// 漏记抽查：模型说不记的批，报告里列出这一批给过模型的聊天行（不含被丢的自动消息），带模型写的原因
func TestReportEmptyBatchSample(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ev(convOwner, "auto", "AUTOMARK 插件自动发的")
	f.ev(convOwner, "owner", "OWNERMARK 随口一句")
	f.seedMem(func(db *sql.DB) {
		f.exec(db, `INSERT INTO summary_state (conversation, last_seq, updated_at) VALUES (?, 0, 1000)`, convOwner)
	})
	f.closeStores()
	g := &recGen{reply: func(string) string { return `{"episode_record":"no","episode_reason":"闲聊 REASONMARK","ops":[]}` }}
	res, err := runShadow(context.Background(), runConfig{Data: f.Data, Char: testChar, Mode: modeFake, Gen: g, Ignore: ignoredAlways, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	if res.EmptyTotal != 1 || len(res.Empties) != 1 {
		t.Fatalf("应该有 1 个调了模型却什么都没写的批：%d %d", res.EmptyTotal, len(res.Empties))
	}
	rep := reportMD(res)
	if !strings.Contains(rep, "OWNERMARK 随口一句") || !strings.Contains(rep, "REASONMARK") {
		t.Errorf("漏记抽查要列出聊天行和模型写的原因：\n%s", rep)
	}
	if strings.Contains(rep, "AUTOMARK") {
		t.Error("漏记抽查只取这一批实际给过模型的行，被丢的自动消息不能出现")
	}
	var marks []markRow
	for _, m := range sampleMarks(res) {
		if m.Type == "missbatch" {
			marks = append(marks, m)
		}
	}
	if len(marks) != 1 {
		t.Errorf("marks 里应该有这一批：%v", marks)
	}
}

// 号码：报告、summary.json、marks.csv 里没有完整号码，只有末四位；summary.json 里没有聊天正文
func TestReportMasksNumbersAndSummaryHasNoText(t *testing.T) {
	t.Parallel()
	big1, big2, big3 := bigNum("7"), bigNum("8"), bigNum("9")
	convBig := "qq:private:" + big1 + ":" + big2
	f := newFixture(t)
	f.ev(convBig, "owner", "SECRETTEXT 我的号是 "+big3+" 你记一下")
	f.ev(convBig, "her", "好")
	f.seedMem(func(db *sql.DB) {
		f.exec(db, `INSERT INTO summary_state (conversation, last_seq, updated_at) VALUES (?, 0, 1000)`, convBig)
	})
	f.closeStores()
	res, err := runShadow(context.Background(), runConfig{Data: f.Data, Char: testChar, Mode: modeFake, Gen: &recGen{}, Ignore: ignoredAlways, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Extract.newFacts()) != 1 {
		t.Fatalf("没有写出事实：%+v", res.Batches)
	}
	files := buildReport(res)
	for name, text := range map[string]string{"report.md": files.Report, "summary.json": string(files.Summary), "marks.csv": string(files.Marks)} {
		for _, n := range []string{big1, big2, big3} {
			if strings.Contains(text, n) {
				t.Errorf("%s 里不该有完整号码 %s", name, n)
			}
		}
	}
	if !strings.Contains(files.Report, "…"+big3[len(big3)-4:]) {
		t.Error("报告里应该留末四位")
	}
	var js map[string]any
	if err := json.Unmarshal(files.Summary, &js); err != nil {
		t.Fatalf("summary.json 不是合法的 JSON：%v", err)
	}
	if strings.Contains(string(files.Summary), "SECRETTEXT") || strings.Contains(string(files.Summary), "你记一下") {
		t.Error("summary.json 里不该有聊天正文")
	}
	nf, _ := js["new_facts"].([]any)
	if len(nf) != 1 {
		t.Fatalf("summary.json 应该有这条新事实的编号：%v", js["new_facts"])
	}
	row := nf[0].(map[string]any)
	if row["ref"] == nil || row["scopes"] == nil || row["evidence"] == nil || row["text"] != nil || row["quote"] != nil {
		t.Errorf("summary.json 的事实只有编号、范围、状态、证据次数：%v", row)
	}
}

// 原话里的 Markdown 和 HTML 不能破坏报告结构
func TestReportEscapesQuotedText(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ev(convOwner, "owner", "# 伪标题 [点我](http://x.invalid) <script>alert(1)</script> `code` | a | b ```")
	f.ev(convOwner, "owner", "第二句\n## 另一个标题\n- 列表")
	f.seedMem(func(db *sql.DB) {
		f.exec(db, `INSERT INTO summary_state (conversation, last_seq, updated_at) VALUES (?, 0, 1000)`, convOwner)
	})
	f.closeStores()
	res, err := runShadow(context.Background(), runConfig{Data: f.Data, Char: testChar, Mode: modeFake, Gen: &recGen{}, Ignore: ignoredAlways, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	rep := reportMD(res)
	for _, bad := range []string{"<script>", "[点我](", "\n# 伪标题", "\n## 另一个标题", "`code`", "```\n# "} {
		if strings.Contains(rep, bad) {
			t.Errorf("原话里的 %q 没被转义", bad)
		}
	}
	if !strings.Contains(rep, "&lt;script&gt;") {
		t.Error("尖括号应该转成实体")
	}
	// 整份报告里只有工具自己写的 ``` 围栏（A 节），数一数是成对的
	if n := strings.Count(rep, "```"); n%2 != 0 {
		t.Errorf("围栏不成对：%d", n)
	}
}

// 抽检样本：事实最多 50 条、片段最多 30 个；同一份结果抽两次一样；单元格以 = 开头的补单引号；能被 encoding/csv 读回来
func TestMarksCSVSampleAndSafety(t *testing.T) {
	t.Parallel()
	r := &runResult{ShadowSeed: 7, Extract: &extracted{}}
	for i := 0; i < 60; i++ {
		text := "事实" + strings.Repeat("甲", i%5+1)
		if i == 3 {
			text = "=cmd|' /C calc'!A0"
		}
		r.Extract.Facts = append(r.Extract.Facts, factRow{ID: strings.Repeat("a", 3) + string(rune('a'+i%26)) + string(rune('A'+i/26)), Ref: "f:" + strings.Repeat("a", 3) + string(rune('a'+i%26)) + string(rune('A'+i/26)), Text: text, New: true})
	}
	for i := 0; i < 35; i++ {
		r.Extract.Episodes = append(r.Extract.Episodes, epRow{Ref: "e:ep" + string(rune('a'+i%26)) + string(rune('A'+i/26)), Text: "片段", New: true})
	}
	a, b := sampleMarks(r), sampleMarks(r)
	nf, ne := 0, 0
	for i, m := range a {
		if m != b[i] {
			t.Fatal("同一份结果抽样两次应该一样")
		}
		switch m.Type {
		case "fact":
			nf++
		case "episode":
			ne++
		}
	}
	if nf != 50 || ne != 30 {
		t.Fatalf("事实 50、片段 30：%d %d", nf, ne)
	}
	raw := marksCSV(r)
	if !bytes.HasPrefix(raw, []byte("\xef\xbb\xbf")) {
		t.Error("应该带 BOM")
	}
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))).ReadAll()
	if err != nil || len(rows) != 81 || rows[0][3] != "你的判断" {
		t.Fatalf("读回来：%v %d", err, len(rows))
	}
	for _, row := range rows[1:] {
		if strings.HasPrefix(row[2], "=") {
			t.Errorf("以 = 开头的单元格会被当公式：%q", row[2])
		}
	}
	// 事实不足 50 就全抽
	small := &runResult{ShadowSeed: 1, Extract: &extracted{Facts: r.Extract.Facts[:12]}}
	if got := len(sampleMarks(small)); got != 12 {
		t.Errorf("不足就全抽：%d", got)
	}
}

// 密钥形状：能认出的认出来，普通的话不误报
func TestSecretShapes(t *testing.T) {
	t.Parallel()
	hex32 := strings.Repeat("ab12", 8)
	yes := []string{
		"我的 key 是 " + "sk-" + strings.Repeat("A1", 12),
		"哈希 " + hex32,
		"AKIA" + strings.Repeat("Q7", 8),
		"密码是abcdef12",
		"Password: hunter2hunter",
		"token=" + strings.Repeat("z", 8),
	}
	no := []string{"密码很重要。", "token 过期了", "AKIA123", "颜色是 " + strings.Repeat("ab", 8), "sk-short", "明天吃饭"}
	for _, s := range yes {
		if !hasSecretShape(s) {
			t.Errorf("应该认出：%q", s)
		}
	}
	for _, s := range no {
		if hasSecretShape(s) {
			t.Errorf("不该误报：%q", s)
		}
	}
}

// D 节：想取代谁的候选、受保护的有效事实的分类计数（用造好的结果直接测报告文字）
func TestReportSectionD(t *testing.T) {
	t.Parallel()
	r := &runResult{Mode: modeFake, Counts: map[string]int{"supersede_protected": 2, "confirm_blocked_protected": 1}, ShadowSeed: 1,
		Extract: &extracted{
			Protected: map[protectedStat]int{{srcBunSummary, subjOwner}: 3, {srcOwnerOK, subjOther}: 1, {srcOther, subjSelf}: 4},
			Facts: []factRow{
				{ID: "old1", Ref: "f:old1", Text: "旧事实原文", Status: "active"},
				{ID: "new1", Ref: "f:new1", Text: "新说法", Status: "candidate", New: true, WantsSupersede: "old1", Scopes: []string{"owner"}},
			}}}
	rep := reportMD(r)
	for _, must := range []string{"supersede_protected（取代被保护挡下）：2", "confirm_blocked_protected（候选因想取代的旧事实受保护而不提升）：1",
		"想取代：旧「旧事实原文」→ 新「新说法」", "| Bun 整理写的 | 3 | 0 | 0 |", "| 你放行的 | 0 | 0 | 1 |", "| 其余（导入时就有效的） | 0 | 4 | 0 |"} {
		if !strings.Contains(rep, must) {
			t.Errorf("D 节应该有 %q", must)
		}
	}
}
