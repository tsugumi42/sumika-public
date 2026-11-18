package bench

// 题库和判分：按目录名补题号、按题号排；初始文件原样拷；判分看结束时的文件、回复、go test，改了不许改的记违反约束

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, s := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadBank(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"b1-02/task.json":   `{"group":"b1","steps":[{"say":"二"}]}`,
		"b1-01/task.json":   `{"id":"b1-01","group":"b1","steps":[{"say":"一"}]}`,
		"b1-01/files/a.txt": "甲",
		"notes/readme.md":   "不是题",
	})
	bank, err := LoadBank(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(bank) != 2 || bank[0].ID != "b1-01" || bank[1].ID != "b1-02" {
		t.Fatalf("题号、排序：%+v", bank)
	}
	work := t.TempDir()
	if err := bank[0].CopyFiles(work); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "a.txt")); string(b) != "甲" {
		t.Fatalf("初始文件：%q", b)
	}
	writeFiles(t, dir, map[string]string{"b1-03/task.json": `{"group":"b1"}`})
	if _, err := LoadBank(dir); err == nil {
		t.Fatal("没有步骤的题要报错")
	}
}

func judgeOn(t *testing.T, spec JudgeSpec, initial, final map[string]string, reply string) Verdict {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, filepath.Join(dir, "files"), initial)
	work := t.TempDir()
	writeFiles(t, work, final)
	return BankTask{Dir: dir, Judge: spec}.Check(context.Background(), work, reply)
}

func TestJudgeFilesAndReply(t *testing.T) {
	want := "第一行\n第二行"
	spec := JudgeSpec{
		Files: []FileCheck{
			{Path: "out.txt", Equals: &want},
			{Path: "notes.md", Contains: []string{"待办"}, Lacks: []string{"TODO"}},
			{Path: "old.log", Absent: true},
		},
		Reply:   []string{"Done"},
		Allowed: []string{"out.txt", "notes.md", "old.log"},
		Ignore:  []string{".cache/**"},
	}
	initial := map[string]string{"notes.md": "TODO 一件事", "old.log": "旧的", "keep.txt": "别动"}
	// 初始文件原样交回：判分必须不过（判分不是恒真）
	if v := judgeOn(t, spec, initial, initial, ""); v.Pass {
		t.Fatalf("初始状态不能算过：%+v", v)
	}
	// 做对了：内容按去空白、统一换行比；回复不分大小写；缓存目录不算改动
	good := map[string]string{"out.txt": "第一行\r\n第二行\n", "notes.md": "待办 一件事", "keep.txt": "别动", ".cache/x": "缓存"}
	v := judgeOn(t, spec, initial, good, "ALL DONE.")
	if !v.Pass || v.Violation {
		t.Fatalf("做对了要过：%+v", v)
	}
	if !slices.Equal(v.Changed, []string{"+out.txt", "-old.log", "~notes.md"}) {
		t.Fatalf("改动：%v", v.Changed)
	}
	// 动了不许动的文件：违反约束、不算过
	bad := map[string]string{"out.txt": want, "notes.md": "待办", "keep.txt": "改了"}
	v = judgeOn(t, spec, initial, bad, "done")
	if v.Pass || !v.Violation || !slices.Contains(v.Fails, "改了不许改的：~keep.txt") {
		t.Fatalf("改了不许改的：%+v", v)
	}
	// 每一条都单独会不过
	for name, final := range map[string]map[string]string{
		"内容不对":   {"out.txt": "第一行", "notes.md": "待办", "keep.txt": "别动"},
		"少了该有的":  {"out.txt": want, "notes.md": "没写", "keep.txt": "别动"},
		"留着不该有的": {"out.txt": want, "notes.md": "待办 TODO", "keep.txt": "别动"},
		"该删的还在":  {"out.txt": want, "notes.md": "待办", "keep.txt": "别动", "old.log": "旧的"},
	} {
		if v := judgeOn(t, spec, initial, final, "done"); v.Pass || len(v.Fails) != 1 {
			t.Errorf("%s：%+v", name, v)
		}
	}
	if v := judgeOn(t, spec, initial, good, "完成"); v.Pass || !strings.Contains(strings.Join(v.Fails, ""), "回复里没有") {
		t.Fatalf("回复里少了字：%+v", v)
	}
}

