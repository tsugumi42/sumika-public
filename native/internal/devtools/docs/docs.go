// Package docs 是文档一致性检查（sumika-dev check docs），照搬自 scripts/check-docs.ts（ADR-065 第 7 条、规格-脚本迁Go 第 2 步）。
// 对应 2026-10-17 文档体检真出过的问题，不预先造检查。只读（--update 只写 scripts/docs-baseline.json）：不开后台、不碰 data/。
//
//	sumika-dev check docs            查一遍，有“错”就退出码 1
//	sumika-dev check docs --update   把旧名的现状写成基线（只在确认是有意的以后用）
//	sumika-dev check docs --staged   读暂存区里的版本、不读工作区（提交前的钩子用：别的会话改了一半没暂存的文件不拦这次提交，2026-10-26）；
//	                                 D11 的记忆目录不在仓库里
//
// D5、D6 2026-11-01 分仓时删了（对 Bun 的 config.ts、daemon/main.ts），编号不补。
// 正则从 JS 搬到 Go（RE2）要注意三处，逐条照 JS 的语义写：
//   - JS 的 \s 含全角空格、NBSP 等，Go 的不含，用 jsSpace；
//   - JS 的 . 和多行 $ 把 \r 当行尾，CRLF 的工作区文件里“(.*)$”不含 \r，Go 的 . 会吃 \r，所以写成 [^\r\n…]*；
//   - JS 的 encodeURI 要自己实现。
package docs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/devtools/gitrun"
)

const baselinePath = "scripts/docs-baseline.json"

// Level 是发现的严重程度。
type Level string

const (
	LevelError Level = "错"
	LevelWarn  Level = "提醒"
)

// Finding 是一条发现。
type Finding struct {
	Check string
	Level Level
	Where string
	What  string
}

// Options 是一次检查的输入。
type Options struct {
	Root   string           // 仓库根
	Staged bool             // 读暂存区
	Update bool             // 写基线
	Now    func() time.Time // 测试里可替换；nil 用系统时间
	MemDir string           // 记忆目录；空用 ~/.claude/projects/D--Projects-Code-sumika/memory
}

// jsSpace 是 JS 正则里 \s 的字符集（放进方括号用）。
const jsSpace = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	adrHeadRe  = regexp.MustCompile(`(?m)^## ADR-(\d{3})：([^\r\n\x{2028}\x{2029}]*)`)
	fixRe      = regexp.MustCompile(`(取代|修正|推翻|改正|覆盖)了?[` + jsSpace + `：:]*ADR-(\d{3})`)
	titleIDsRe = regexp.MustCompile(`ADR-(\d{3}(?:[、，,][` + jsSpace + `]*\d{3})*)`)
	titleSep   = regexp.MustCompile(`[、，,][` + jsSpace + `]*`)
	dateRe     = regexp.MustCompile(`日期：(\d{4}-\d{2}-\d{2})`)
	doneTodoRe = regexp.MustCompile(`^[` + jsSpace + `]*- \[x\]`)
	d13Re      = regexp.MustCompile(`(?:已被|被)[` + jsSpace + `]*\[?ADR-(\d{3})|ADR-(\d{3})\]?(?:\([^)]*\))?[` + jsSpace + `]*(?:改为|取代|修正|推翻)`)
	stagedExt  = regexp.MustCompile(`\.(md|ts|tsx|css|ya?ml|json)$`)
	scopeExt   = regexp.MustCompile(`\.(md|ts|tsx|css)$`)
	docsTop    = regexp.MustCompile(`^docs/[^/]+\.(md|html)$`)
	notesRe    = regexp.MustCompile(`^docs/notes/[^/]+\.md$`)
	memLinkRe  = regexp.MustCompile(`\[\[([\w-]+)\]\]`)
	d10Skip    = regexp.MustCompile(`找不回|没进仓库|不引用`)
)

