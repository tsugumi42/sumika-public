package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/jsstr"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
	"sumika-native/internal/toolchain"
)

// ───────────── grep、find（grep.js、find.js、utils/tools-manager.js） ─────────────
// 照 pi 起 rg、fd（ADR-169：不在 Go 里自己写搜索）。rg 多个文件时输出的先后不固定（pi 没加 --sort），fd 搜得快时会自己排好

const grepDefaultLimit, findDefaultLimit = 100, 1000
const grepMaxLineLength = 500

// maxSearchLimit 是 grep、find 的 limit 上限；limit 夹到 1～maxSearchLimit 的整数。
// 不夹的话 find 的 limit:0 会让 fd 不限条数，grep 的 limit:1e999 也没有上限。
const maxSearchLimit = 10000

// searchLimit 取 limit 参数：不是数字用默认值，NaN 当默认值，其余夹到 1～maxSearchLimit 并取整
func searchLimit(args jsjson.Value, def float64) float64 {
	limit := def
	if v, ok := argNumber(args, "limit"); ok {
		limit = v
	}
	if math.IsNaN(limit) {
		limit = def
	}
	return math.Min(math.Max(math.Trunc(limit), 1), maxSearchLimit)
}

// limitHint 是"到 limit 了"之后建议怎么办：翻倍后还在上限内就建议翻倍，否则只建议缩小范围
func limitHint(limit float64) string {
	if limit*2 <= maxSearchLimit {
		return fmt.Sprintf("Use limit=%s for more, or refine pattern", jsjson.Number_(limit*2))
	}
	return fmt.Sprintf("Refine pattern (limit is capped at %d)", maxSearchLimit)
}

// envGlobs 排除 .env 一类文件的 rg 参数，必须放在用户的 glob 后面（rg 的 glob 后写的优先，用户的 glob 盖不过去）。
// 规则和受保护路径判断用的 policy.IsEnvFileName 是同一条（.env 和 .env.任意后缀，不分大小写），所以大小写写成字符组。
// 明确给的文件路径 rg 本来就不看 glob：那种情况审批门已经问过主人了
var envGlobs = []string{"--glob", "!.[eE][nN][vV]", "--glob", "!.[eE][nN][vV].*"}

// isEnvPath 是 rg 报出的路径（正反斜杠都可能）的文件名属于 .env 一类
func isEnvPath(p string) bool {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		p = p[i+1:]
	}
	return policy.IsEnvFileName(p)
}

// toolPath 向登记表要：Sumika 自带的 vendor/bin → PATH；找不到不下载（pi 会去 GitHub 下，Go 版不下，规格第七节 T15）
func toolPath(name string) string {
	if r, ok := toolchain.Lookup(name); ok {
		return r.Path
	}
	return ""
}

// UseVendorBin 把资源根下的 vendor/bin（rg、fd）放到本进程 PATH 最前面；没有这个目录或已经在 PATH 里就什么也不做。
// 桌面程序没有 pi 的 ~/.pi/agent/bin，不放的话 grep、find 工具只会报"找不到 rg / fd"。
func UseVendorBin(root string) {
	dir := filepath.Join(root, "vendor", "bin")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return
	}
	path := os.Getenv("PATH")
	for _, p := range filepath.SplitList(path) {
		if strings.EqualFold(filepath.Clean(p), filepath.Clean(dir)) {
			return
		}
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+path)
}

// truncateLine 照 truncate.js：超过 500 个 UTF-16 码元的一行截断，后面写 "... [truncated]"
func truncateLine(line string) (string, bool) {
	if store.JSLen(line) <= grepMaxLineLength {
		return line, false
	}
	return store.JSSlice(line, grepMaxLineLength) + "... [truncated]", true
}

func argNumber(args jsjson.Value, key string) (float64, bool) {
	v, ok := args.Obj[key]
	if !ok || v.Kind != jsjson.Number {
		return 0, false
	}
	return v.Num, true
}

type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text *string `json:"text"`
		} `json:"path"`
		LineNumber *float64 `json:"line_number"`
		Lines      struct {
			Text *string `json:"text"`
		} `json:"lines"`
	} `json:"data"`
}

