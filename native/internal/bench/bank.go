package bench

// 题库（规格-表二执行器 第五、六节）：scripts/bench-t2/tasks/<题号>/task.json 写说明、几步、判分方式，同目录 files/ 是初始文件。
// 判分全部确定性、只看结束时的状态：文件内容、文件在不在、回复里有没有该有的字、go test 的退出码、有没有改不许改的文件

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

// BankTask 是题库里的一道题
type BankTask struct {
	ID         string `json:"id"`    // 不写就用目录名
	Group      string `json:"group"` // b1 / b2 / b3 / b4
	Source     string `json:"source,omitempty"`
	Steps      []Step `json:"steps"`
	Seed       []Line `json:"seed,omitempty"`
	KeepTokens int    `json:"keepTokens,omitempty"`
	// RecentMessages 照 Task 的同名字段（B4 让两组都从全部历史出发）
	RecentMessages int `json:"recentMessages,omitempty"`
	// RefSteps 是参考做法要几次工具调用（B2 过线：步数不超过它的 1.5 倍）
	RefSteps int       `json:"refSteps,omitempty"`
	Judge    JudgeSpec `json:"judge"`
	Dir      string    `json:"-"` // 题目所在目录（files/ 在它下面）
}

// JudgeSpec 是一道题怎么判
type JudgeSpec struct {
	GoTest   bool        `json:"goTest,omitempty"`   // 工作目录里 go test ./... 退出码 0
	Files    []FileCheck `json:"files,omitempty"`    // 结束时的文件
	Reply    []string    `json:"reply,omitempty"`    // 最后一步的回复里都要有（不分大小写；"a|b" 是有其中一个就行）
	Allowed  []string    `json:"allowed,omitempty"`  // 只许改 / 新建 / 删这些（相对路径，可带 * 通配）；空的不限
	Forbid   []string    `json:"forbid,omitempty"`   // 这些一动就算违反约束，Allowed 里有也一样（Aider 题的测试文件、go.mod）
	ReadOnly bool        `json:"readOnly,omitempty"` // 一个文件都不许动（只读的题）
	Ignore   []string    `json:"ignore,omitempty"`   // 比较改动时不看的（她跑命令产生的缓存、编译产物之类）
	Run      []RunCheck  `json:"run,omitempty"`      // 在工作目录里跑命令看输出（查行为，不只查字在不在）
}

// RunCheck 在工作目录里跑一条命令（30 秒时限），标准输出去掉首尾空白、统一换行以后要和 Stdout 相等
type RunCheck struct {
	Cmd    []string `json:"cmd"`
	Stdout string   `json:"stdout"`
}

// FileCheck 是对一个文件的要求
type FileCheck struct {
	Path      string   `json:"path"`
	Absent    bool     `json:"absent,omitempty"` // 结束时不该在
	Equals    *string  `json:"equals,omitempty"` // 去掉首尾空白、换行统一成 \n 以后相等
	Contains  []string `json:"contains,omitempty"`
	Lacks     []string `json:"lacks,omitempty"`
	Regex     []string `json:"regex,omitempty"`     // 每个都要匹配上（JSON 之类空格写法不定的）
	SameLines []string `json:"sameLines,omitempty"` // 去掉首尾空白后的非空行，和这些一一对上（不管顺序、重复的算两行）
}

// Verdict 是判分结果
type Verdict struct {
	Pass      bool            `json:"pass"`                // 每条都过、也没违反约束
	Violation bool            `json:"violation,omitempty"` // 改了不许改的文件
	Fails     []string        `json:"fails,omitempty"`     // 哪几条没过
	Changed   []string        `json:"changed,omitempty"`   // 和初始文件比改了哪些（+ 新建、- 删掉、~ 改了）
	GoTest    string          `json:"goTest,omitempty"`    // go test 输出的最后一段
	Answers   []string        `json:"answers,omitempty"`   // 带期望答案的那几句各判成什么（Classify，按步骤先后）
	Commands  []CommandResult `json:"commands,omitempty"`  // 所有判分命令的完整诊断，按执行顺序
}

