package tools

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
	"sumika-native/internal/toolchain"
)

// 独立复核查出的两个洞：grep 的保护只看路径参数自己的文件名，搜索目录下面的 .env 照样读得出来（rg 带 --hidden，用户给的 glob 原样透传）；
// find、grep 的 limit 不夹，find 的 limit:0 让 fd 不限条数。

// TestMain：测试二进制拷成 rg（Windows 上 rg.exe）放进假的 pi bin 目录时，扮演一个"不听 glob、什么都报"的 rg（测兜底过滤用）
func TestMain(m *testing.M) {
	if out := os.Getenv("FAKE_RG_OUT"); out != "" && strings.EqualFold(filepath.Base(os.Args[0]), fakeExe("rg")) {
		os.Stdout.WriteString(out)
		os.Exit(0)
	}
	// 测试大量 Refresh 登记表；真起版本命令会拖慢、还会干扰句柄数断言。
	restore := toolchain.SetVersionCommandsForTests(nil)
	code := m.Run()
	restore()
	os.Exit(code)
}

func useFakeRg(t *testing.T, events ...string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "vendor", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "bin", fakeExe("rg")), b, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { toolchain.Refresh(toolchain.Env{}) })
	t.Setenv("FAKE_RG_OUT", strings.Join(events, "\n")+"\n")
	env := toolchain.Env{ResourceRoot: dir}
	toolchain.Refresh(env)
}

func rgMatch(path, text string) string {
	return `{"type":"match","data":{"path":{"text":` + strconv.Quote(path) + `},"line_number":1,"lines":{"text":` + strconv.Quote(text+"\n") + `}}}`
}

// 兜底：rg 不听 glob、照样报出 .env 类文件的匹配时，输出阶段丢掉，并在结果末尾说明
func TestGrepDropsEnvMatchesRgStillReports(t *testing.T) {
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	useFakeRg(t, rgMatch(filepath.Join(wd, ".env"), "TOPSECRET"), rgMatch(filepath.Join(wd, "a.txt"), "hello"), rgMatch(wd+"/sub/.env.local", "TOPSECRET2"))
	out := runGrep(t, wd, `{"pattern":"x"}`)
	if strings.Contains(out, "TOPSECRET") || strings.Contains(out, ".env") && !strings.Contains(out, "2 match(es) in .env files were skipped") {
		t.Fatalf("兜底没丢掉 .env 的匹配：\n%s", out)
	}
	if !strings.Contains(out, "a.txt:1: hello") {
		t.Fatalf("普通文件的匹配不该丢：\n%s", out)
	}
	// 只有 .env 的匹配：当没匹配，但说明为什么
	useFakeRg(t, rgMatch(filepath.Join(wd, ".env"), "TOPSECRET"))
	out = runGrep(t, wd, `{"pattern":"x"}`)
	if strings.Contains(out, "TOPSECRET") || !strings.HasPrefix(out, "No matches found") || !strings.Contains(out, "1 match(es) in .env files were skipped") {
		t.Fatalf("只有 .env 匹配时的输出不对：\n%s", out)
	}
}

func useVendorBin(t *testing.T) {
	t.Helper()
	vendor, _ := filepath.Abs(filepath.Join("..", "..", "..", "vendor", "bin"))
	setToolPath(t, vendor+string(os.PathListSeparator)+os.Getenv("PATH"))
	// 这些用例测的是真 ripgrep / fd 的行为；vendor/bin（公开仓库不带）和 PATH 里都没有就跳过并写明，CI 的 Linux 步骤会装
	for _, n := range []string{"rg", "fd"} {
		if toolPath(n) == "" {
			t.Skipf("原因：这条用真 %s 的行为，vendor/bin 和 PATH 里都没有它", n)
		}
	}
}

