package main

import (
	"strings"
	"testing"
	"time"
)

func TestScenario(t *testing.T) {
	for _, name := range []string{"smoke", "full"} {
		s, _, err := loadScenario(name)
		if err != nil {
			t.Fatal(err)
		}
		if name == "smoke" && (len(s.Steps) != 2 || s.Steps[0].ID != "remember" || s.Steps[1].ID != "recall") {
			t.Fatalf("短剧本没有覆盖写入和问回：%+v", s)
		}
	}
	good := `{"name":"编的场景","steps":[{"id":"a","kind":"message","scene":"private","actor":"owner","text":"甲"},{"id":"b","kind":"message","scene":"private","actor":"owner","text":"乙","reply":"a"},{"id":"r","kind":"recall","target":"b"}]}`
	if _, err := parseScenario([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"name":"空","steps":[]}`,
		strings.Replace(good, `"reply":"a"`, `"reply":"missing"`, 1),
		strings.Replace(good, `"id":"b"`, `"id":"a"`, 1),
		strings.Replace(good, `"target":"b"`, `"target":"r"`, 1),
		strings.Replace(good, `"reply":"a"`, `"reply":"a","actor_typo":"owner"`, 1),
		strings.Replace(good, `"text":"乙"`, `"text":"乙","wait_ms":-1`, 1),
		good + `{}`,
	} {
		if _, err := parseScenario([]byte(bad)); err == nil {
			t.Fatalf("错误剧本被接受：%s", bad)
		}
	}
}
func TestReport(t *testing.T) {
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.FixedZone("test", 8*3600))
	text := "第一行 | 原文\n```保留反引号\n第二行"
	r := report{Name: "编的报告", Started: at, Ended: at.Add(time.Second), Backfill: "没有", Evidence: map[string][]map[string]any{"记忆事实": {{"text": "钥匙在青盒"}}}, Rounds: []round{
		{Step: step{ID: "one", Kind: "message"}, Started: at, Ended: at.Add(time.Second), Sent: []sentMessage{{At: at.Add(10 * time.Millisecond), Text: text}, {At: at.Add(30 * time.Millisecond), Text: "尾段"}}},
		{Step: step{ID: "silent", Kind: "message"}, Started: at, Ended: at.Add(time.Second)},
		{Step: step{ID: "two", Kind: "message"}, Started: at, Ended: at.Add(time.Second), Sent: []sentMessage{{At: at.Add(100 * time.Millisecond), Text: "另外一轮"}}},
	}, Calls: []modelCall{{Prompt: 120, Output: 20, CacheRead: 80, CacheWrite: 5, Reasoning: 7, UsageSeen: true}, {}}}
	got := renderReport(r)
	for _, want := range []string{text, "钥匙在青盒", "2 轮", "首段 P50 10 ms / P95 100 ms", "末段 P50 30 ms / P95 100 ms", "输入总 token：120", "输出：20（含思考 7）", "未取得 usage：1", "没有发出回复。"} {
		if !strings.Contains(got, want) {
			t.Fatalf("报告缺 %q：\n%s", want, got)
		}
	}
	r.Rounds = nil
	if got = renderReport(r); !strings.Contains(got, "P50/P95 不可用") {
		t.Fatal(got)
	}
}