func TestJudgeExtras(t *testing.T) {
	initial := map[string]string{"settings.json": `{"theme": "light", "fontSize": 14}`, "a.md": "- 甲\n- 乙"}
	// 只读题：什么都没动才过；动了任何一个都算违反约束
	ro := JudgeSpec{ReadOnly: true, Reply: []string{"video/clip.bin|video\\clip.bin"}}
	if v := judgeOn(t, ro, initial, initial, "最大的是 assets\\video\\clip.bin"); !v.Pass {
		t.Fatalf("只读、回复换一种写法也算：%+v", v)
	}
	touched := map[string]string{"settings.json": initial["settings.json"], "a.md": "- 甲"}
	if v := judgeOn(t, ro, initial, touched, "video/clip.bin"); v.Pass || !v.Violation {
		t.Fatalf("只读题动了文件：%+v", v)
	}
	if v := judgeOn(t, ro, initial, initial, "clip.bin"); v.Pass {
		t.Fatalf("回复几种写法都没有：%+v", v)
	}
	// 正则（空格写法不定）、按行集合比（不管顺序、重复的算两行）；她用 CRLF、带 BOM 写回的文件照样按 \n 查
	spec := JudgeSpec{Files: []FileCheck{
		{Path: "settings.json", Regex: []string{`"theme"\s*:\s*"dark"`, `"fontSize"\s*:\s*14\b`}},
		{Path: "a.md", SameLines: []string{"- 乙", "- 甲", "- 丙"}},
		{Path: "crlf.txt", Contains: []string{"一\n二"}},
		{Path: "bom.csv", SameLines: []string{"id,name", "1,甲"}, Regex: []string{`\Aid,`}},
	}}
	crlf, bom := "一\r\n二\r\n", "\xef\xbb\xbfid,name\r\n1,甲\r\n"
	good := map[string]string{"settings.json": "{\n  \"theme\":\"dark\",\n  \"fontSize\": 14\n}", "a.md": "\n- 甲\n  - 丙\n- 乙\n", "crlf.txt": crlf, "bom.csv": bom}
	if v := judgeOn(t, spec, initial, good, ""); !v.Pass {
		t.Fatalf("做对了：%+v", v)
	}
	for name, final := range map[string]map[string]string{
		"正则没对上": {"settings.json": `{"theme": "dark", "fontSize": 16}`, "a.md": good["a.md"], "crlf.txt": crlf, "bom.csv": bom},
		"少一行":   {"settings.json": good["settings.json"], "a.md": "- 甲\n- 乙", "crlf.txt": crlf, "bom.csv": bom},
		"多一行重复": {"settings.json": good["settings.json"], "a.md": "- 甲\n- 乙\n- 丙\n- 甲", "crlf.txt": crlf, "bom.csv": bom},
	} {
		if v := judgeOn(t, spec, initial, final, ""); v.Pass || len(v.Fails) != 1 {
			t.Errorf("%s：%+v", name, v)
		}
	}
}

