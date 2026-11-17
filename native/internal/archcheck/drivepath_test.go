package archcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 运行期默认值不许绑这台电脑（AGENTS 技术约束；ADR-379）：native/internal/ 下非测试的 Go 文件里，
// 字符串字面量解引号后不许出现盘符路径——`D:\…`、`c:/…` 换个用户名、换块盘就找不到。
//
// 判定：`(?i)(^|[^A-Za-z])[A-Z]:[\\/]`，盘符前面不是字母才算（`https://` 里的 `s:/` 不算），
// 冒号后面是 / 或 \。字符串字面量就是 ast.BasicLit 且 Kind == token.STRING（导入路径、结构体标签里的
// 字符串也照查；注释不查，注释里的举例不读盘）。
//
// 不查的地方：native/cmd/ 是开发工具；scripts/ 是 Bun 脚本；测试文件可以用本机路径；
// testdata、vendor 不编进程序。
//
// 存量登记在 drivePathBaseline（drivepath_baseline_test.go），只许变少：
//   - 新出现一处 → 这个测试红；
//   - 登记过的不再成立（文件没了、字面量改了）也红，要从清单删掉那一行，不留死条目。
//
// 看不见的：从配置、环境变量读进来的盘符路径是数据，不是代码，这里查不到；那种靠 env.paths 那项在运行时提醒。

// driveLiteral 是例外清单的一条：文件（相对 native/internal，正斜杠）加解引号后内容的 SHA-256。
// 不拿整段字面量当键：analyzer_gen.go 里的 PowerShell 脚本有十 KB，放进清单没法看也没法评审。
type driveLiteral struct{ File, Hash string }

// driveHit 是一处命中，报错时给人看位置和内容。File 相对 native/internal，用正斜杠。
type driveHit struct {
	File string
	Line int
	Text string
}

// driveScan 是一次扫描的结果。
type driveScan struct {
	files    int                       // 扫过的非测试 .go 文件数，用来确认扫描器确实扫到了东西
	hits     map[driveLiteral]driveHit // 按“文件 + 内容哈希”去重
	problems []string                  // 读不了、解析失败：不能当没有
}

var (
	// drivePath 只回答“有没有盘符路径”。
	drivePath = regexp.MustCompile(`(?i)(^|[^A-Za-z])[A-Z]:[\\/]`)
	// drivePathToken 从命中的字面量里取出盘符路径片段，报错时给人看。边界和 drivePath 一致；
	// 片段停在空白、引号、括号、常见的全角标点：`（C:\x\tool.exe）` 取 `C:\x\tool.exe`。
	drivePathToken = regexp.MustCompile("(?i)(?:^|[^A-Za-z])([A-Z]:[\\\\/][^\\s\"'`（）()\\[\\]，。；：]*)")
)

// literalHash 是例外清单的键的一半：解引号后的内容。
func literalHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// drivePathsIn 列出字面量里的盘符路径片段（去重、保持出现顺序）。
func drivePathsIn(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range drivePathToken.FindAllStringSubmatch(text, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// scanDrivePaths 扫 root 下所有非测试的 .go 文件。testdata、vendor 跳过：那是被测数据和不编进程序的依赖。
// 解析失败的文件记进 problems，不当没有。
func scanDrivePaths(root string) driveScan {
	s := driveScan{hits: map[driveLiteral]driveHit{}}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(p)
		if err != nil {
			s.problems = append(s.problems, rel+"：读不了（"+err.Error()+"）")
			return nil
		}
		f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
		if err != nil {
			s.problems = append(s.problems, rel+"：解析失败（"+err.Error()+"）")
			return nil
		}
		s.files++
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil || !drivePath.MatchString(text) {
				return true
			}
			key := driveLiteral{File: rel, Hash: literalHash(text)}
			if _, ok := s.hits[key]; !ok {
				s.hits[key] = driveHit{File: rel, Line: fset.Position(lit.Pos()).Line, Text: text}
			}
			return true
		})
		return nil
	})
	if err != nil {
		s.problems = append(s.problems, "扫描 "+root+" 失败："+err.Error())
	}
	return s
}

