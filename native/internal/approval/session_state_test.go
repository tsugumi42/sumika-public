package approval

// 会话审批档和“这个对话里都允许”重启后保留（规格-审批分级 8.2）。
// 形状：重启后还在、撤销后重启不回来、读回来时筛掉不是本地私聊的和不认识的档、文件坏了不崩、写失败不影响当场生效。
// 数据目录只用 t.TempDir()。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/policy"
)

func sessionGate(t *testing.T, dir string) (*Gate, error) {
	t.Helper()
	g := NewGate(dir)
	t.Cleanup(func() { g.Analyzer.Close() })
	return g, g.UseSessionFile(filepath.Join(dir, "go", "approval-sessions.json"))
}

func convRule(key string) Rule {
	return Rule{Key: key, Label: "写 " + key, Tool: "write", Workdir: `D:\w`, Dirs: []string{`E:\o`}, At: 5}
}

func TestSessionStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	g1, err := sessionGate(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := g1.SetSessionMode("console:suzu", "yolo"); err != nil {
		t.Fatal(err)
	}
	if err := g1.SetSessionMode("console:suzu:topic:t1", "auto"); err != nil {
		t.Fatal(err)
	}
	g1.Conv.Merge("console:suzu", convRule("write:dir-a"))
	g1.Conv.Merge("console:suzu", convRule("write:dir-b"))

	g2, err := sessionGate(t, dir) // 重启
	if err != nil {
		t.Fatal(err)
	}
	if g2.ModeFor("console:suzu") != "yolo" || g2.ModeFor("console:suzu:topic:t1") != "auto" || g2.ModeFor("console:suzu:topic:t2") != "manual" {
		t.Fatalf("重启后各会话的档不对：%s %s %s", g2.ModeFor("console:suzu"), g2.ModeFor("console:suzu:topic:t1"), g2.ModeFor("console:suzu:topic:t2"))
	}
	if _, over := g2.SessionMode("console:suzu"); !over {
		t.Fatal("该仍是会话自己的覆盖")
	}
	dirs, ok := g2.Conv.Match("console:suzu", `D:\w`, "write:dir-a", "")
	if !ok || len(dirs) != 1 || dirs[0] != `E:\o` {
		t.Fatalf("“这个对话”该原样回来（含批准过的外部目录）：%v %v", dirs, ok)
	}
	if _, ok := g2.Conv.Match("console:suzu", `D:\other`, "write:dir-a", ""); ok {
		t.Fatal("会话换了工作目录，旧目录里点的授权仍不该算数")
	}
	if g2.Conv.Has("console:suzu:topic:t1", "write:dir-a") {
		t.Fatal("别的话题不沾光")
	}
	if rules := g2.Conv.List(); len(rules) != 2 || rules[0].Scope != policy.ByConversation {
		t.Fatalf("规则页该看到两条：%+v", rules)
	}

	// 撤销、回到默认以后再重启，不会回来
	if !g2.Conv.Remove("console:suzu", "write:dir-a") {
		t.Fatal("撤销失败")
	}
	if err := g2.SetSessionMode("console:suzu", ""); err != nil {
		t.Fatal(err)
	}
	g3, err := sessionGate(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if g3.ModeFor("console:suzu") != "manual" || g3.Conv.Has("console:suzu", "write:dir-a") || !g3.Conv.Has("console:suzu", "write:dir-b") {
		t.Fatalf("撤销的不该回来、没撤的该留着：%s %v %v", g3.ModeFor("console:suzu"), g3.Conv.Has("console:suzu", "write:dir-a"), g3.Conv.Has("console:suzu", "write:dir-b"))
	}
	// 全清空后文件也不留
	g3.Conv.Remove("console:suzu", "write:dir-b")
	g3.SetSessionMode("console:suzu:topic:t1", "")
	if _, err := os.Stat(filepath.Join(dir, "go", "approval-sessions.json")); !os.IsNotExist(err) {
		t.Fatalf("什么都没有了该删文件：%v", err)
	}
}

// 读回来时筛一遍：文件被手改过、塞进 QQ 会话、群聊、不认识的档，一律不恢复（重启后可触发的人不比重启前多）
func TestSessionStateHostileFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "go", "approval-sessions.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"modes":{"console:ok":"yolo","qq:private:100:201":"yolo","console:suzu:group:g1":"yolo","console:bad":"随便","":"yolo","internal:task-1":"yolo"},
	"conv":[{"Scope":"always","Key":"write:x","Tool":"write","Conversation":"console:ok","Workdir":"D:\\w"},
	{"Key":"write:x","Tool":"write","Conversation":"qq:private:100:201"},
	{"Key":"write:x","Tool":"write","Conversation":"console:suzu:group:g1"},
	{"Key":"","Tool":"write","Conversation":"console:ok"},
	{"Key":"powershell:git","Tool":"powershell","Conversation":"internal:task-1"}]}`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := sessionGate(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, conv := range []string{"qq:private:100:201", "console:suzu:group:g1", "console:bad", "", "internal:task-1"} {
		if m, over := g.SessionMode(conv); over || m != "" {
			t.Errorf("%q 不该恢复出档：%q", conv, m)
		}
		if g.Conv.Has(conv, "write:x") || g.Conv.Has(conv, "powershell:git") {
			t.Errorf("%q 不该恢复出放行", conv)
		}
	}
	if g.ModeFor("console:ok") != "yolo" {
		t.Error("本地私聊的该恢复")
	}
	rules := g.Conv.List()
	if len(rules) != 1 || rules[0].Scope != policy.ByConversation || rules[0].Conversation != "console:ok" {
		t.Fatalf("只该恢复一条，范围固定是“这个对话”（文件里写的 always 不算）：%+v", rules)
	}
}

// 文件坏了：不崩，改名留存，之后照常写新的
func TestSessionStateCorruptFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "go", "approval-sessions.json")
	os.MkdirAll(filepath.Dir(file), 0o755)
	os.WriteFile(file, []byte(`{"modes": 坏了`), 0o644)
	g, err := sessionGate(t, dir)
	if err == nil || !strings.Contains(err.Error(), "改名留存") {
		t.Fatalf("该报格式坏了并说明留存：%v", err)
	}
	kept, _ := filepath.Glob(file + ".坏-*")
	if len(kept) != 1 {
		t.Fatalf("坏文件该改名留存：%v", kept)
	}
	if g.ModeFor("console:suzu") != "manual" {
		t.Fatal("读不了当没有")
	}
	if err := g.SetSessionMode("console:suzu", "auto"); err != nil {
		t.Fatal(err)
	}
	g2, err := sessionGate(t, dir)
	if err != nil || g2.ModeFor("console:suzu") != "auto" {
		t.Fatalf("坏文件之后新写的该能读回：%v %s", err, g2.ModeFor("console:suzu"))
	}
}

// 写不进去：当场照常生效，会话档返回说明，“这个对话”走 SessionSaveErr，都不丢内存里的
func TestSessionStateSaveFailureKeepsMemory(t *testing.T) {
	dir := t.TempDir()
	// go 目录的位置放一个普通文件，建目录与写文件都会失败
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("占位"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := sessionGate(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	var reported []error
	g.SessionSaveErr = func(err error) { reported = append(reported, err) }
	err = g.SetSessionMode("console:suzu", "yolo")
	if err == nil || !strings.Contains(err.Error(), "已经生效") {
		t.Fatalf("写不进去该说明但不吞：%v", err)
	}
	if g.ModeFor("console:suzu") != "yolo" {
		t.Fatal("写盘失败也该当场生效")
	}
	g.Conv.Merge("console:suzu", convRule("write:dir-a"))
	if !g.Conv.Has("console:suzu", "write:dir-a") || len(reported) != 1 {
		t.Fatalf("放行照旧、失败要报一次：%d", len(reported))
	}
}

// 不是本地私聊的会话（子任务、QQ）只在内存里，不进文件
func TestSessionStateNonLocalNotWritten(t *testing.T) {
	dir := t.TempDir()
	g, err := sessionGate(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.SetSessionMode("internal:task-1", "yolo"); err != nil {
		t.Fatal(err)
	}
	g.Conv.Merge("internal:task-1", convRule("write:dir-a"))
	g.Conv.Merge("console:suzu:group:g", convRule("write:dir-a"))
	if _, err := os.Stat(filepath.Join(dir, "go", "approval-sessions.json")); !os.IsNotExist(err) {
		t.Fatalf("不该写文件：%v", err)
	}
	if g.ModeFor("internal:task-1") != "yolo" {
		t.Fatal("内存里照旧生效")
	}
}