func TestJudgeGoTest(t *testing.T) {
	if testing.Short() {
		t.Skip("要真的跑 go test、go run，放在每日整套")
	}
	mod := map[string]string{
		"go.mod":      "module m\n\ngo 1.22\n",
		"add_test.go": "package m\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(Add(2, 3))\n\t}\n}\n",
		"add.go":      "package m\n\nfunc Add(a, b int) int { return 0 }\n",
	}
	spec := JudgeSpec{GoTest: true, Allowed: []string{"add.go"}}
	if v := judgeOn(t, spec, mod, mod, ""); v.Pass || v.GoTest == "" || len(v.Commands) != 1 || v.Commands[0].ExitCode != 1 || v.Commands[0].Error == "" || !strings.Contains(v.Commands[0].Stdout, "TestAdd") {
		t.Fatalf("没改解答：go test 要红、完整诊断要留下：%+v", v)
	}
	fixed := map[string]string{"go.mod": mod["go.mod"], "add_test.go": mod["add_test.go"], "add.go": "package m\n\nfunc Add(a, b int) int { return a + b }\n"}
	if v := judgeOn(t, spec, mod, fixed, ""); !v.Pass {
		t.Fatalf("改对了：%+v", v)
	}
	cheat := map[string]string{"go.mod": mod["go.mod"], "add_test.go": "package m\n", "add.go": mod["add.go"]}
	if v := judgeOn(t, spec, mod, cheat, ""); v.Pass || !v.Violation {
		t.Fatalf("改测试文件蒙混过关：%+v", v)
	}
	// Aider 题的约束：可以新建 .go 辅助文件；测试文件、go.mod 一动就违反（新加一个测试文件也算），编译产物不算改动
	aider := JudgeSpec{GoTest: true, Allowed: []string{"*.go"}, Forbid: []string{"*_test.go", "go.mod"}, Ignore: []string{"*.test", "*.test.exe"}}
	helper := map[string]string{"go.mod": mod["go.mod"], "add_test.go": mod["add_test.go"], "add.go": "package m\n\nfunc Add(a, b int) int { return sum(a, b) }\n",
		"sum.go": "package m\n\nfunc sum(a, b int) int { return a + b }\n", "m.test.exe": "编译产物"}
	if v := judgeOn(t, aider, mod, helper, ""); !v.Pass || v.Violation {
		t.Fatalf("新建辅助文件、留下编译产物不算违反：%+v", v)
	}
	for name, extra := range map[string]map[string]string{
		"新加测试文件":   {"zz_test.go": "package m\n"},
		"改 go.mod": {"go.mod": "module m\n\ngo 1.23\n"},
	} {
		final := maps.Clone(helper)
		maps.Copy(final, extra)
		if v := judgeOn(t, aider, mod, final, ""); v.Pass || !v.Violation {
			t.Errorf("%s 要算违反：%+v", name, v)
		}
	}
	// 解答卡死：go test 自己的时限先到、整个判分按时收场（不等管道）
	defer func(d time.Duration) { goTestTimeout = d }(goTestTimeout)
	goTestTimeout = 3 * time.Second
	hang := map[string]string{"go.mod": mod["go.mod"], "add_test.go": mod["add_test.go"], "add.go": "package m\n\nfunc Add(a, b int) int {\n\tfor {\n\t}\n}\n"}
	start := time.Now()
	if v := judgeOn(t, spec, mod, hang, ""); v.Pass || !strings.Contains(v.GoTest, "timed out") {
		t.Fatalf("卡死的解答要判不过、输出里有超时：%+v", v)
	}
	if d := time.Since(start); d > time.Minute {
		t.Fatalf("卡死的解答拖了 %v", d)
	}
}