// drivePathProblems 把扫描结果和例外清单对一遍，返回所有问题。纯函数：真实的树和测试里搭的桩树用同一份判断。
func drivePathProblems(scan driveScan, baseline map[driveLiteral]string) []string {
	out := append([]string(nil), scan.problems...)
	keys := make([]driveLiteral, 0, len(scan.hits))
	for k := range scan.hits {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].File != keys[j].File {
			return keys[i].File < keys[j].File
		}
		return keys[i].Hash < keys[j].Hash
	})
	for _, k := range keys {
		if _, ok := baseline[k]; ok {
			continue
		}
		h := scan.hits[k]
		paths := strings.Join(drivePathsIn(h.Text), "、")
		if paths == "" {
			paths = "（取不出片段）"
		}
		out = append(out, fmt.Sprintf("%s:%d 的字符串字面量里写了盘符路径 %s：运行期默认值不能绑这台电脑（AGENTS 技术约束）；改成相对数据目录或配置项。真是示例文案、系统目录兜底，才登记进 drivePathBaseline", h.File, h.Line, paths))
	}
	baseKeys := make([]driveLiteral, 0, len(baseline))
	for k := range baseline {
		baseKeys = append(baseKeys, k)
	}
	sort.Slice(baseKeys, func(i, j int) bool {
		if baseKeys[i].File != baseKeys[j].File {
			return baseKeys[i].File < baseKeys[j].File
		}
		return baseKeys[i].Hash < baseKeys[j].Hash
	})
	for _, k := range baseKeys {
		if _, ok := scan.hits[k]; ok {
			continue
		}
		out = append(out, "drivePathBaseline 里的 "+k.File+"（sha256 "+shortHash(k.Hash)+"）已经不成立（文件没了或字面量改了）：从清单删掉这一项")
	}
	return out
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12] + "…"
	}
	return hash
}

// minInternalGoFiles 是“扫描器确实扫到了整个 internal/”的下限：现在有一千多个非测试的 .go 文件。
// 低于它说明根目录不对或扫描器坏了，不能什么也没查就过。
const minInternalGoFiles = 500

func TestInternalHasNoDriveLetterPaths(t *testing.T) {
	scan := scanDrivePaths("..")
	if scan.files < minInternalGoFiles {
		t.Fatalf("只扫到 %d 个非测试的 .go 文件（internal/ 下有一千多个）：扫描器坏了或根目录不对，这一项什么也没查", scan.files)
	}
	for _, p := range drivePathProblems(scan, drivePathBaseline) {
		t.Error(p)
	}
	for k, reason := range drivePathBaseline {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("drivePathBaseline 里 %s 没写原因：每条都要写清为什么留、什么时候去掉", k.File)
		}
	}
	// 棘轮的弱点：同一个提交里新增一处违规、再往清单加一行，上面查不出来。所以给条数一个写死的上限，
	// 加条目就得同时改它，评审一眼看得见。
	if len(drivePathBaseline) > drivePathBaselineMax {
		t.Errorf("drivePathBaseline 有 %d 项，超过上限 %d：清单只许变少。要加条目先问主人，不要自己把 drivePathBaselineMax 改大", len(drivePathBaseline), drivePathBaselineMax)
	}
	if len(drivePathBaseline) < drivePathBaselineMax {
		t.Errorf("drivePathBaseline 只剩 %d 项，把 drivePathBaselineMax 同步改小到 %d（上限跟着缩，才能挡住以后悄悄加回来）", len(drivePathBaseline), len(drivePathBaseline))
	}
}

