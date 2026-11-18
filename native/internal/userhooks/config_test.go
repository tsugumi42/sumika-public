package userhooks

import (
	"strings"
	"testing"
	"time"
)

func TestParseShapes(t *testing.T) {
	good := `{"hooks":{"PostToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"x"}]}]}}`
	cases := []struct {
		name      string
		in        string
		wantErr   bool
		post, stp int // 解出的组数
		diag      int
	}{
		{"空文件", "", false, 0, 0, 0},
		{"只有空白", " \r\n\t ", false, 0, 0, 0},
		{"空对象", "{}", false, 0, 0, 0},
		{"hooks 空对象", `{"hooks":{}}`, false, 0, 0, 0},
		{"hooks 是 null", `{"hooks":null}`, false, 0, 0, 0},
		{"顶层是数组", `[]`, true, 0, 0, 0},
		{"hooks 是数组", `{"hooks":[]}`, true, 0, 0, 0},
		{"hooks 是字符串", `{"hooks":"x"}`, true, 0, 0, 0},
		{"坏 JSON", `{"hooks":`, true, 0, 0, 0},
		{"别的键忽略", `{"permissions":{"allow":["x"]},"hooks":{}}`, false, 0, 0, 0},
		{"带 BOM", "\xef\xbb\xbf" + good, false, 1, 0, 0},
		{"事件值不是数组", `{"hooks":{"PostToolUse":{"a":1}}}`, false, 0, 0, 1},
		{"组里 hooks 是字符串", `{"hooks":{"Stop":[{"hooks":"x"}]}}`, false, 0, 0, 1},
		{"不支持的事件", `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"x"}]}]}}`, false, 0, 0, 1},
		{"不认识的事件", `{"hooks":{"Whatever":[]}}`, false, 0, 0, 1},
		{"matcher 不是字符串", `{"hooks":{"Stop":[{"matcher":5,"hooks":[{"type":"command","command":"x"}]}]}}`, false, 0, 0, 1},
		{"坏正则只跳这一组", `{"hooks":{"PostToolUse":[{"matcher":"(","hooks":[{"type":"command","command":"x"}]},{"matcher":"Edit","hooks":[{"type":"command","command":"y"}]}]}}`, false, 1, 0, 1},
		{"缺 command", `{"hooks":{"Stop":[{"hooks":[{"type":"command"}]}]}}`, false, 0, 0, 1},
		{"command 只有空白", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"  "}]}]}}`, false, 0, 0, 1},
		{"类型不是 command", `{"hooks":{"Stop":[{"hooks":[{"type":"http","url":"x"}]}]}}`, false, 0, 0, 1},
		{"handler 类型错乱", `{"hooks":{"Stop":[{"hooks":[5,{"type":"command","command":"x"}]}]}}`, false, 0, 1, 1},
		{"未知 shell", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x","shell":"zsh"}]}]}}`, false, 0, 0, 1},
		{"有 args", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x","args":["a"]}]}]}}`, false, 0, 0, 1},
		{"onFailure 写错", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x","onFailure":"retry"}]}]}}`, false, 0, 0, 1},
		{"feedback 写错", `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"x","feedback":"x"}]}]}}`, false, 0, 0, 1},
		{"async 记诊断仍保留", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x","async":true}]}]}}`, false, 0, 1, 1},
		{"Stop 上写 if 不跑", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x","if":"Edit(*.go)"}]}]}}`, false, 0, 0, 1},
		{"if 写法不对", `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"x","if":"a b c"}]}]}}`, false, 0, 0, 1},
		{"paths 类型错", `{"hooks":{"Stop":[{"paths":5,"hooks":[{"type":"command","command":"x"}]}]}}`, false, 0, 0, 1},
		{"paths 大括号没配对", `{"hooks":{"Stop":[{"paths":"{a","hooks":[{"type":"command","command":"x"}]}]}}`, false, 0, 0, 1},
		{"paths 是数组", `{"hooks":{"Stop":[{"paths":["*.go","*.mod"],"hooks":[{"type":"command","command":"x"}]}]}}`, false, 0, 1, 0},
		{"超大", `{"hooks":{}}` + strings.Repeat(" ", maxConfigBytes), true, 0, 0, 0},
	}
	for _, c := range cases {
		set, err := Parse([]byte(c.in), 60*time.Second)
		if (err != nil) != c.wantErr {
			t.Errorf("%s：err=%v", c.name, err)
			continue
		}
		if err != nil {
			continue
		}
		if got := len(set.Events[EventPostToolUse]); got != c.post {
			t.Errorf("%s：PostToolUse 组数 %d，要 %d", c.name, got, c.post)
		}
		if got := len(set.Events[EventStop]); got != c.stp {
			t.Errorf("%s：Stop 组数 %d，要 %d（%v）", c.name, got, c.stp, set.Diag)
		}
		if len(set.Diag) != c.diag {
			t.Errorf("%s：诊断 %d 条，要 %d：%v", c.name, len(set.Diag), c.diag, set.Diag)
		}
	}
}

func TestParseTimeoutAndDefaults(t *testing.T) {
	handler := func(extra string) string {
		return `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"` + extra + `}]}]}}`
	}
	for extra, want := range map[string]time.Duration{
		``:                 42 * time.Second,
		`,"timeout":30`:    30 * time.Second,
		`,"timeout":0`:     time.Second,
		`,"timeout":-5`:    time.Second,
		`,"timeout":0.2`:   time.Second,
		`,"timeout":2.5`:   2500 * time.Millisecond,
		`,"timeout":99999`: maxTimeout,
		`,"timeout":600`:   maxTimeout,
		`,"timeout":null`:  42 * time.Second,
		`,"timeout":1e999`: 0, // 解不开：这条跳过
		`,"timeout":"30"`:  0,
		`,"timeout":[1]`:   0,
	} {
		set, err := Parse([]byte(handler(extra)), 42*time.Second)
		if err != nil {
			t.Fatalf("%s：%v", extra, err)
		}
		groups := set.Events[EventStop]
		if want == 0 {
			if len(groups) != 0 {
				t.Errorf("%s：该跳过", extra)
			}
			continue
		}
		if len(groups) != 1 || groups[0].Handlers[0].Timeout != want {
			t.Errorf("%s：%+v 要 %v", extra, groups, want)
		}
	}
	set, _ := Parse([]byte(handler(`,"onFailure":"block","feedback":"failure","statusMessage":"检查中","shell":"bash"`)), time.Second)
	h := set.Events[EventStop][0].Handlers[0]
	if !h.Block || h.Feedback != "failure" || h.Label != "检查中" || h.Shell != "bash" {
		t.Errorf("%+v", h)
	}
	set, _ = Parse([]byte(handler("")), time.Second)
	if h := set.Events[EventStop][0].Handlers[0]; h.Block || h.Feedback != "always" || h.Shell != "" {
		t.Errorf("默认值：%+v", h)
	}
}

func TestMatcher(t *testing.T) {
	for _, c := range []struct {
		pattern, tool string
		want          bool
	}{
		{"", "edit", true}, {"*", "anything", true},
		{"Edit|Write", "edit", true}, {"Edit|Write", "write", true}, {"Edit|Write", "read", false},
		{"edit", "edit", true}, {"MultiEdit", "edit", true},
		{"Bash", "powershell", true}, {"PowerShell", "powershell", true}, {"Bash", "edit", false},
		{"Edit", "edits", false}, // 名单是整词，不是子串
		{"^edit$", "edit", true}, {"mcp__.*", "mcp__x__y", true}, {"mcp__.*", "edit", false},
		{"Glob", "find", true}, {"Task", "task", true},
	} {
		m, err := newMatcher(c.pattern)
		if err != nil {
			t.Fatalf("%q：%v", c.pattern, err)
		}
		if got := m.matches(c.tool); got != c.want {
			t.Errorf("matcher %q 对 %q：%v", c.pattern, c.tool, got)
		}
	}
	if _, err := newMatcher("("); err == nil {
		t.Error("坏正则要报错")
	}
}

func TestPathGlobs(t *testing.T) {
	root := `D:\work\proj`
	for _, c := range []struct {
		glob, file string
		want       bool
	}{
		{"*.go", `D:\work\proj\a.go`, true},
		{"*.go", `D:\work\proj\sub\deep\a.go`, true},
		{"*.go", `D:\work\proj\a.gox`, false},
		{"*.GO", `D:\work\proj\a.go`, true},
		{"native/**/*.go", `D:\work\proj\native\a.go`, true},
		{"native/**/*.go", `D:\work\proj\native\x\y\a.go`, true},
		{"native/**/*.go", `D:\work\proj\other\a.go`, false},
		{"native/*.go", `D:\work\proj\native\x\a.go`, false},
		{"**/*.go", `D:\work\proj\a.go`, true},
		{"./native/*.go", `D:\work\proj\native\a.go`, true},
		{"*.{go,mod}", `D:\work\proj\go.mod`, true},
		{"*.{go,mod}", `D:\work\proj\go.sum`, false},
		{"a?.go", `D:\work\proj\ab.go`, true},
		{"a?.go", `D:\work\proj\abc.go`, false},
		{"[a-c].go", `D:\work\proj\b.go`, true},
		{"[a-c].go", `D:\work\proj\d.go`, false},
		{"[!a-c].go", `D:\work\proj\d.go`, true},
		{`native\*.go`, `D:\work\proj\native\a.go`, true},  // 反斜杠也认
		{"native/*.go", `D:\elsewhere\native\a.go`, false}, // 工作区外的比绝对路径
		{"D:/elsewhere/**", `D:\elsewhere\native\a.go`, true},
	} {
		globs, err := parsePaths([]byte(`"` + strings.ReplaceAll(c.glob, `\`, `\\`) + `"`))
		if err != nil {
			t.Fatalf("%q：%v", c.glob, err)
		}
		if got := pathMatches(globs, c.file, root); got != c.want {
			t.Errorf("glob %q 对 %q：%v", c.glob, c.file, got)
		}
	}
	for _, bad := range []string{"", "{a", "a}", "[a", "[]"} {
		if _, err := globRegexp(bad); err == nil {
			t.Errorf("%q 该报错", bad)
		}
	}
}

func TestIfRule(t *testing.T) {
	root := `D:\work\proj`
	file := `D:\work\proj\a.go`
	for _, c := range []struct {
		rule, tool, file, cmd string
		want                  bool
	}{
		{"Edit", "edit", file, "", true},
		{"Edit(*.go)", "edit", file, "", true},
		{"Edit(*.md)", "edit", file, "", false},
		{"Edit(*.go)", "write", file, "", false},
		{"Bash(git *)", "powershell", "", "git status", true},
		{"Bash(git *)", "powershell", "", "  git status ", true},
		{"Bash(git *)", "powershell", "", "gitk", false},
		{"Bash(git *)", "powershell", "", "echo git status", false},
		{"Write(*.go)", "write", "", "", false}, // 没有文件路径就比不上
	} {
		r, err := parseIf(c.rule)
		if err != nil {
			t.Errorf("%q：%v", c.rule, err)
			continue
		}
		if got := r.allows(c.tool, c.file, c.cmd, root); got != c.want {
			t.Errorf("if %q 对 %s %q %q：%v", c.rule, c.tool, c.file, c.cmd, got)
		}
	}
	if _, err := parseIf("not a rule"); err == nil {
		t.Error("坏写法要报错")
	}
}