func grep(ctx context.Context, cwd string, args jsjson.Value) (string, error) {
	rg := toolPath("rg")
	if rg == "" {
		return "", errors.New("ripgrep (rg) is not available and could not be downloaded")
	}
	pattern, _ := argString(args, "pattern")
	dir, _ := argString(args, "path")
	if dir == "" {
		dir = "."
	}
	searchPath := resolveToCwd(dir, cwd)
	fi, err := os.Stat(searchPath)
	if err != nil {
		return "", fmt.Errorf("Path not found: %s", searchPath)
	}
	isDir := fi.IsDir()
	contextN := 0.0
	if v, ok := argNumber(args, "context"); ok && v > 0 {
		contextN = v
	}
	limit := searchLimit(args, grepDefaultLimit)
	formatPath := func(p string) string {
		if isDir {
			if rel, err := filepath.Rel(searchPath, p); err == nil && rel != "" && rel != "." && !strings.HasPrefix(rel, "..") {
				return strings.ReplaceAll(rel, `\`, "/")
			}
		}
		return filepath.Base(p)
	}
	// --no-config：工具的行为不受用户自己的 rg 配置影响（pi 的原注释）
	rgArgs := []string{"--no-config", "--json", "--line-number", "--color=never", "--hidden"}
	if v := args.Obj["ignoreCase"]; v.Kind == jsjson.Bool && v.Bool {
		rgArgs = append(rgArgs, "--ignore-case")
	}
	if v := args.Obj["literal"]; v.Kind == jsjson.Bool && v.Bool {
		rgArgs = append(rgArgs, "--fixed-strings")
	}
	if g, ok := argString(args, "glob"); ok && g != "" {
		rgArgs = append(rgArgs, "--glob", g)
	}
	rgArgs = append(rgArgs, envGlobs...)
	rgArgs = append(rgArgs, "--", pattern, searchPath)
	cmd := exec.Command(rg, rgArgs...)
	hideWindow(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("Failed to run ripgrep: %s", err)
	}
	stop := context.AfterFunc(ctx, func() { cmd.Process.Kill() })
	type match struct {
		path string
		line float64
		text *string
	}
	var matches []match
	count := 0.0
	skippedEnv := 0 // 兜底：rg 的 glob 排除之外，仍然报出 .env 类文件的匹配，一律丢掉
	limitHit := false
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1<<30)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || count >= limit {
			continue
		}
		var ev rgEvent
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "match" {
			continue
		}
		// 起点是单个文件时不过滤：.env 文件只能是主人在审批卡上批过的那个（审批门对 .env 每次都问），rg 对明确给的文件也不看 glob
		if isDir && ev.Data.Path.Text != nil && isEnvPath(*ev.Data.Path.Text) {
			skippedEnv++
			continue
		}
		count++
		if ev.Data.Path.Text != nil && *ev.Data.Path.Text != "" && ev.Data.LineNumber != nil {
			matches = append(matches, match{*ev.Data.Path.Text, *ev.Data.LineNumber, ev.Data.Lines.Text})
		}
		if count >= limit {
			limitHit = true
			cmd.Process.Kill()
		}
	}
	waitErr := cmd.Wait()
	stop()
	if ctx.Err() != nil {
		return "", errors.New("Operation aborted")
	}
	if !limitHit && waitErr != nil {
		var ee *exec.ExitError
		if !errors.As(waitErr, &ee) || ee.ExitCode() != 1 {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return "", errors.New(msg)
			}
			code := -1
			if ee != nil {
				code = ee.ExitCode()
			}
			return "", fmt.Errorf("ripgrep exited with code %d", code)
		}
	}
	envNotice := fmt.Sprintf("%d match(es) in .env files were skipped: .env files are protected and are not searched", skippedEnv)
	if count == 0 {
		if skippedEnv > 0 {
			return "No matches found\n\n[" + envNotice + "]", nil
		}
		return "No matches found", nil
	}
	cache := map[string][]string{}
	fileLines := func(p string) []string {
		if ls, ok := cache[p]; ok {
			return ls
		}
		var ls []string
		if b, err := os.ReadFile(p); err == nil {
			s := jsstr.DecodeUTF8(b)
			s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
			ls = strings.Split(s, "\n")
		}
		cache[p] = ls
		return ls
	}
	linesTruncated := false
	var outLines []string
	for _, m := range matches {
		rel := formatPath(m.path)
		if contextN == 0 && m.text != nil {
			t := strings.ReplaceAll(*m.text, "\r\n", "\n")
			t = strings.TrimSuffix(strings.ReplaceAll(t, "\r", ""), "\n")
			t, cut := truncateLine(t)
			linesTruncated = linesTruncated || cut
			outLines = append(outLines, fmt.Sprintf("%s:%s: %s", rel, jsjson.Number_(m.line), t))
			continue
		}
		ls := fileLines(m.path)
		if len(ls) == 0 {
			outLines = append(outLines, fmt.Sprintf("%s:%s: (unable to read file)", rel, jsjson.Number_(m.line)))
			continue
		}
		start, end := m.line, m.line
		if contextN > 0 {
			start, end = math.Max(1, m.line-contextN), math.Min(float64(len(ls)), m.line+contextN)
		}
		for cur := start; cur <= end; cur++ {
			text := ""
			// JS 的 lines[current - 1]：不是整数的下标取不到（context 给了小数时）
			if i := cur - 1; i == math.Trunc(i) && i >= 0 && int(i) < len(ls) {
				text = ls[int(i)]
			}
			text, cut := truncateLine(strings.ReplaceAll(text, "\r", ""))
			linesTruncated = linesTruncated || cut
			if cur == m.line {
				outLines = append(outLines, fmt.Sprintf("%s:%s: %s", rel, jsjson.Number_(cur), text))
			} else {
				outLines = append(outLines, fmt.Sprintf("%s-%s- %s", rel, jsjson.Number_(cur), text))
			}
		}
	}
	t := truncateHead(strings.Join(outLines, "\n"), math.MaxInt)
	output := t.content
	var notices []string
	if limitHit {
		notices = append(notices, fmt.Sprintf("%s matches limit reached. %s", jsjson.Number_(limit), limitHint(limit)))
	}
	if t.truncated {
		notices = append(notices, formatSize(maxBytes)+" limit reached")
	}
	if linesTruncated {
		notices = append(notices, fmt.Sprintf("Some lines truncated to %d chars. Use read tool to see full lines", grepMaxLineLength))
	}
	if skippedEnv > 0 {
		notices = append(notices, envNotice)
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return output, nil
}

// relativizeFindResult 照 relativizeFindResultPath：绝对路径改成相对搜索目录的、正斜杠；原来以分隔符结尾（目录）的保留一个 /
func relativizeFindResult(p, searchPath string) string {
	trailing := strings.HasSuffix(p, `\`) || strings.HasSuffix(p, "/")
	rel := p
	if filepath.IsAbs(p) {
		if r, err := filepath.Rel(searchPath, p); err == nil {
			rel = r
			if rel == "." {
				rel = ""
			}
		}
	}
	rel = strings.ReplaceAll(rel, `\`, "/")
	if trailing && !strings.HasSuffix(rel, "/") {
		rel += "/"
	}
	return rel
}

func find(ctx context.Context, cwd string, args jsjson.Value) (string, error) {
	pattern, _ := argString(args, "pattern")
	dir, _ := argString(args, "path")
	if dir == "" {
		dir = "."
	}
	searchPath := resolveToCwd(dir, cwd)
	limit := searchLimit(args, findDefaultLimit)
	fd := toolPath("fd")
	if fd == "" {
		return "", errors.New("fd is not available and could not be downloaded")
	}
	fdArgs := []string{"--glob", "--color=never", "--hidden"}
	// 不在 git 仓库里也认 .gitignore（往上找 .git，找到了就不用加）
	inGit := false
	for cur := searchPath; ; {
		if exists(filepath.Join(cur, ".git")) {
			inGit = true
			break
		}
		up := filepath.Dir(cur)
		if up == cur {
			break
		}
		cur = up
	}
	if !inGit {
		fdArgs = append(fdArgs, "--no-require-git")
	}
	fdArgs = append(fdArgs, "--max-results", jsjson.Number_(limit))
	eff := pattern
	if strings.Contains(pattern, "/") {
		fdArgs = append(fdArgs, "--full-path")
		if !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "**/") && pattern != "**" {
			eff = "**/" + pattern
		}
		// Windows 上路径分隔符两种都认
		eff = strings.ReplaceAll(eff, "/", `[/\\]`)
	}
	fdArgs = append(fdArgs, "--", eff, searchPath)
	cmd := exec.Command(fd, fdArgs...)
	hideWindow(cmd)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("Failed to run fd: %s", err)
	}
	stop := context.AfterFunc(ctx, func() { cmd.Process.Kill() })
	waitErr := cmd.Wait()
	stop()
	if ctx.Err() != nil {
		return "", errors.New("Operation aborted")
	}
	// readline 按 \n、\r\n 分行；最后一行没有换行也算
	raw := stdout.String()
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	if raw == "" {
		lines = nil
	}
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	output := strings.Join(lines, "\n")
	if waitErr != nil && output == "" {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", fmt.Errorf("fd exited with code %d", cmd.ProcessState.ExitCode())
	}
	if output == "" {
		return "No files found matching pattern", nil
	}
	var rel []string
	for _, l := range lines {
		if l = jsstr.Trim(strings.TrimSuffix(l, "\r")); l != "" {
			rel = append(rel, relativizeFindResult(l, searchPath))
		}
	}
	t := truncateHead(strings.Join(rel, "\n"), math.MaxInt)
	res := t.content
	var notices []string
	if float64(len(rel)) >= limit {
		notices = append(notices, fmt.Sprintf("%s results limit reached. %s", jsjson.Number_(limit), limitHint(limit)))
	}
	if t.truncated {
		notices = append(notices, formatSize(maxBytes)+" limit reached")
	}
	if len(notices) > 0 {
		res += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return res, nil
}