// CommandResult 保留原始的两路输出；即便输出为空也写出来，不能只靠截过的失败摘要查原因。
type CommandResult struct {
	Cmd      []string `json:"cmd"`
	Ms       int64    `json:"ms"`
	TimedOut bool     `json:"timedOut"`
	ExitCode int      `json:"exitCode"` // 未启动或取不到退出码时为 -1
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	Error    string   `json:"error,omitempty"`
}

// LoadBank 读题库目录下的每道题（按题号排）
func LoadBank(dir string) ([]BankTask, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []BankTask
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "task.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var t BankTask
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, fmt.Errorf("%s：%w", e.Name(), err)
		}
		if t.ID == "" {
			t.ID = e.Name()
		}
		if len(t.Steps) == 0 {
			return nil, fmt.Errorf("%s：没有步骤", t.ID)
		}
		t.Dir = filepath.Join(dir, e.Name())
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// CopyFiles 把题目的 files/ 原样拷进 dst（没有 files/ 就什么都不拷）
func (t BankTask) CopyFiles(dst string) error {
	src := filepath.Join(t.Dir, "files")
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return os.MkdirAll(dst, 0o755)
	}
	return os.CopyFS(dst, os.DirFS(src))
}

// snapshot 是目录里每个文件的内容（相对路径用 /）
func snapshot(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return files, nil
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		files[filepath.ToSlash(rel)] = b
		return err
	})
	return files, err
}

// matchAny：pat 以 /** 结尾的算整个目录，其余照 path.Match
func matchAny(pats []string, p string) bool {
	for _, pat := range pats {
		if dir, ok := strings.CutSuffix(pat, "/**"); ok {
			if strings.HasPrefix(p, dir+"/") {
				return true
			}
		} else if ok, _ := path.Match(pat, p); ok {
			return true
		}
	}
	return false
}