// fakeExe 是测试里造假程序文件的名字：和 toolchain 登记表找程序的规则一致，Windows 带 .exe，别的系统不带
func fakeExe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func envFixture(t *testing.T) string {
	t.Helper()
	useVendorBin(t)
	wd := t.TempDir()
	for name, body := range map[string]string{
		".env":           "TOPSECRET_ENV\n",
		"sub/.env.local": "TOPSECRET_ENV2\n",
		"sub2/.ENV":      "TOPSECRET_ENV3\n",
		"sub/.env/x.txt": "TOPSECRET_ENV_DIR\n",
		"a.txt":          "hello TOPSECRET_PLAIN\n",
		".envrc":         "TOPSECRET_ENVRC\n",
	} {
		p := filepath.Join(wd, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return wd
}

func runGrep(t *testing.T, wd, argsJSON string) string {
	t.Helper()
	args, err := jsjson.Parse(argsJSON)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Execute(context.Background(), wd, "grep", args)
	if err != nil {
		t.Fatal(err)
	}
	return r.Text
}

func TestGrepSkipsEnvFilesUnderSearchDir(t *testing.T) {
	wd := envFixture(t)
	for _, argsJSON := range []string{
		`{"pattern":"TOPSECRET"}`,
		`{"pattern":"TOPSECRET","path":"."}`,
		`{"pattern":"TOPSECRET","path":"sub"}`,
		`{"pattern":"TOPSECRET","glob":".env*"}`,
		`{"pattern":"TOPSECRET","glob":"**/.env*"}`,
		`{"pattern":"TOPSECRET","glob":"*.{local,ENV}"}`,
		`{"pattern":"TOPSECRET","glob":".ENV"}`,
		`{"pattern":"TOPSECRET","glob":"!a.txt"}`,
		`{"pattern":"TOPSECRET","ignoreCase":true,"context":1}`,
	} {
		out := runGrep(t, wd, argsJSON)
		if strings.Contains(out, "were skipped") {
			t.Errorf("%s：rg 该在源头就排除 .env 类文件，不该靠输出阶段兜底：\n%s", argsJSON, out)
		}
		for _, leak := range []string{"TOPSECRET_ENV\n", "TOPSECRET_ENV2", "TOPSECRET_ENV3", "TOPSECRET_ENV_DIR", ".env:", ".env.local:", ".ENV:", ".env-"} {
			if strings.Contains(out+"\n", leak) {
				t.Errorf("%s 读出了 .env 类文件（%q）：\n%s", argsJSON, leak, out)
			}
		}
	}
	// 不带 glob 时普通文件和 .envrc（不属于 .env 一类）照常搜得到
	out := runGrep(t, wd, `{"pattern":"TOPSECRET"}`)
	if !strings.Contains(out, "a.txt:1: hello TOPSECRET_PLAIN") || !strings.Contains(out, ".envrc:1:") {
		t.Fatalf("普通文件被误伤：\n%s", out)
	}
}

// 主人明确批准过的单个 .env 文件（审批门对 .env 每次都问）：rg 对明确给的文件不看 glob，搜得到
func TestGrepExplicitEnvFileStillWorksAfterApproval(t *testing.T) {
	wd := envFixture(t)
	out := runGrep(t, wd, `{"pattern":"TOPSECRET","path":".env"}`)
	if !strings.Contains(out, "TOPSECRET_ENV") {
		t.Fatalf("明确给的 .env 文件应该搜得到：\n%s", out)
	}
}

func TestEnvFilterSharesPolicyRule(t *testing.T) {
	for _, name := range []string{".env", ".ENV", ".env.local", ".Env.Production", ".envrc", "env", "a.env", ".environment"} {
		want := policy.TouchesProtected(`C:\work\`+name, `C:\work`, nil, false)
		if got := isEnvPath(`C:\work\sub/` + name); got != want {
			t.Errorf("%q：grep 的过滤得 %v，受保护路径判断得 %v", name, got, want)
		}
	}
}

func TestSearchLimitClamped(t *testing.T) {
	parse := func(s string) jsjson.Value {
		v, err := jsjson.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		args string
		want float64
	}{
		{`{}`, 100}, {`{"limit":0}`, 1}, {`{"limit":-5}`, 1}, {`{"limit":2.9}`, 2}, {`{"limit":7}`, 7},
		{`{"limit":10000}`, 10000}, {`{"limit":10001}`, 10000}, {`{"limit":1e999}`, 10000}, {`{"limit":"3"}`, 100},
	} {
		if got := searchLimit(parse(c.args), 100); got != c.want {
			t.Errorf("%s：得到 %v，应该是 %v", c.args, got, c.want)
		}
	}
	if got := searchLimit(jsjson.Value{}, 100); got != 100 {
		t.Errorf("没有参数：得到 %v", got)
	}
	if got := searchLimit(jsjson.Value{Kind: jsjson.Number, Num: math.NaN()}, 100); got != 100 {
		t.Errorf("NaN：得到 %v", got)
	}
	if strings.Contains(limitHint(10000), "limit=20000") || strings.Contains(limitHint(6000), "limit=12000") {
		t.Errorf("提示不能建议超过上限的值：%q %q", limitHint(10000), limitHint(6000))
	}
	if got := limitHint(4); got != "Use limit=8 for more, or refine pattern" {
		t.Errorf("limitHint(4) = %q", got)
	}
}

func TestFindLimitZeroDoesNotMeanUnlimited(t *testing.T) {
	useVendorBin(t)
	wd := t.TempDir()
	for _, n := range []string{"1.txt", "2.txt", "3.txt"} {
		if err := os.WriteFile(filepath.Join(wd, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args, _ := jsjson.Parse(`{"pattern":"*.txt","limit":0}`)
	r, err := Execute(context.Background(), wd, "find", args)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.SplitN(r.Text, "\n\n", 2)[0], ".txt"); n != 1 {
		t.Fatalf("limit:0 应夹成 1 条，得到 %d 条：\n%s", n, r.Text)
	}
	if strings.Contains(r.Text, "limit=0") {
		t.Fatalf("提示还在建议 limit=0：\n%s", r.Text)
	}
}
