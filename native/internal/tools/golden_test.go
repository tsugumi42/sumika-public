package tools

// 工具层对拍（规格-Go后台 四点七 2c）：读 testdata/cases.json，在临时工作目录里跑 Execute，
// 和 Bun 版跑同一份场景的结果（testdata/golden.json，scripts/go-golden-tools.test.ts 生成）逐个比：
// 给她看的结果文字、是不是出错、跑完以后目录里有什么。
// 有意和 Bun 不一样的场景登记在 testdata/intended.json（带 ADR），结果文字比登记的那份；Bun 哪天也变成一样了就报错，提醒删掉登记

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
)

type toolCase struct {
	Name  string            `json:"name"`
	Tool  string            `json:"tool"`
	Args  json.RawMessage   `json:"args"`
	Files map[string]string `json:"files"`
	Dirs  []string          `json:"dirs"`
	Env   map[string]string `json:"env"` // 跑的时候设上的环境变量
}

type toolGolden struct {
	Name       string             `json:"name"`
	Text       string             `json:"text"`
	IsError    bool               `json:"isError"`
	Files      map[string]*string `json:"files"`
	FullOutput *string            `json:"fullOutput"` // 命令输出太长时存进临时文件的完整输出
	Trash      []string           `json:"trash"`      // 挪进"回收站"的（名字去掉前面的随机串）
}

type intendedDiff struct {
	Name string `json:"name"`
	ADR  string `json:"adr"`
	Text string `json:"text"`
}

var trashName = regexp.MustCompile(`^[0-9a-f]{32}-`)

// mask：工作目录换成 <WORKDIR>（原样的、正斜杠的），再把用户目录换成 <HOME>
func mask(s, wd string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, wd, "<WORKDIR>"), filepath.ToSlash(wd), "<WORKDIR>")
	if home, err := os.UserHomeDir(); err == nil {
		s = strings.ReplaceAll(s, home, "<HOME>")
	}
	return s
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// snapshot 是工作目录里现在有什么：文件 → 内容，空目录 → nil（相对路径，正斜杠）
func snapshot(t *testing.T, root string) map[string]*string {
	out := map[string]*string{}
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(es) == 0 && rel != "" {
			out[rel] = nil
		}
		names := make([]string, 0, len(es))
		for _, e := range es {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, n := range names {
			abs, r := filepath.Join(dir, n), n
			if rel != "" {
				r = rel + "/" + n
			}
			if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
				walk(abs, r)
				continue
			}
			b, err := os.ReadFile(abs)
			if err != nil {
				t.Fatal(err)
			}
			s := string(b)
			out[r] = &s
		}
	}
	walk(root, "")
	return out
}