// oldNames 是 ADR 里“旧名 → 新名”的旧名，只减不增（D8）。
// 号码交给 scan-private：按本机认识的真实号码拦，2026-11-11 起这里不再猜长数字。
// 各名字的来历（照搬自旧脚本的注释）：copy-twemoji、@twemoji/svg 是 Twemoji 下载脚本的旧名，脚本 2026-11-01 随网页分到 sumika-bun，不再查；
// “暂停 → 睡眠模式”不查：视频播放器的暂停是正常用法；“控制台 → 主窗口”是 ADR-081，留下的一处是 Koishi 控制台；
// ctx.language、/api/language 是 ADR-143 改名前的；心声是 ADR-146 改名前的；memory.confirmOccasions 是 ADR-228 改名前的；
// mewtype 是 ADR-250 改名前的；bilibiliFormat 是链接解析模板改名前的（旧值 card / cover-text 现在是 templates 里的 @卡片 / @封面文字）；
// MagpieLabel 是 ADR-390 M1 改名前的（现在是 llm.Model.Label）。规格和决策记录里解释改名的几处有意保留。
var oldNames = []string{"家里", "数据中心", "出门", "在家", "控制台", "ctx.language", "/api/language", "心声", "--w-heart", "su-app-heart", "/api/qq/heart", "qq-heart", "language.ts", "language.reviewBatches", "memory.confirmOccasions", "mewtype", "bilibiliFormat", "MagpieLabel"}

// 旧名扫描不查的文件：决策记录和 LESSONS 自己写着旧名。
var oldNamesSkip = []string{"docs/决策记录.md", "LESSONS.md"}

type adr struct {
	n     int
	title string
	line  int
	body  string
	fixes [][]string // 标题和正文里“取代 / 修正 / 推翻 / 改正 / 覆盖 ADR-xxx”的匹配，解析时算一次，D1 和 D13 共用
}

type checker struct {
	opt       Options
	found     []Finding
	staged    map[string]string
	adrs      []adr
	updateMsg string // --update 写完基线后要打的那一行
}

// readError 是读不到文件这类“检查没法继续”的错误，由 Check 转成 error 返回（旧脚本里是未捕获异常，退出码 1）。
type readError struct{ msg string }

func (c *checker) bad(check, where, what string, level Level) {
	c.found = append(c.found, Finding{check, level, where, what})
}

func (c *checker) git(args ...string) []byte {
	// --staged：继承钩子的环境。git 为钩子设的 GIT_INDEX_FILE 指向“这次要提交的索引”（部分提交时是临时索引），
	// `:文件` 读到的才是这次真正会进提交的内容；GIT_DIR 也要留着（worktree 的钩子里靠它找仓库）。
	// 不加 --staged：清掉全部 GIT_*，别的工具带进来的变量不该让手跑和巡检查到别的仓库。（旧脚本两种情况都继承，只在环境被污染时有区别。）
	var env []string
	if c.opt.Staged {
		env = os.Environ()
	}
	out, _, err := gitrun.Run(c.opt.Root, env, nil, args...)
	if err != nil {
		panic(readError{"起不来 git：" + err.Error()})
	}
	return out
}

func (c *checker) tracked() []string {
	var out []string
	for _, s := range strings.Split(string(c.git("ls-files")), "\n") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// loadStaged 一次读完暂存区里的版本（一个 cat-file --batch；一个文件起一次 git 太慢）。
// 输出是“<哈希> <类型> <字节数>\n<内容>\n”，没有的是“<名字> missing\n”。
func (c *checker) loadStaged(files []string) {
	c.staged = map[string]string{}
	var want []string
	var in bytes.Buffer
	for _, f := range files {
		// 旧脚本把 md、ts、tsx、css、yaml、json 全读一遍（实测 50 MB，git 吐出来要 0.5 秒），而各项检查只读 md、ts、tsx、css 和基线文件；
		// 这里只取会被读到的，读不到的文件检查本来就不看，输出不变。
		if stagedExt.MatchString(f) && (scopeExt.MatchString(f) || f == baselinePath) {
			want = append(want, f)
			in.WriteString(":" + f + "\n")
		}
	}
	out := c.git2(in.Bytes(), "cat-file", "--batch")
	at := 0
	for _, f := range want {
		nl := bytes.IndexByte(out[at:], '\n')
		if nl < 0 {
			break
		}
		head := strings.Split(string(out[at:at+nl]), " ")
		at += nl + 1
		if len(head) != 3 {
			continue
		}
		size, _ := strconv.Atoi(head[2])
		if head[1] == "blob" && at+size <= len(out) {
			c.staged[f] = string(out[at : at+size])
		}
		at += size + 1
	}
}

func (c *checker) git2(stdin []byte, args ...string) []byte {
	out, _, err := gitrun.Run(c.opt.Root, os.Environ(), stdin, args...)
	if err != nil {
		panic(readError{"起不来 git：" + err.Error()})
	}
	return out
}

func (c *checker) read(p string) string {
	if !c.opt.Staged {
		b, err := os.ReadFile(filepath.Join(c.opt.Root, filepath.FromSlash(p)))
		if err != nil {
			panic(readError{err.Error()})
		}
		return string(b)
	}
	s, ok := c.staged[p]
	if !ok {
		panic(readError{"暂存区里没有 " + p})
	}
	return s
}

func adrNo(n int) string { return fmt.Sprintf("ADR-%03d", n) }

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// parseADRs 按“## ADR-nnn：标题”切开决策记录；line 是标题所在行号。
func parseADRs(text string) []adr {
	idx := adrHeadRe.FindAllStringSubmatchIndex(text, -1)
	out := make([]adr, len(idx))
	line, last := 1, 0
	for i, m := range idx {
		line += strings.Count(text[last:m[0]], "\n")
		last = m[0]
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		out[i] = adr{n: atoi(text[m[2]:m[3]]), title: text[m[4]:m[5]], line: line, body: text[m[1]:end]}
		out[i].fixes = fixRe.FindAllStringSubmatch(out[i].title+"\n"+out[i].body, -1)
	}
	return out
}

// Check 跑全部检查。--update 时写基线、不报检查结果（返回的 found 为空）。
func Check(opt Options) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			re, ok := r.(readError)
			if !ok {
				panic(r)
			}
			res, err = Result{}, errors.New(re.msg)
		}
	}()
	c := &checker{opt: opt}
	files := c.tracked()
	if opt.Staged {
		c.loadStaged(files)
	}
	c.d1d2(c.read("docs/决策记录.md"))
	c.d4(files)
	c.d13(files)
	c.d12(files)
	c.d14(files)
	c.d8(files)
	c.d11()
	return Result{Found: c.found, UpdateMsg: c.updateMsg}, nil
}

