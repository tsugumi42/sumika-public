package tools

// 工具执行（第 2 步 2b / 2c），照 pi-coding-agent 的 core/tools（read.js、ls.js、truncate.js、path-utils.js、utils/paths.js）。
// 文件工具保留既有规范正文；命令全文走宿主有界 sink，预览与全文引用由内核分别保存。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/jsstr"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/media"
)

// ResultDisposition 归结果宿主持有；执行返回与上下文取消都不是接纳事实。
type ResultDisposition interface {
	Accept()
	Discard()
}

// Result 是一次工具调用的结果：Text 给模型看，Details 给界面看并存进 steps，IsError 是执行时抛了错
type Result struct {
	Text       string
	IsError    bool
	Details    any
	FullResult *kernel.ResultRef `json:"-"`
	Partial    bool              `json:"-"`
	// Controlled 是宿主标记；外来文字和模型参数不能设置，旧结果精简据此保留正文。
	Controlled bool `json:"-"`
	// ControlledText 是混合结果中不能精简的资料尾部。
	ControlledText string `json:"-"`
	// OnAccepted 只在结果进入实例后调用，丢弃或超时不调用。
	OnAccepted func() `json:"-"`
	// Acceptable 在追加前核对资料来源仍有效；false 时丢弃结果正文。
	Acceptable func() bool `json:"-"`
	// Images 随结果交给模型（read 读图片）；只在内存里，不进 steps、结果存档和日志。
	Images      []media.Image     `json:"-"`
	Disposition ResultDisposition `json:"-"`
}

// Prepare 是 pi 在校验参数之前给工具的一次整理机会（工具的 prepareArguments；现在只有 edit 有）。
// 执行、防卡死提醒用的都是整理以后的参数；steps 里记的、下一次请求里带回去的是模型原来给的
func Prepare(name string, args jsjson.Value) jsjson.Value {
	if name == "edit" {
		return prepareEdit(args)
	}
	return args
}

// Execute 跑一个工具。args 是解析好的参数；cwd 是工作目录。还没搬过来的工具返回错误（调用方不该给出它们的调用）
func Execute(ctx context.Context, cwd, name string, args jsjson.Value) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{Text: "Operation aborted", IsError: true}, nil
	}
	var text string
	var details any
	var images []media.Image
	var err error
	switch name {
	case "read":
		text, images, err = read(ctx, cwd, args)
	case "ls":
		text, err = ls(cwd, args)
	case "todo_write":
		text, err = todo(args)
	case "clipboard":
		text, err = clipboardTool(args)
	case "open":
		text, err = openTool(cwd, args)
	case "notify":
		text, err = notifyTool(ctx, args)
	case "write":
		text, err = write(ctx, cwd, args)
	case "edit":
		text, details, err = edit(ctx, cwd, args)
	case "powershell":
		text, err = powershell(ctx, cwd, args)
	case "process_output":
		text, err = processOutput(ctx, args)
	case "process_stop":
		text, err = processStop(ctx, args)
	case "grep":
		text, err = grep(ctx, cwd, args)
	case "find":
		text, err = find(ctx, cwd, args)
	default:
		// 插件注册的工具（记忆的三个这类）由内核交给插件执行，走不到这里
		return Result{}, fmt.Errorf("没有内置工具 %s", name)
	}
	if err != nil {
		return Result{Text: err.Error(), IsError: true}, nil
	}
	return Result{Text: text, Details: details, Images: images}, nil
}

// ───────────── 路径（utils/paths.js、core/tools/path-utils.js） ─────────────

var unicodeSpaces = regexp.MustCompile("[\u00A0\u2000-\u200A\u202F\u205F\u3000]")
var winShellPath = regexp.MustCompile(`(?i)^/(?:mnt/|cygdrive/)?([a-z])(?:/(.*))?$`)