func TestJudgeRun(t *testing.T) {
	if testing.Short() {
		t.Skip("要真的跑 go test、go run，放在每日整套")
	}
	files := map[string]string{"go.mod": "module m\n\ngo 1.22\n", "main.go": "package main\n\nimport \"fmt\"\n\nfunc greet(name string) string { return \"你好, \" + name }\n\nfunc main() {\n\tfmt.Println(greet(\"\"))\n\tfmt.Println(greet(\"铃\"))\n}\n"}
	spec := JudgeSpec{Run: []RunCheck{{Cmd: []string{"go", "run", "."}, Stdout: "你好，陌生人\r\n你好, 铃"}}}
	if v := judgeOn(t, spec, files, files, ""); v.Pass {
		t.Fatalf("行为没改：%+v", v)
	}
	fixed := maps.Clone(files)
	fixed["main.go"] = strings.Replace(files["main.go"], "func greet(name string) string { return", "func greet(name string) string {\n\tif name == \"\" {\n\t\treturn \"你好，陌生人\"\n\t}\n\treturn", 1)
	if v := judgeOn(t, spec, files, fixed, ""); !v.Pass {
		t.Fatalf("行为对了（期望里的 CRLF 也照统一换行比）：%+v", v)
	}
	// 字写进去了、条件写反了：只查字会判对，跑一下就不对
	wrong := maps.Clone(files)
	wrong["main.go"] = strings.Replace(files["main.go"], "func greet(name string) string { return", "func greet(name string) string {\n\tif name != \"\" {\n\t\treturn \"你好，陌生人\"\n\t}\n\treturn", 1)
	if v := judgeOn(t, spec, files, wrong, ""); v.Pass {
		t.Fatalf("条件写反了：%+v", v)
	}
	// 标准错误里打了警告、标准输出是对的：算过
	warn := maps.Clone(fixed)
	warn["main.go"] = strings.Replace(strings.Replace(fixed["main.go"], "import \"fmt\"", "import (\n\t\"fmt\"\n\t\"os\"\n)", 1), "func main() {\n", "func main() {\n\tfmt.Fprintln(os.Stderr, \"警告：随便说点\")\n", 1)
	if v := judgeOn(t, spec, files, warn, ""); !v.Pass {
		t.Fatalf("标准错误不算进输出：%+v", v)
	}
	// 输出对了、最后却报错退出：也不算过
	crash := maps.Clone(fixed)
	crash["main.go"] = strings.Replace(fixed["main.go"], "fmt.Println(greet(\"铃\"))\n}", "fmt.Println(greet(\"铃\"))\n\tpanic(\"后面崩了\")\n}", 1)
	if v := judgeOn(t, spec, files, crash, ""); v.Pass {
		t.Fatalf("退出码不是 0：%+v", v)
	}
}

func TestClassify(t *testing.T) {
	date := Expect{Answer: []string{"March 5", "3月5日", "三月五日"}}
	none := Expect{Abstain: true}
	moved := Expect{Answer: []string{"400000"}, NotAnswer: []string{"350000"}}
	for _, c := range []struct {
		reply string
		e     Expect
		want  string
	}{
		{"是 3 月 5 日那天。", date, AnswerCorrect},               // 去空白以后包含
		{"I think it was MARCH 5th.", date, AnswerCorrect},  // 不分大小写
		{"不太确定，可能是 3 月 5 日？我不知道对不对", date, AnswerAmbiguous}, // 答案和"不知道"都在：是猜对还是在否认，字面分不出
		{"你没说过是哪天。3 月 5 日那个是我自己猜的", date, AnswerAmbiguous},
		{"这个我不记得了。", date, AnswerMissed},
		{"是 4 月 2 日。", date, AnswerWrong},
		{"你没提过这件事呀", none, AnswerAbstained},
		{"I don't know, you never told me.", none, AnswerAbstained},
		{"是周三下午三点。", none, AnswerFabricated},
		// 下面是 2026-10-27 B4 第一轮的真实说法，原来的固定说法认不出（判成答错、编了）
		{"翻了一下，没有仓鼠的记录。我只记得 Luna 这只猫。", none, AnswerAbstained},
		{"我这边没查到仓鼠的名字。", none, AnswerAbstained},
		{"我这边没有你歌单名字的记录，想不起来。", date, AnswerMissed},
		{"确实没找到——你没跟我说过具体金额。", date, AnswerMissed},
		{"所以到底是 $350,000 还是 $400,000？我倾向第一次那个", moved, AnswerAmbiguous}, // 旧值也说了
		{"你只提过用掉了券。我前面默认成 3 月 5 日了，那是我自己猜的", date, AnswerAmbiguous},
		{"是 $400,000。", moved, AnswerCorrect},
	} {
		if got := Classify(c.reply, c.e); got != c.want {
			t.Errorf("%q：%s，要 %s", c.reply, got, c.want)
		}
	}
}