// Result 是 Check 的结果；--update 时 Found 为空，UpdateMsg 是写基线后的那一行。
type Result struct {
	Found     []Finding
	UpdateMsg string
}

// ———— D1 被推翻 / 修正的旧 ADR，标题后要注明新 ADR 的号；D2 ADR 编号连续、日期不晚于今天、不早于前一条 ————
func (c *checker) d1d2(adrText string) {
	adrs := parseADRs(adrText)
	c.adrs = adrs
	byN := map[int]adr{}
	for _, a := range adrs {
		byN[a.n] = a
	}
	for _, a := range adrs {
		// 新 ADR 里写“取代 / 修正 / 推翻 / 改正 / 覆盖 ADR-xxx”（标题和正文都算）
		for _, m := range fixRe.FindAllStringSubmatch(a.title+"\n"+a.body, -1) {
			old, ok := byN[atoi(m[2])]
			if !ok || old.n >= a.n {
				continue
			}
			// 标题里的号可能连写：“已被 ADR-047、050 修正”
			var ids []int
			for _, x := range titleIDsRe.FindAllStringSubmatch(old.title, -1) {
				for _, s := range titleSep.Split(x[1], -1) {
					ids = append(ids, atoi(s))
				}
			}
			has := false
			for _, id := range ids {
				if id == a.n {
					has = true
				}
			}
			if !strings.Contains(old.title, "已被") || !has {
				c.bad("D1 旧 ADR 没注明", fmt.Sprintf("决策记录.md:%d", old.line),
					fmt.Sprintf("ADR-%s 被 ADR-%03d %s，标题后要写\"已被 ADR-%03d ……\"", m[2], a.n, m[1], a.n), LevelError)
			}
		}
	}

	now := time.Now
	if c.opt.Now != nil {
		now = c.opt.Now
	}
	today := now().Format("2006-01-02") // 本地日期
	prevDate := ""
	for i, a := range adrs {
		if i > 0 && a.n != adrs[i-1].n+1 {
			c.bad("D2 ADR 编号", fmt.Sprintf("决策记录.md:%d", a.line), fmt.Sprintf("%s 前面是 %s，编号不连续", adrNo(a.n), adrNo(adrs[i-1].n)), LevelError)
		}
		m := dateRe.FindStringSubmatch(a.body)
		if m == nil {
			continue
		}
		d := m[1]
		if d > today {
			c.bad("D2 ADR 日期", fmt.Sprintf("决策记录.md:%d", a.line), fmt.Sprintf("%s 写的是 %s，比今天（%s）晚——看系统日期再写", adrNo(a.n), d, today), LevelError)
		}
		if prevDate != "" && d < prevDate {
			c.bad("D2 ADR 日期", fmt.Sprintf("决策记录.md:%d", a.line), fmt.Sprintf("%s 的日期 %s 比前一条 %s 早", adrNo(a.n), d, prevDate), LevelWarn)
		}
		prevDate = d
	}
}