// normalizeWindowsShellPath：/c/foo、/mnt/c/foo、/cygdrive/c/foo 换成 C:\foo
func normalizeWindowsShellPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) {
		return p
	}
	m := winShellPath.FindStringSubmatch(p)
	if m == nil {
		return p
	}
	return strings.ToUpper(m[1]) + `:\` + strings.ReplaceAll(m[2], "/", `\`)
}

// normalizePath（工具用的那组选项：普通空格化、去掉开头的 @、Windows 的 shell 路径、展开 ~）
func normalizePath(p string, toolInput bool) string {
	if toolInput {
		p = unicodeSpaces.ReplaceAllString(p, " ")
		p = strings.TrimPrefix(p, "@")
	}
	p = normalizeWindowsShellPath(p)
	if home, err := os.UserHomeDir(); err == nil {
		if p == "~" {
			return home
		}
		if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
			return filepath.Join(home, p[2:])
		}
	}
	// file:// 的本机地址换成路径（paths.js 79 行），和审批判定（policy.ToolPath）用的是同一个转换。
	// 转不了的（不是绝对路径、编码过的斜杠）pi 报错，这里先原样留着、当成普通路径去找（规格第七节 T16）
	if strings.HasPrefix(p, "file://") {
		if fp, err := jsstr.FileURLToPath(p); err == nil {
			return fp
		}
	}
	return p
}

// resolveToCwd：相对路径接在工作目录后面，再规范化（node 的 path.resolve）
func resolveToCwd(p, cwd string) string {
	n := normalizePath(p, true)
	if filepath.IsAbs(n) {
		return filepath.Clean(n)
	}
	return filepath.Clean(filepath.Join(normalizePath(cwd, false), n))
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// resolveReadPath：找不到时再试几种 macOS 上的写法（截图名里的窄空格、NFD、弯引号），都没有就用原来的
func resolveReadPath(p, cwd string) string {
	r := resolveToCwd(p, cwd)
	if exists(r) {
		return r
	}
	for _, v := range []string{
		regexp.MustCompile(`(?i) (AM|PM)\.`).ReplaceAllString(r, "\u202F$1."),
		norm.NFD.String(r),
		strings.ReplaceAll(r, "'", "\u2019"),
	} {
		if v != r && exists(v) {
			return v
		}
	}
	return r
}

// ───────────── 截断（truncate.js） ─────────────

const maxLines = 2000
const maxBytes = 50 * 1024

// formatSize：1023B、1.5KB、2.0MB
func formatSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}

type truncation struct {
	content                     string
	truncated, firstLineExceeds bool
	byLines                     bool // 是按行数截的（不是按字节）
	totalLines, outputLines     int
}

// truncateHead：从头留，整行整行地留，行数和字节哪个先到算哪个（JS 的 toFixed 是四舍五入，Go 的 %.1f 也是，在这几个数上一样）
func truncateHead(content string, lineLimit int) truncation {
	lines := []string{}
	if content != "" {
		lines = strings.Split(content, "\n")
		if strings.HasSuffix(content, "\n") {
			lines = lines[:len(lines)-1]
		}
	}
	t := truncation{totalLines: len(lines)}
	if len(lines) <= lineLimit && len(content) <= maxBytes {
		t.content, t.outputLines = content, len(lines)
		return t
	}
	t.truncated = true
	if len(lines[0]) > maxBytes {
		t.firstLineExceeds = true
		return t
	}
	var out []string
	n := 0
	t.byLines = true
	for i := 0; i < len(lines) && i < lineLimit; i++ {
		add := len(lines[i])
		if i > 0 {
			add++
		}
		if n+add > maxBytes {
			t.byLines = false
			break
		}
		out = append(out, lines[i])
		n += add
	}
	if len(out) >= lineLimit && n <= maxBytes {
		t.byLines = true
	}
	t.content, t.outputLines = strings.Join(out, "\n"), len(out)
	return t
}

// ───────────── read（read.js） ─────────────

func argString(args jsjson.Value, key string) (string, bool) {
	v, ok := args.Obj[key]
	if !ok || v.Kind != jsjson.String {
		return "", false
	}
	return v.Str, true
}

// 图片的文件头（utils/mime.js detectSupportedImageMimeType 认的那几种；动图 PNG、某些 JPEG 它不算）
func looksLikeImage(b []byte) bool {
	return bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}) || bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) ||
		bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")) ||
		(bytes.HasPrefix(b, []byte("RIFF")) && len(b) >= 12 && string(b[8:12]) == "WEBP") || bytes.HasPrefix(b, []byte("BM"))
}

// windowText 按 offset、limit 切出一段，整读路线和文档路线共用。
// offset、limit 照 read.js 按 JS 的数算：可以是小数、负数，切片照 Array.prototype.slice（取整、负数从尾巴倒着数），
// 提示里的行号也照 JS 的写法写（2.5、-1 这类）
func windowText(whole, path string, args jsjson.Value) (string, error) {
	all := strings.Split(whole, "\n")
	offset, hasOffset := argNum(args, "offset")
	limit, hasLimit := argNum(args, "limit")
	n := float64(len(all))
	start := 0.0
	if hasOffset && offset != 0 {
		start = math.Max(0, offset-1)
	}
	if start >= n {
		return "", fmt.Errorf("Offset %s is beyond end of file (%d lines total)", jsjson.Number_(offset), len(all))
	}
	var selected string
	userLimited, hasUserLimit := 0.0, false
	if hasLimit {
		end := math.Min(start+limit, n)
		selected, userLimited, hasUserLimit = strings.Join(jsSlice(all, start, end), "\n"), end-start, true
	} else {
		selected = strings.Join(jsSlice(all, start, n), "\n")
	}
	t := truncateHead(selected, maxLines)
	startDisplay := start + 1
	num := jsjson.Number_
	var out string
	switch {
	case t.firstLineExceeds:
		// allLines[startLine]：offset 是小数时取不到那一行，Buffer.byteLength(undefined) 报 node 的这句
		if start != math.Trunc(start) {
			return "", errors.New(`The "string" argument must be of type string or an instance of Buffer or ArrayBuffer. Received undefined`)
		}
		long := all[int(start)]
		out = cutUTF8(long, maxBytes) + "\n\n" + longLineHint(num(startDisplay), formatSize(len(long)), start+1 < n, num(startDisplay+1))
	case t.truncated:
		end := startDisplay + float64(t.outputLines) - 1
		if t.byLines {
			out = fmt.Sprintf("%s\n\n[Showing lines %s-%s of %d. Use offset=%s to continue.]", t.content, num(startDisplay), num(end), len(all), num(end+1))
		} else {
			out = fmt.Sprintf("%s\n\n[Showing lines %s-%s of %d (%s limit). Use offset=%s to continue.]", t.content, num(startDisplay), num(end), len(all), formatSize(maxBytes), num(end+1))
		}
	case hasUserLimit && start+userLimited < n:
		out = fmt.Sprintf("%s\n\n[%s more lines in file. Use offset=%s to continue.]", t.content, num(n-(start+userLimited)), num(start+userLimited+1))
	default:
		out = t.content
	}
	return cleanWindow(out, path)
}

// argNum 是参数里的一个数（不是数就当没给）
func argNum(args jsjson.Value, key string) (float64, bool) {
	v, ok := args.Obj[key]
	if !ok || v.Kind != jsjson.Number {
		return 0, false
	}
	return v.Num, true
}

// jsSlice 是 Array.prototype.slice(start, end)：两头先取整（向零），负数从尾巴倒着数，开头不小于结尾就是空的
func jsSlice(a []string, start, end float64) []string {
	rel := func(x float64) int {
		n := float64(len(a))
		if math.IsNaN(x) {
			return 0
		}
		x = math.Trunc(x)
		if x < 0 {
			return int(math.Max(n+x, 0))
		}
		return int(math.Min(x, n))
	}
	s, e := rel(start), rel(end)
	if s >= e {
		return nil
	}
	return a[s:e]
}

// ───────────── ls（ls.js） ─────────────

// JS 的 localeCompare（Bun 的 ICU，默认语言环境）：用 x/text 的 collate 近似（只比小写以后的，和 pi 一样）。
// Collator 里有每次比较都改写的缓冲区，不能几个协程共用（并行的一批里会有几个 ls 同时跑）：每次 ls 自己建一个
func ls(cwd string, args jsjson.Value) (string, error) {
	path, ok := argString(args, "path")
	if !ok || path == "" {
		path = "."
	}
	dir := resolveToCwd(path, cwd)
	limit, ok := argNum(args, "limit") // 照 JS 按数比（2.5 条就是取到第 3 条才停），提示里也照 JS 写
	if !ok {
		limit = 500
	}
	st, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("Path not found: %s", dir)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("Not a directory: %s", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("Cannot read directory: %s", err.Error())
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	c := collate.New(language.English)
	sortStable(names, func(a, b string) bool { return c.CompareString(strings.ToLower(a), strings.ToLower(b)) < 0 })
	var out []string
	limited := false
	for _, n := range names {
		if float64(len(out)) >= limit {
			limited = true
			break
		}
		fi, err := os.Stat(filepath.Join(dir, n))
		if err != nil {
			continue // 读不到的跳过
		}
		if fi.IsDir() {
			n += "/"
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return "(empty directory)", nil
	}
	t := truncateHead(strings.Join(out, "\n"), int(^uint(0)>>1))
	text := t.content
	var notices []string
	if limited {
		notices = append(notices, fmt.Sprintf("%s entries limit reached. Use limit=%s for more", jsjson.Number_(limit), jsjson.Number_(limit*2)))
	}
	if t.truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", formatSize(maxBytes)))
	}
	if len(notices) > 0 {
		text += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return text, nil
}

func sortStable(xs []string, less func(a, b string) bool) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && less(xs[j], xs[j-1]); j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