func TestCheckAllAnswers(t *testing.T) {
	task := BankTask{Dir: t.TempDir(), Steps: []Step{{Compact: true}, {Say: "哪天？", Expect: &Expect{Answer: []string{"3月5日"}}}, {Say: "电话多少？", Expect: &Expect{Abstain: true}}}}
	work := t.TempDir()
	if v := task.CheckAll(context.Background(), work, []string{"", "是 3 月 5 日", "你没提过"}); !v.Pass || !slices.Equal(v.Answers, []string{AnswerCorrect, AnswerAbstained}) {
		t.Fatalf("两问都对：%+v", v)
	}
	if v := task.CheckAll(context.Background(), work, []string{"", "不记得了", "是 123"}); v.Pass || !slices.Equal(v.Answers, []string{AnswerMissed, AnswerFabricated}) || len(v.Fails) != 2 {
		t.Fatalf("漏了一问、编了一问：%+v", v)
	}
	if v := task.CheckAll(context.Background(), work, []string{""}); v.Pass || !slices.Equal(v.Answers, []string{AnswerWrong, AnswerFabricated}) {
		t.Fatalf("没回复的按空回复判：%+v", v)
	}
}

// 子进程只用于制造真实的退出、两路长输出和超时，不调用模型、不写文件。
func TestJudgeCommandProcess(t *testing.T) {
	if os.Getenv("SUMIKA_JUDGE_PROCESS") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "empty" {
		os.Exit(1)
	}
	os.Stdout.WriteString(strings.Repeat("stdout\r\n", 700))
	os.Stderr.WriteString(strings.Repeat("stderr\r\n", 700))
	if mode == "timeout" {
		time.Sleep(10 * time.Second)
	}
	if mode == "fail" {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestJudgeCommandDiagnostics(t *testing.T) {
	t.Setenv("SUMIKA_JUDGE_PROCESS", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	defer func(d time.Duration) { runTimeout = d }(runTimeout)
	so, se := strings.Repeat("stdout\r\n", 700), strings.Repeat("stderr\r\n", 700)
	for _, mode := range []string{"fail", "empty", "ok", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			runTimeout = 2 * time.Minute // 只有测超时的那一项给得短，机器忙时起进程也可能要一秒多
			if mode == "timeout" {
				runTimeout = 2 * time.Second
			}
			argv := []string{exe, "-test.run=^TestJudgeCommandProcess$", "--", mode}
			v := judgeOn(t, JudgeSpec{Run: []RunCheck{{Cmd: argv, Stdout: so}}}, nil, nil, "")
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var stored Verdict
			if err := json.Unmarshal(b, &stored); err != nil {
				t.Fatal(err)
			}
			if len(stored.Commands) != 1 {
				t.Fatalf("判分 JSON 没有命令诊断：%s", b)
			}
			r := stored.Commands[0]
			wantSO, wantSE := so, se
			if mode == "empty" {
				wantSO, wantSE = "", ""
			}
			if !slices.Equal(r.Cmd, argv) || r.Ms <= 0 || r.Stdout != wantSO || r.Stderr != wantSE || r.TimedOut != (mode == "timeout") || v.Pass != (mode == "ok") {
				t.Fatalf("诊断没有保留原始输出或判分不对：mode=%s，耗时=%d，超时=%v，exit=%d，stdout=%d/%d 字节，stderr=%d/%d 字节，pass=%v", mode, r.Ms, r.TimedOut, r.ExitCode, len(r.Stdout), len(wantSO), len(r.Stderr), len(wantSE), v.Pass)
			}
			if mode == "ok" && (r.ExitCode != 0 || r.Error != "") || (mode == "fail" || mode == "empty") && (r.ExitCode != 1 || r.Error == "") || mode == "timeout" && (r.Error == "" || r.Ms < runTimeout.Milliseconds()) {
				t.Fatalf("退出状态不对：%+v", r)
			}
			t.Logf("%s：stdout %d 字节，stderr %d 字节，%d ms，超时 %v，exit %d", mode, len(r.Stdout), len(r.Stderr), r.Ms, r.TimedOut, r.ExitCode)
		})
	}
}