// 测试自己的检查：每一种写法、每一个入口都要被扫出来（或被明确放过）。判断逻辑改坏了，这里要红。
func TestDrivePathCheckerShapes(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"bad.go":              "package bad\n\nvar a = \"D:\\\\foo\"\n",
		"raw.go":              "package raw\n\nvar b = `C:\\Windows`\n",
		"comment.go":          "package c\n\n// 不要写 D:\\x\nvar ok = \"https://example.com/x\"\nvar clock = \"12:30/晚上\"\n",
		"skip_test.go":        "package c\n\nvar e = \"E:\\\\x\"\n",
		"sub/deep.go":         "package deep\n\nvar c = `c:/users`\n",
		"testdata/fixture.go": "package fixture\n\nvar f = `F:\\x`\n",
	})

	scan := scanDrivePaths(root)
	if len(scan.problems) != 0 {
		t.Fatalf("干净的桩树不该有问题：%v", scan.problems)
	}
	// bad.go、raw.go、comment.go、sub/deep.go；测试文件和 testdata 不算。
	if scan.files != 4 {
		t.Errorf("应扫到 4 个非测试文件，实际 %d", scan.files)
	}
	want := []driveLiteral{
		{File: "bad.go", Hash: literalHash("D:\\foo")},
		{File: "raw.go", Hash: literalHash(`C:\Windows`)},
		{File: "sub/deep.go", Hash: literalHash("c:/users")},
	}
	if len(scan.hits) != len(want) {
		t.Fatalf("应命中 %d 处，实际 %d：%v", len(want), len(scan.hits), scan.hits)
	}
	for _, w := range want {
		if _, ok := scan.hits[w]; !ok {
			t.Errorf("没扫到 %s 的 %s", w.File, w.Hash)
		}
	}

	// 空清单：三处都报，报出路径片段；注释、测试文件、testdata、https:// 不报。
	joined := strings.Join(drivePathProblems(scan, nil), "\n")
	for _, must := range []string{
		"bad.go:3", `D:\foo`,
		"raw.go:3", `C:\Windows`,
		"sub/deep.go:3", `c:/users`,
	} {
		if !strings.Contains(joined, must) {
			t.Errorf("空清单下应报出 %q，实际：\n%s", must, joined)
		}
	}
	for _, mustNot := range []string{"skip_test.go", "testdata/", "comment.go", "https://"} {
		if strings.Contains(joined, mustNot) {
			t.Errorf("不该报 %q：\n%s", mustNot, joined)
		}
	}

	// 全部登记：不再报。
	full := map[driveLiteral]string{}
	for _, w := range want {
		full[w] = "测试桩"
	}
	if p := drivePathProblems(scan, full); len(p) != 0 {
		t.Errorf("全部登记后不该再报：\n%s", strings.Join(p, "\n"))
	}

	// 只登记两处：剩下的一处仍要报，登记过的不报。
	partial := map[driveLiteral]string{
		want[0]: "测试桩",
		want[1]: "测试桩",
	}
	joined = strings.Join(drivePathProblems(scan, partial), "\n")
	if strings.Contains(joined, "bad.go") || strings.Contains(joined, "raw.go") {
		t.Errorf("登记过的两处不该再报：\n%s", joined)
	}
	if !strings.Contains(joined, "sub/deep.go") {
		t.Errorf("没登记的 sub/deep.go 要报：\n%s", joined)
	}

	// 不成立的登记要报：文件没了、同文件字面量改了的都算。
	stale := map[driveLiteral]string{
		{File: "gone.go", Hash: literalHash(`G:\x`)}:      "测试桩",
		{File: "bad.go", Hash: literalHash(`D:\changed`)}: "测试桩",
	}
	joined = strings.Join(drivePathProblems(scan, stale), "\n")
	for _, must := range []string{
		"drivePathBaseline 里的 gone.go",
		"drivePathBaseline 里的 bad.go",
		"已经不成立",
	} {
		if !strings.Contains(joined, must) {
			t.Errorf("不成立的登记应报出 %q，实际：\n%s", must, joined)
		}
	}
	if !strings.Contains(joined, "sub/deep.go:3") || !strings.Contains(joined, "raw.go:3") {
		t.Errorf("不成立的登记之外，没登记的两处仍要报：\n%s", joined)
	}

	// 解析失败不能当没有。
	writeTree(t, root, map[string]string{"broken.go": "package x\nvar a = \n"})
	if p := scanDrivePaths(root).problems; len(p) == 0 {
		t.Error("有语法错误的文件应当报解析失败，不能悄悄跳过")
	}
}