func TestSameAsBun(t *testing.T) {
	// -short 下照常跑：对拍管着回收站这类保护（ADR-225 第 3 条）
	// 完整命令输出也只写测试目录，不落进真实的系统临时目录。
	temp := t.TempDir()
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	t.Setenv("TMPDIR", temp)
	var cases []toolCase
	var golden []toolGolden
	readJSON(t, "testdata/cases.json", &cases)
	readJSON(t, "testdata/golden.json", &golden)
	if len(cases) != len(golden) {
		t.Fatalf("场景 %d 个，golden %d 个：改了场景要重新生成 golden", len(cases), len(golden))
	}
	var diffs []intendedDiff
	readJSON(t, "testdata/intended.json", &diffs)
	intended := map[string]intendedDiff{}
	for _, d := range diffs {
		intended[d.Name] = d
	}
	for _, g := range golden {
		if d, ok := intended[g.Name]; ok && d.Text == g.Text {
			t.Errorf("%s：Bun 的结果已经和登记的一样了，删掉 intended.json 里这条（%s）", g.Name, d.ADR)
		}
	}
	// grep、find 用的 rg、fd：照 daemon/main.ts 把仓库的 vendor/bin 放在 PATH 最前面（scripts/fetch-tools.ts 下的那一份，Bun 那边也用它）
	vendor, err := filepath.Abs(filepath.Join("..", "..", "..", "vendor", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	setToolPath(t, vendor+string(os.PathListSeparator)+os.Getenv("PATH"))
	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if reason := windowsOnlyGolden(c.Name); reason != "" && runtime.GOOS != "windows" {
				t.Skip("原因：" + reason)
			}
			want := golden[i]
			if want.Name != c.Name {
				t.Fatalf("golden 第 %d 个是 %s", i, want.Name)
			}
			wd := t.TempDir()
			for _, d := range c.Dirs {
				if err := os.MkdirAll(filepath.Join(wd, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for rel, text := range c.Files {
				abs := filepath.Join(wd, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(abs, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// 参数里的 <WORKDIR_URL> 是工作目录的 file:// 地址（和 golden 脚本一样拼）
			args, err := jsjson.Parse(strings.ReplaceAll(string(c.Args), "<WORKDIR_URL>", "file:///"+filepath.ToSlash(wd)))
			if err != nil {
				t.Fatal(err)
			}
			// 删除挪到这个目录，不碰真的回收站（trash.ts 的 SUMIKA_TRASH_TEST_DIR）
			trash := t.TempDir()
			t.Setenv("SUMIKA_TRASH_TEST_DIR", trash)
			for k, v := range c.Env {
				t.Setenv(k, v)
			}
			var sink bytes.Buffer
			r, err := Execute(WithOutputSink(context.Background(), &sink), wd, c.Tool, Prepare(c.Tool, args))
			if err != nil {
				t.Fatal(err)
			}
			text := r.Text
			var full *string
			if want.FullOutput != nil {
				s := mask(sink.String(), wd)
				full = &s
				if !strings.Contains(text, "preview truncated") || strings.Contains(text, ".log") {
					t.Error("未限制预览或仍暴露非托管路径")
				}
			}
			text = mask(text, wd)
			if d, ok := intended[c.Name]; ok {
				want.Text = d.Text
			}
			sameText := true
			switch {
			case want.FullOutput != nil:
				// 旧临时路径文案已退役；完整采集仍逐字核对原始正文。
			case c.Name == "ps-trash-not-empty":
				// 拒绝删除由下面的目录快照断言；5.1 和 7 的诊断排版不属于工具契约。
			case c.Name == "ps-timeout":
				// 4 秒从进程启动算，冷启动也占这段时间；只接受尚无输出，或原来的完整 start 和超时信息。
				sameText = text == "Command timed out after 4 seconds" || text == want.Text
			case c.Tool == "find" && !r.IsError:
				// fd 并行遍历不保证输出顺序，只比命中的路径集合。
				gotLines, wantLines := strings.Split(text, "\n"), strings.Split(want.Text, "\n")
				sort.Strings(gotLines)
				sort.Strings(wantLines)
				sameText = strings.Join(gotLines, "\n") == strings.Join(wantLines, "\n")
			default:
				sameText = text == want.Text
			}
			if !sameText || r.IsError != want.IsError {
				t.Errorf("结果不一样\n Go：%q（出错 %v）\nBun：%q（出错 %v）", text, r.IsError, want.Text, want.IsError)
			}
			if (full == nil) != (want.FullOutput == nil) || (full != nil && *full != *want.FullOutput) {
				t.Errorf("完整输出不一样：Go 有 %v、Bun 有 %v", full != nil, want.FullOutput != nil)
			}
			got, _ := json.Marshal(snapshot(t, wd))
			exp, _ := json.Marshal(want.Files)
			if string(got) != string(exp) {
				t.Errorf("目录里不一样\n Go：%s\nBun：%s", got, exp)
			}
			var trashed []string
			es, _ := os.ReadDir(trash)
			for _, e := range es {
				trashed = append(trashed, trashName.ReplaceAllString(e.Name(), ""))
			}
			sort.Strings(trashed)
			if strings.Join(trashed, "\n") != strings.Join(want.Trash, "\n") {
				t.Errorf("回收站里不一样\n Go：%v\nBun：%v", trashed, want.Trash)
			}
		})
	}
}

// windowsOnlyGolden 说这条对拍场景的基线只在 Windows 上成立，并给出原因；不是的返回空串。
// 基线是 Bun 在 Windows 上录的：PowerShell 输出用 CRLF 换行、错误信息里的路径用反斜杠、cmd 和 Windows 路径写法。
// 照 AGENTS 的约定，Go 的行为按自己的规格，Bun 录像只是 Windows 上的回归基线；Linux 上不拿它当标准。
// 只点名列出的场景，别的场景在 Linux 上照常对拍。
func windowsOnlyGolden(name string) string {
	switch name {
	case "write-backslash":
		return "反斜杠在 Windows 上是路径分隔符，Linux 上是文件名里的一个字符"
	case "write-under-file", "write-under-file-deep", "grep-missing-path", "find-missing-path":
		return "错误信息里的路径基线是 Windows 的反斜杠写法"
	case "find-full-path", "read-file-url":
		return "场景用的是 Windows 的路径写法（盘符、file:///C:/ 形式的 URL）"
	}
	if strings.HasPrefix(name, "ps-") {
		return "PowerShell 工具的基线是 Windows 上 pwsh 的 \r\n 换行、cmd.exe 和 PATH 里的 Windows 目录"
	}
	return ""
}
