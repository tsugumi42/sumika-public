package edittext

import (
	"strings"
	"testing"
)

const sample = `package main

func main() {
	cfg := load("a.yml")
	if cfg.Debug {
		run(cfg, 3, "fast")
	}
	for i := 0; i < 10; i++ {
		step(i)
	}
}
`

// 一次编辑，对着 content 拿到未命中错误
func missOf(t *testing.T, content string, e Pair) (*MissError, string) {
	t.Helper()
	_, err := Apply(content, []Pair{e}, "a.go")
	m := AsMiss(err)
	if m == nil {
		t.Fatalf("该是未命中：%v", err)
	}
	return m, Hint(content, []Pair{e}, m)
}

// 原报错的字一个都不能变（审批预览、对拍、模型已经习惯的格式）
func TestMissErrorTextUnchanged(t *testing.T) {
	_, err := Apply(sample, []Pair{{"不存在的文字", "x"}}, "a.go")
	if err == nil || err.Error() != "Could not find the exact text in a.go. The old text must match exactly including all whitespace and newlines." {
		t.Fatalf("%v", err)
	}
	_, err = Apply(sample, []Pair{{"cfg", "x"}}, "a.go")
	if err == nil || err.Error() != "Found 3 occurrences of the text in a.go. The text must be unique. Please provide more context to make it unique." {
		t.Fatalf("%v", err)
	}
	_, err = Apply(sample, []Pair{{"step(i)", "x"}, {"不存在", "y"}}, "a.go")
	if err == nil || err.Error() != "Could not find edits[1] in a.go. The oldText must match exactly including all whitespace and newlines." || AsMiss(err).Index != 1 {
		t.Fatalf("%v", err)
	}
	if AsMiss(errTest("别的错")) != nil {
		t.Fatal("不是未命中不该认")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

// 1. newText 已经在文件里：说可能已经改过
func TestHintAlreadyApplied(t *testing.T) {
	_, h := missOf(t, sample, Pair{"\tfor i := 0; i < 5; i++ {\n", "\tfor i := 0; i < 10; i++ {\n\t\tstep(i)\n"})
	if !strings.Contains(h, "已经在这个文件里") {
		t.Fatalf("%q", h)
	}
	// newText 太短不拿来判断（"}" 到处都有）
	_, h = missOf(t, sample, Pair{"zzz 不在", "}"})
	if strings.Contains(h, "已经在这个文件里") {
		t.Fatalf("短 newText 不该触发：%q", h)
	}
}

// 2. 空白不一样：逐行忽略空白投票，显示原文和开头空白对比，不加行号
func TestHintWhitespaceMismatch(t *testing.T) {
	old := "\tif cfg.Debug {\n    run(cfg, 3, \"fast\")\n\t}" // 模型把 Tab 写成了空格，还漏了缩进
	m, h := missOf(t, sample, Pair{old, "x"})
	if m.Count != 0 {
		t.Fatal(m)
	}
	for _, want := range []string{"第 5～6 行", "\t\trun(cfg, 3, \"fast\")", "→→run(cfg, 3, \"fast\")", "····run(cfg, 3, \"fast\")", "文件里：", "oldText："} {
		if !strings.Contains(h, want) {
			t.Errorf("缺 %q：\n%s", want, h)
		}
	}
	if strings.Contains(h, "5:") || strings.Contains(h, "   5\t") {
		t.Errorf("原文不加行号（加了模型会抄进 oldText）：\n%s", h)
	}
	// CRLF 文件也一样
	crlf := strings.ReplaceAll(sample, "\n", "\r\n")
	if _, h := missOf(t, crlf, Pair{old, "x"}); !strings.Contains(h, "第 5～6 行") || strings.Contains(h, "\r") {
		t.Errorf("CRLF：%q", h)
	}
}

// 投票：一半以上的行同意才算；只有零星几行对得上就不瞎指
func TestHintVoteThreshold(t *testing.T) {
	old := "if cfg.Debug {\n  完全不存在的一行 alpha\n  完全不存在的二行 beta\n  完全不存在的三行 gamma\n  step(i)"
	if _, h := missOf(t, sample, Pair{old, "x"}); strings.Contains(h, "逐行对得上") {
		t.Fatalf("5 行里只有 2 行对得上，不该定位：\n%s", h)
	}
	// 显示范围封顶 12 行，每行封顶 200 字
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("\tline" + strings.Repeat("x", 300) + "\n")
	}
	long := b.String()
	_, h := missOf(t, long, Pair{strings.ReplaceAll(long, "\t", "    "), "y"})
	if n := strings.Count(h, "xxxxxxxxxx"); n == 0 || strings.Count(h, "\n") > 24 || !strings.Contains(h, "后面还有") {
		t.Fatalf("行数 %d：\n%.400s", strings.Count(h, "\n"), h)
	}
	if strings.Contains(h, strings.Repeat("x", 250)) {
		t.Fatal("单行没截断")
	}
}

// 3. 记错一个参数：投票救不了，相似度能救
func TestHintSimilarLine(t *testing.T) {
	m, h := missOf(t, sample, Pair{"\t\trun(cfg, 5, \"slow\")\n", "\t\trun(cfg, 5, \"fast\")\n"})
	if m.Count != 0 || !strings.Contains(h, "最接近 oldText 的是第 6 行") || !strings.Contains(h, "run(cfg, 3, \"fast\")") {
		t.Fatalf("%q", h)
	}
	// 完全无关：没有提示，不瞎猜
	if _, h := missOf(t, sample, Pair{"Completely unrelated sentence about cats", "x"}); h != "" {
		t.Fatalf("无关的文字不该有提示：%q", h)
	}
	// 太大的文件跳过这一步（不卡）
	big := strings.Repeat("filler line number one\n", hintFuzzyMaxLines+10) + "\t\trun(cfg, 3, \"fast\")\n"
	if _, h := missOf(t, big, Pair{"\t\trun(cfg, 5, \"slow\")\n", "q"}); h != "" {
		t.Fatalf("大文件不做相似度：%q", h[:min(80, len(h))])
	}
}

// 不唯一：列出每处的行号，最多 8 处
func TestHintDuplicate(t *testing.T) {
	m, h := missOf(t, sample, Pair{"cfg", "x"})
	if m.Count != 3 || !strings.Contains(h, "第 4、5、6") {
		t.Fatalf("%v %q", m, h)
	}
	many := strings.Repeat("dup line\n", 20)
	m, h = missOf(t, many, Pair{"dup line", "x"})
	if m.Count != 20 || !strings.Contains(h, "第 1、2、3、4、5、6、7、8 行等") || strings.Contains(h, "9") {
		t.Fatalf("%v %q", m, h)
	}
}

func TestHintEdgeInputs(t *testing.T) {
	if Hint("", nil, nil) != "" || Hint("x", []Pair{{"a", "b"}}, &MissError{Index: 5}) != "" {
		t.Fatal("没东西时返回空")
	}
	for _, content := range []string{"", "\n", "\uFEFFpackage main\n", "{{{{\n}}}}\n"} {
		for _, old := range []string{"}", "   ", "不存在的行", "func main() {\n}"} {
			_, err := Apply(content, []Pair{{old, "新的一行文字比较长比较长比较长"}}, "a.go")
			if m := AsMiss(err); m != nil {
				_ = Hint(content, []Pair{{old, "x"}}, m) // 不崩
			}
		}
	}
}