// ———— D4 文档都进了索引；待办里没有删除线条目；D10 引用找不回的出处 ————
func (c *checker) d4(files []string) {
	index := c.read("docs/README.md")
	for _, f := range files {
		if docsTop.MatchString(f) && f != "docs/README.md" {
			name := strings.TrimPrefix(f, "docs/")
			if !strings.Contains(index, "]("+name+")") {
				c.bad("D4 文档没进索引", f, "在 docs/README.md 的\"文件\"表里加一行", LevelError)
			}
		}
	}
	for i, l := range strings.Split(c.read("docs/待办.md"), "\n") {
		if doneTodoRe.MatchString(l) {
			c.bad("D4 待办", fmt.Sprintf("待办.md:%d", i+1), "做完的条目要删掉，不留删除线", LevelError)
		}
	}
	const t = "会话临时目录"
	for _, f := range files {
		if !strings.HasPrefix(f, "docs/") || !strings.HasSuffix(f, ".md") || strings.HasPrefix(f, "docs/notes/") || strings.HasPrefix(f, "docs/archive/") || f == "docs/决策记录.md" {
			continue
		}
		for i, l := range strings.Split(c.read(f), "\n") {
			if strings.Contains(l, t) && !d10Skip.MatchString(l) {
				c.bad("D10 引用找不回的出处", fmt.Sprintf("%s:%d", f, i+1), fmt.Sprintf("\"%s\"会话一关就没了；结论落进 docs/notes/ 再引用", t), LevelWarn)
			}
		}
	}
}

// ———— D13 旧 ADR 又被新 ADR 修正了：别的文档里写着“已被旧 ADR 改为……”的地方，要回头看还对不对 ————
// 2026-11-07 主人同意（复盘-11-06）。证据：ADR-280 改了六份界面文档开头（“已被 ADR-280 改为照 Grok”），当天 ADR-283 又改回一部分，那六处没人回头改。
// 只提醒：文档里有一行说“已被 / 被 ADR-旧 改为、取代、修正”，而整份文档没提到修正它的新 ADR。
func (c *checker) d13(files []string) {
	adrs := c.adrs
	byN := map[int]bool{}
	for _, a := range adrs {
		byN[a.n] = true
	}
	fix := map[int][]int{} // 旧 ADR → 后来修正它的新 ADR（按首次出现的顺序）
	for _, a := range adrs {
		for _, m := range fixRe.FindAllStringSubmatch(a.title+"\n"+a.body, -1) {
			old := atoi(m[2])
			if old < a.n && byN[old] {
				dup := false
				for _, x := range fix[old] {
					if x == a.n {
						dup = true
					}
				}
				if !dup {
					fix[old] = append(fix[old], a.n)
				}
			}
		}
	}
	for _, f := range files {
		if !strings.HasSuffix(f, ".md") || !(strings.HasPrefix(f, "docs/") || f == "AGENTS.md") || strings.HasPrefix(f, "docs/inbox/") || f == "docs/决策记录.md" {
			continue
		}
		text := c.read(f)
		for i, l := range strings.Split(text, "\n") {
			// 先用字面量粗筛：d13Re 每个分支都要有“ADR-”和“被 / 改为 / 取代 / 修正 / 推翻”之一，没有的行不用跑正则。
			// RE2 对这种开头不固定的交替式没有字面量加速，全仓文档逐行跑要 460 毫秒；粗筛只会放过更多行，不会漏掉能匹配的行。
			if !strings.Contains(l, "ADR-") || !(strings.Contains(l, "被") || strings.Contains(l, "改为") || strings.Contains(l, "取代") || strings.Contains(l, "修正") || strings.Contains(l, "推翻")) {
				continue
			}
			for _, m := range d13Re.FindAllStringSubmatch(l, -1) {
				s := m[1]
				if s == "" {
					s = m[2]
				}
				old := atoi(s)
				var newer []string
				for _, n := range fix[old] {
					if !strings.Contains(text, adrNo(n)) {
						newer = append(newer, adrNo(n))
					}
				}
				if len(newer) > 0 {
					c.bad("D13 旧 ADR 又被修正", fmt.Sprintf("%s:%d", f, i+1),
						fmt.Sprintf("这里说被 %s 改过，%s 后来又被 %s 修正；看这句还对不对，对的话在这份文档里提一下新 ADR", adrNo(old), adrNo(old), strings.Join(newer, "、")), LevelWarn)
				}
			}
		}
	}
}