func normalize(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

// Check 判一道题：workdir 是结束时的工作目录，reply 是最后一步的回复
func (t BankTask) Check(ctx context.Context, workdir, reply string) Verdict {
	var v Verdict
	fail := func(format string, a ...any) { v.Fails = append(v.Fails, fmt.Sprintf(format, a...)) }
	before, err := snapshot(filepath.Join(t.Dir, "files"))
	if err != nil {
		fail("读初始文件：%v", err)
	}
	after, err := snapshot(workdir)
	if err != nil {
		fail("读工作目录：%v", err)
	}
	for p, b := range after {
		if matchAny(t.Judge.Ignore, p) {
			continue
		}
		if old, ok := before[p]; !ok {
			v.Changed = append(v.Changed, "+"+p)
		} else if !bytes.Equal(old, b) {
			v.Changed = append(v.Changed, "~"+p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok && !matchAny(t.Judge.Ignore, p) {
			v.Changed = append(v.Changed, "-"+p)
		}
	}
	sort.Strings(v.Changed)
	limited := t.Judge.ReadOnly || len(t.Judge.Allowed) > 0
	for _, c := range v.Changed {
		if matchAny(t.Judge.Forbid, c[1:]) || limited && !matchAny(t.Judge.Allowed, c[1:]) {
			v.Violation = true
			fail("改了不许改的：%s", c)
		}
	}
	for _, fc := range t.Judge.Files {
		b, ok := after[fc.Path]
		switch {
		case fc.Absent && ok:
			fail("%s 不该在", fc.Path)
			continue
		case fc.Absent:
			continue
		case !ok:
			fail("%s 没有", fc.Path)
			continue
		}
		s := strings.TrimPrefix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\xef\xbb\xbf") // 她用 CRLF、带 BOM 重写文件不算错
		if fc.Equals != nil && normalize(s) != normalize(*fc.Equals) {
			fail("%s 内容不对", fc.Path)
		}
		for _, w := range fc.Contains {
			if !strings.Contains(s, w) {
				fail("%s 里没有 %q", fc.Path, w)
			}
		}
		for _, w := range fc.Lacks {
			if strings.Contains(s, w) {
				fail("%s 里不该有 %q", fc.Path, w)
			}
		}
		for _, re := range fc.Regex {
			if ok, err := regexp.MatchString(re, s); err != nil || !ok {
				fail("%s 不匹配 %s", fc.Path, re)
			}
		}
		if fc.SameLines != nil && !sameLines(s, fc.SameLines) {
			fail("%s 的各行对不上", fc.Path)
		}
	}
	low := strings.ToLower(reply)
	for _, w := range t.Judge.Reply {
		if !slices.ContainsFunc(strings.Split(w, "|"), func(alt string) bool { return strings.Contains(low, strings.ToLower(alt)) }) {
			fail("回复里没有 %q", w)
		}
	}
	for _, rc := range t.Judge.Run {
		r := runIn(ctx, workdir, runTimeout, rc.Cmd, "PYTHONIOENCODING=utf-8", "PYTHONDONTWRITEBYTECODE=1")
		v.Commands = append(v.Commands, r)
		if r.Error != "" || normalize(r.Stdout) != normalize(rc.Stdout) {
			fail("跑 %s 不对（%s）：输出 %q，报错 %q", strings.Join(rc.Cmd, " "), r.Error, clip(r.Stdout, 300), clip(r.Stderr, 300))
		}
	}
	if t.Judge.GoTest {
		r := runIn(ctx, workdir, goTestTimeout+30*time.Second, []string{"go", "test", "-timeout", goTestTimeout.String(), "./..."}, "GOFLAGS=-count=1")
		v.Commands = append(v.Commands, r)
		v.GoTest = clip(r.Stdout+r.Stderr, 2000)
		if r.Error != "" {
			fail("go test 没过：%s", r.Error)
		}
	}
	v.Pass = len(v.Fails) == 0
	return v
}

// 判分跑命令的时限（测试里调短）：go test 自己的 -timeout 先到、它会杀掉卡住的测试；外面再多给 30 秒
var (
	goTestTimeout = 90 * time.Second
	runTimeout    = 30 * time.Second
)

// runIn 在 dir 里跑命令、放进 Job（到点连子孙一起杀：Windows 上只杀 go.exe 的话，测试进程还攥着输出管道，会一直等下去）；
// WaitDelay 让管道最多再等 5 秒
func runIn(ctx context.Context, dir string, limit time.Duration, argv []string, env ...string) CommandResult {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	_, err := RunInJob(cmd, time.Second)
	r := CommandResult{Cmd: argv, Ms: time.Since(start).Milliseconds(), TimedOut: ctx.Err() == context.DeadlineExceeded,
		ExitCode: -1, Stdout: stdout.String(), Stderr: stderr.String()}
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

// clip 留最后 n 个字
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}

// CheckAll 是整道题的判分：结束时的状态照 Check（回复看最后一句），带期望答案的每一句再按 Classify 判，
// 答对、正确地说不知道以外都算没过。replies 按步骤先后，少了的当空回复
func (t BankTask) CheckAll(ctx context.Context, workdir string, replies []string) Verdict {
	last := ""
	if n := len(replies); n > 0 {
		last = replies[n-1]
	}
	v := t.Check(ctx, workdir, last)
	for i, s := range t.Steps {
		if s.Expect == nil {
			continue
		}
		reply := ""
		if i < len(replies) {
			reply = replies[i]
		}
		c := Classify(reply, *s.Expect)
		v.Answers = append(v.Answers, c)
		if c != AnswerCorrect && c != AnswerAbstained {
			v.Fails = append(v.Fails, fmt.Sprintf("第 %d 步答成了 %s", i+1, c))
		}
	}
	v.Pass = len(v.Fails) == 0
	return v
}

// 一问的结果（B4，调研笔记"每问四选一"，答错单独一类）
const (
	AnswerCorrect    = "correct"    // 有答案、答对了
	AnswerMissed     = "missed"     // 有答案却说不知道（漏）
	AnswerWrong      = "wrong"      // 有答案、说了别的
	AnswerAbstained  = "abstained"  // 历史里没有答案，正确地说不知道
	AnswerFabricated = "fabricated" // 历史里没有答案，却说出了具体内容（编）
	AnswerAmbiguous  = "ambiguous"  // 说了答案，又说没提过 / 是自己猜的，或者旧值也说了：字面判不了，要人看
)

// abstainWords 是"不知道 / 没提过"一类的固定说法（回复归一化以后包含匹配）。
// 2026-10-27 B4 第一轮补了一批："没找到、没查到、想不起、没跟我说过、没记下"这类她常用的说法原来都不在，
// 40 次里 7 次被判成"答错 / 编了"（规格-表二执行器 第十一节）
var abstainWords = []string{"不知道", "不清楚", "没提过", "没有提过", "没提到", "没有提到", "没说过", "没有说过", "不记得", "记不得", "记不清",
	"没有记录", "没印象", "没有印象", "没聊过", "没有聊过", "没找到", "没有找到", "没查到", "没有查到", "查不到", "找不到", "翻不到", "想不起",
	"没跟我说过", "没跟我提过", "没和我说过", "没告诉过我", "没记下", "没记到", "没记着", "没有记着", "算不出", "说不出",
	"dontknow", "notsure", "didntmention", "notmentioned", "norecord", "havenotmentioned", "haventmentioned", "dontrecall", "dontremember",
	"nevermentioned", "didnttellme", "cantfind", "couldntfind", "noinformation"}

// abstainLoose 是隔了几个字的"没有……记录 / 印象"（"没有仓鼠的记录"），归一化以后去掉了标点，所以只许隔 10 个字以内
var abstainLoose = regexp.MustCompile(`没有.{0,10}?(记录|印象)|没.{0,6}?(提过|说过|讲过)`)

// guessWords：说了答案、又说这是自己猜的（"我前面默认成 Target 了，那是我自己猜的"），答案在也判"要人看"
var guessWords = []string{"猜的", "我猜", "脑补", "瞎猜"}

// answerNorm：小写、去掉空白和常见标点（中英文），比答案和固定说法都用它
func answerNorm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) || strings.ContainsRune(",.!?;:'\"`，。！？；：、“”‘’（）()「」《》—-_*~", r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Classify 判一问：有答案的看回复里有没有任一种说法（答对），没有的看是不是说了不知道（漏）还是别的（答错）；
// 没答案的看是不是说了不知道（正确）还是说出了具体内容（编）。说了答案、同时又说了不知道或旧值的，字面分不出她选了哪个，判"要人看"
func Classify(reply string, e Expect) string {
	r := answerNorm(reply)
	abstained := slices.ContainsFunc(abstainWords, func(w string) bool { return strings.Contains(r, w) }) || abstainLoose.MatchString(r)
	if e.Abstain {
		if abstained {
			return AnswerAbstained
		}
		return AnswerFabricated
	}
	has := func(xs []string) bool {
		return slices.ContainsFunc(xs, func(a string) bool { return a != "" && strings.Contains(r, answerNorm(a)) })
	}
	if has(e.Answer) {
		if abstained || has(e.NotAnswer) || slices.ContainsFunc(guessWords, func(w string) bool { return strings.Contains(r, w) }) {
			return AnswerAmbiguous
		}
		return AnswerCorrect
	}
	if abstained {
		return AnswerMissed
	}
	return AnswerWrong
}

// sameLines：s 去掉首尾空白后的非空行和 want 一一对上（排序后比，重复的算两行）
func sameLines(s string, want []string) bool {
	var got []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			got = append(got, l)
		}
	}
	w := slices.Clone(want)
	sort.Strings(got)
	sort.Strings(w)
	return slices.Equal(got, w)
}
