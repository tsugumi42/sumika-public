package chatui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 终端块（主人 2026-11-07：看着像普通文本块，要更像终端）：一块里"$ 命令"接输出，失败的退出码在块里最后一行。
func TestConsoleBlock(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 600}
	st := &Step{ID: "c", Kind: StepCommand, State: StepFailed, Target: "go test ./lamp/...", HasExit: true, ExitCode: 1, Output: "=== RUN   TestLamp\nFAIL\n"}
	tt := cardTester(h, st, map[string]*bool{})
	con := tree(tt).find("command")
	if con == nil || con.find("prompt") == nil || con.find("prompt").Text != "$" {
		t.Fatalf("应该是一块终端块、第一行有 $：%v", tt.Texts())
	}
	if con.find("output") == nil || !subtreeHas(con, "=== RUN   TestLamp") {
		t.Errorf("输出应该在同一块里：%v", tt.Texts())
	}
	if ex := con.find("exit"); ex == nil || ex.Text != "退出码 1" {
		t.Errorf("退出码应该在终端块最后一行：%v", tt.Texts())
	}
	if head := tree(tt).find("head"); head == nil || subtreeHas(head, "退出码") || head.find("badge") == nil {
		t.Error("头部只留角标，不再写退出码")
	}
	// $ 在命令左边、命令折行时接续行对齐在 $ 后面（命令是 $ 右边单独的一列）
	if p, x := con.find("prompt"), con.find("cmd").find("text"); p.Box.X+p.Box.W > x.Box.X {
		t.Errorf("$（右缘 %.1f）应该在命令（左缘 %.1f）左边", p.Box.X+p.Box.W, x.Box.X)
	}
}

// 长命令只露前 4 行，"还有 N 行"点开看全部、再点收起。
func TestConsoleLongCommand(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 600}
	st := &Step{ID: "c", Kind: StepCommand, State: StepDone, Target: "cat <<'EOF'\n1\n2\n3\n4\n5\nEOF"}
	open := map[string]*bool{"c": ptr(true)}
	tt := cardTester(h, st, open)
	cmd := func() string { return tree(tt).find("command").find("cmd").find("text").Text }
	if got := cmd(); got != "cat <<'EOF'\n1\n2\n3" || !tt.HasText("还有 3 行") {
		t.Fatalf("应该只露前 4 行并写还有 3 行：%q %v", got, tt.Texts())
	}
	click(t, tt, "还有 3 行")
	if got := cmd(); got != st.Target || !tt.HasText("收起命令") {
		t.Errorf("点开以后应该是整条命令：%q", got)
	}
	click(t, tt, "收起命令")
	if !tt.HasText("还有 3 行") {
		t.Error("再点应该收回 4 行")
	}
}

// 很多行输出：跑完最高 12 行、默认停在最底下、左下"共 N 行"，"展开全部"最高 30 行；运行中只看最后 6 行；不足 12 行不写行数。
func TestConsoleLongOutput(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 600}
	line := h.monoLine()
	st := &Step{ID: "c", Kind: StepCommand, State: StepRunning, Target: "go test -v", Output: manyLines(100)}
	open := map[string]*bool{}
	tt := cardTester(h, st, open)
	all := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(all, "第 100 行") || strings.Contains(all, "第 94 行") || tt.HasText("共 100 行") {
		t.Errorf("运行中只看最后 6 行、不写行数：%q", all)
	}
	st.State = StepDone
	*open["c"] = true
	tt.Frame()
	tt.Frame()
	out := tree(tt).find("command").find("output")
	if out == nil || !near(out.Box.H, float32(consoleOutLines)*line) || !tt.HasText("共 100 行") {
		t.Fatalf("跑完应该是 %d 行高的滚动块并写共 100 行：高 %.1f，%v", consoleOutLines, out.Box.H, tt.Texts())
	}
	if last := out.find("text"); last == nil || last.Box.Y+last.Box.H > out.Box.Y+out.Box.H+1 {
		t.Errorf("应该默认停在最底下：内容底 %.1f，块底 %.1f", last.Box.Y+last.Box.H, out.Box.Y+out.Box.H)
	}
	click(t, tt, "展开全部")
	if out = tree(tt).find("command").find("output"); !near(out.Box.H, float32(consoleAllLines)*line) || !tt.HasText("收起") {
		t.Errorf("展开全部以后应该是 %d 行高：%.1f", consoleAllLines, out.Box.H)
	}
	st.Output = manyLines(5)
	tt.Frame()
	tt.Frame()
	if tt.HasText("共 5 行") || tt.HasText("展开全部") {
		t.Error("不足 12 行不该写行数和展开全部")
	}
}

// 颜色控制码剥掉；剥两遍和剥一遍一样；没有 ESC 的原样返回。
func TestStripANSI(t *testing.T) {
	esc := "\x1b"
	for _, c := range []struct{ in, want string }{
		{"plain", "plain"},
		{esc + "[1m=== RUN" + esc + "[0m", "=== RUN"},
		{esc + "[38;5;196mred" + esc + "[39m ok", "red ok"},
		{esc + "]0;title\x07after", "after"},
		{esc + "]8;;http://x" + esc + "\\link" + esc + "]8;;" + esc + "\\", "link"},
		{"tail" + esc, "tail"},
		{esc + "(Bx", "x"},
		{"中文" + esc + "[2K行", "中文行"},
	} {
		got := stripANSI(c.in)
		if got != c.want || stripANSI(got) != got {
			t.Errorf("stripANSI(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
	for i := 0; i < 256; i++ { // 截断在任意位置都不越界、结果里没有 ESC
		s := fmt.Sprintf("a%s[%dmb%s]x", esc, i, esc)
		for j := 0; j <= len(s); j++ {
			if got := stripANSI(s[:j]); strings.Contains(got, esc) {
				t.Fatalf("stripANSI(%q) 留下了 ESC：%q", s[:j], got)
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatalf("点 %q：%v", label, err)
	}
	tt.Frame()
	tt.Frame()
}