const uriKeep = ";/?:@&=+$,#-_.!~*'()"

// encodeURI 和 JS 的 encodeURI 一样：保留字母数字和 ;/?:@&=+$,#-_.!~*'()，其余按 UTF-8 字节写成 %XX。
func encodeURI(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(uriKeep, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// ———— D12 调研笔记要挂到用处上（2026-10-20：调研做完只留在 notes/ 里，做那块时没人翻）————
// docs/notes/ 下的笔记，要能从 notes 以外的文档顺着链接走到：被外面直接链到的算挂上了，被挂上了的笔记再链到的也算；
// 确实不挂的，笔记前 5 行里写一句“不挂：原因”。
func (c *checker) d12(files []string) {
	var notes []string
	for _, f := range files {
		if notesRe.MatchString(f) {
			notes = append(notes, f)
		}
	}
	mentions := func(text, f string) bool {
		n := strings.TrimPrefix(f, "docs/notes/")
		return strings.Contains(text, n) || strings.Contains(text, encodeURI(n))
	}
	// 历史层（docs/archive/，2026-11-12 起）里的链接不算挂上：挪进历史的文档已经不是现行用处
	var parts []string
	for _, f := range files {
		if strings.HasSuffix(f, ".md") && !strings.HasPrefix(f, "docs/notes/") && !strings.HasPrefix(f, "docs/inbox/") && !strings.HasPrefix(f, "docs/archive/") {
			parts = append(parts, c.read(f))
		}
	}
	outside := strings.Join(parts, "\n")
	hung := map[string]bool{}
	for _, f := range notes {
		if mentions(outside, f) {
			hung[f] = true
		}
	}
	text := map[string]string{}
	get := func(f string) string {
		if s, ok := text[f]; ok {
			return s
		}
		s := c.read(f)
		text[f] = s
		return s
	}
	for grew := true; grew; {
		grew = false
		for _, f := range notes {
			if hung[f] {
				continue
			}
			for _, h := range notes {
				if hung[h] && mentions(get(h), f) {
					hung[f] = true
					grew = true
					break
				}
			}
		}
	}
	for _, f := range notes {
		if hung[f] {
			continue
		}
		head := strings.Split(get(f), "\n")
		if len(head) > 5 {
			head = head[:5]
		}
		skip := false
		for _, l := range head {
			if strings.Contains(l, "不挂：") {
				skip = true
			}
		}
		if skip {
			continue
		}
		c.bad("D12 调研没挂到用处上", f, "在对应的待办条目、规格或 ADR 里链上它（写\"参考：……\"），或者从已经挂上的笔记链过来；不挂就在笔记前 5 行写\"不挂：原因\"", LevelWarn)
	}
}

// 旧名计数：外层按旧名第一次出现的顺序，内层按文件顺序，写基线时保持这个顺序（和旧脚本 JSON.stringify 的输出逐字节一致）。
type nameCounts struct {
	order []string
	files map[string][]fileCount
}

type fileCount struct {
	file string
	n    int
}

func (nc *nameCounts) get(w, f string) int {
	for _, x := range nc.files[w] {
		if x.file == f {
			return x.n
		}
	}
	return 0
}

func (nc *nameCounts) total() int {
	t := 0
	for _, l := range nc.files {
		for _, x := range l {
			t += x.n
		}
	}
	return t
}

func (nc *nameCounts) json() []byte {
	var b bytes.Buffer
	str := func(s string) string {
		var sb bytes.Buffer
		enc := json.NewEncoder(&sb)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(s)
		return strings.TrimRight(sb.String(), "\n")
	}
	b.WriteString("{\n  \"names\": ")
	if len(nc.order) == 0 {
		b.WriteString("{}")
	} else {
		b.WriteString("{\n")
		for i, w := range nc.order {
			fmt.Fprintf(&b, "    %s: {\n", str(w))
			for j, x := range nc.files[w] {
				fmt.Fprintf(&b, "      %s: %d", str(x.file), x.n)
				if j < len(nc.files[w])-1 {
					b.WriteString(",")
				}
				b.WriteString("\n")
			}
			b.WriteString("    }")
			if i < len(nc.order)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString("  }")
	}
	b.WriteString("\n}\n")
	return b.Bytes()
}

// ———— D8 旧名只减不增 ————
func (c *checker) d8(files []string) {
	nc := &nameCounts{files: map[string][]fileCount{}}
	for _, f := range files {
		if !scopeExt.MatchString(f) || strings.HasPrefix(f, "docs/notes/") || strings.HasPrefix(f, "docs/archive/") {
			continue
		}
		skip := false
		for _, s := range oldNamesSkip {
			if f == s {
				skip = true
			}
		}
		if skip {
			continue
		}
		s := c.read(f)
		for _, w := range oldNames {
			if n := strings.Count(s, w); n > 0 {
				if _, ok := nc.files[w]; !ok {
					nc.order = append(nc.order, w)
				}
				nc.files[w] = append(nc.files[w], fileCount{f, n})
			}
		}
	}
	hasBase := false
	if c.opt.Staged {
		_, hasBase = c.staged[baselinePath]
	} else {
		_, err := os.Stat(filepath.Join(c.opt.Root, filepath.FromSlash(baselinePath)))
		hasBase = err == nil
	}
	var base struct {
		Names map[string]map[string]int `json:"names"`
	}
	if hasBase {
		if err := json.Unmarshal([]byte(c.read(baselinePath)), &base); err != nil {
			panic(readError{baselinePath + " 不是合法的 JSON：" + err.Error()})
		}
	}
	if c.opt.Update {
		if err := os.WriteFile(filepath.Join(c.opt.Root, filepath.FromSlash(baselinePath)), nc.json(), 0o644); err != nil {
			panic(readError{err.Error()})
		}
		c.updateMsg = fmt.Sprintf("基线已写到 scripts/docs-baseline.json：旧名 %d 处", nc.total())
		return
	}
	for _, w := range oldNames {
		for _, x := range nc.files[w] {
			was := base.Names[w][x.file]
			if x.n > was {
				c.bad("D8 旧名变多了", x.file, fmt.Sprintf("\"%s\" %d → %d 处（改名见 ADR-061、065；确实有意保留就 --update）", w, was, x.n), LevelError)
			}
		}
	}
}

// ———— D11 记忆里的 [[链接]] 指向的文件要在（记忆目录在本机，不在就跳过）————
func (c *checker) d11() {
	memDir := c.opt.MemDir
	if memDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		memDir = filepath.Join(home, ".claude", "projects", "D--Projects-Code-sumika", "memory")
	}
	// 按系统给的目录顺序读，不排序：旧版用 Node 的 readdirSync，吐的就是这个顺序（NTFS 上接近不区分大小写的排序，
	// 实测 B a _x Z m1 -z 7 A1 读出来是 -z 7 a A1 B m1 Z _x）；os.ReadDir 会按字节排序，顺序不同，提醒的先后就和旧版不一样了。
	dir, err := os.Open(memDir)
	if err != nil {
		return
	}
	ents, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	for _, f := range names {
		b, err := os.ReadFile(filepath.Join(memDir, f))
		if err != nil {
			panic(readError{err.Error()})
		}
		for _, m := range memLinkRe.FindAllStringSubmatch(string(b), -1) {
			if _, err := os.Stat(filepath.Join(memDir, m[1]+".md")); err != nil {
				c.bad("D11 记忆链接", "记忆/"+f, fmt.Sprintf("[[%s]] 指向的文件不在", m[1]), LevelWarn)
			}
		}
	}
}

// Run 解析参数、检查、打印，返回退出码：有“错”为 1，其余为 0；参数不对为 2；读不到文件这类检查没法继续的为 1。
func Run(args []string, root string, stdout, stderr io.Writer) int {
	opt := Options{Root: root}
	for _, a := range args {
		switch a {
		case "--staged":
			opt.Staged = true
		case "--update":
			opt.Update = true
		default:
			fmt.Fprintf(stderr, "不认识的参数 %q\n用法：sumika-dev check docs [--staged] [--update]\n", a)
			return 2
		}
	}
	res, err := Check(opt)
	if err != nil {
		fmt.Fprintln(stderr, "错误：", err)
		return 1
	}
	if opt.Update {
		fmt.Fprintln(stdout, res.UpdateMsg)
		return 0
	}
	found := res.Found
	errs := 0
	for _, x := range found {
		mark := "·"
		if x.Level == LevelError {
			mark = "✗"
			errs++
		}
		fmt.Fprintf(stdout, "%s [%s] %s：%s\n", mark, x.Check, x.Where, x.What)
	}
	if len(found) > 0 {
		fmt.Fprintf(stdout, "%d 个错、%d 个提醒\n", errs, len(found)-errs)
	} else {
		fmt.Fprintln(stdout, "文档检查都过了")
	}
	if errs > 0 {
		return 1
	}
	return 0
}
