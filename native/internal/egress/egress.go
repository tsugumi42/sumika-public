// Package egress 把一次工具调用的参数里“会把数据带出去的地址”找出来，并看有没有把别处来的内容拼进地址里
// （规格-群聊引用和路由 8.2、8.3，goose 的外发目的地检查）。
//
// 只给主人看，不拦：找到的地址和危险的原因写在审批卡上，编号同时写进对话日志；放不放行仍由审批决定。
// 纯函数、没有任何依赖：参数里的字符串由调用方摊平了给，被引用的原文也由调用方给。
//
// 局限（不假装全能）：只认字面上写出来的地址和常见联网命令的目标；拼出来的、从变量里取的、藏在编码里的看不全，
// 看到“$(”“${”“$env:”这类拼接写法时标红，让主人自己看。记忆和文件内容本身这里读不到，只能按“拼接写法”和“上传文件的写法”间接标出。
package egress

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 危险原因码。编号和原因码进日志（不含正文），原因的中文在 Reason。
const (
	WhyQuoteInURL    = "quote_in_url"   // 被引用的原文（别人的话）出现在地址里
	WhyQuoteInCall   = "quote_in_call"  // 被引用的原文出现在这条联网调用里（请求体、搜索词……）
	WhyShellInURL    = "shell_in_url"   // 地址里有 $(、${、$env: 这类会被替换成别的东西的写法
	WhyUploadFile    = "upload_file"    // 联网命令带着文件内容出去（-d @文件、-InFile、Get-Content | curl ……）
	WhyLongQuery     = "long_query"     // 地址的参数特别长
	WhyEncodedBlob   = "encoded_blob"   // 地址里夹着一大段编码过的东西
	WhyCredentialURL = "credential_url" // 地址里带着用户名密码
)

// Reason 是原因码的中文说法（卡上和记忆页用）
func Reason(code string) string {
	switch code {
	case WhyQuoteInURL:
		return "被引用的原文被拼进了地址"
	case WhyQuoteInCall:
		return "被引用的原文被带进了这次联网调用"
	case WhyShellInURL:
		return "地址里有会被替换成别的内容的写法"
	case WhyUploadFile:
		return "联网命令带着文件内容出去"
	case WhyLongQuery:
		return "地址的参数特别长"
	case WhyEncodedBlob:
		return "地址里夹着一大段编码过的内容"
	case WhyCredentialURL:
		return "地址里带着用户名和密码"
	}
	return "写法可疑"
}

// 目标的种类
const (
	KindURL    = "url"
	KindEmail  = "email"
	KindHost   = "host"   // 联网命令的目标主机
	KindSearch = "search" // 搜索词（搜索引擎那头会看到）
	KindUpload = "upload" // 没找到地址、但命令带着文件内容出去
)

// Target 是一处外发目标。Value 已压成一行、截短；搜索词不放原文。Why 空是不危险。
type Target struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Why   string `json:"why,omitempty"`
}

// Report 是一次调用的外发检查结果。ID 是卡和对话日志共用的编号。
type Report struct {
	ID      string   `json:"id"`
	Targets []Target `json:"targets"`
	// Unchecked：参数太多、太长或嵌套太深，后面的没检查（卡上要说，免得“没有外发块”被当成检查过了）
	Unchecked bool `json:"unchecked,omitempty"`
}

// Danger 数危险的有几处
func (r *Report) Danger() int {
	n := 0
	if r != nil {
		for _, t := range r.Targets {
			if t.Why != "" {
				n++
			}
		}
	}
	return n
}

// Reasons 是危险原因码，去重，按出现先后
func (r *Report) Reasons() []string {
	var out []string
	if r != nil {
		for _, t := range r.Targets {
			if t.Why != "" && !contains(out, t.Why) {
				out = append(out, t.Why)
			}
		}
	}
	return out
}

const (
	maxTargets  = 20
	valueRunes  = 120
	minQuoteRun = 8 // 被引原文至少连着这么多个字出现才算“拼进去了”；更短的巧合太多
	maxQuoteRun = 4000
	maxQuotes   = 20   // 一批最多二十块引用
	maxWindows  = 1500 // 一条被引原文最多比对这么多个窗口（长的隔着取），检查在审批路径上同步跑
	longQuery   = 200
)

// 不联网的本地工具：参数里的网址只是内容，不是外发
var localTools = map[string]bool{
	"read": true, "ls": true, "grep": true, "find": true, "write": true, "edit": true, "remember": true, "memory_forget": true,
	"memory_search": true, "recall_raw": true, "todo_write": true, "schedule_next": true, "skill": true, "clipboard": true,
	"process_output": true, "process_stop": true, "checkpoint_restore": true, "ask_owner": true, "notify": true,
	"read_link_detail": true,
}

// Scan 检查一次工具调用。strs 是参数里所有的字符串（调用方递归摊平），quotes 是这一批悄悄话里被引用的原文。
// 没有外发目标返回 nil。toolCallID 用来定编号：同一次调用在卡和日志里是同一个号。
func Scan(toolCallID, tool string, strs, quotes []string) *Report {
	return ScanChecked(toolCallID, tool, strs, quotes, false)
}

// maxScanBytes：一次调用最多检查这么多字节的参数，再多的不查并标 Unchecked（检查在审批路径上同步跑，不能被超长参数拖住）
const maxScanBytes = 256 << 10

// ScanChecked 同 Scan；unchecked 为真说明调用方摊平参数时已经截掉了一部分（太多或嵌套太深）。
// 内部任何意外（panic）都当没有结果：检查只是信息，不能因为它出错而影响审批。
func ScanChecked(toolCallID, tool string, strs, quotes []string, unchecked bool) (rep *Report) {
	defer func() {
		if recover() != nil {
			rep = nil
		}
	}()
	if localTools[tool] {
		return nil
	}
	total := 0
	for i, s := range strs {
		if total += len(s); total > maxScanBytes {
			strs, unchecked = strs[:i], true
			break
		}
	}
	nq := normalizedQuotes(quotes)
	var targets []Target
	seen := map[string]int{}
	add := func(t Target) {
		t.Value = oneLine(t.Value, valueRunes)
		key := t.Kind + "\x00" + t.Value
		if i, ok := seen[key]; ok {
			if targets[i].Why == "" {
				targets[i].Why = t.Why
			}
			return
		}
		if len(targets) < maxTargets {
			seen[key] = len(targets)
			targets = append(targets, t)
		}
	}
	for _, s := range strs {
		if s == "" {
			continue
		}
		here := len(targets)
		bareURL := tool == "web_fetch" // web_fetch 的参数本来就是地址，没写协议头的也算
		urls := findURLs(s, bareURL)
		for _, u := range urls {
			add(Target{Kind: KindURL, Value: u, Why: judgeURL(u, nq)})
		}
		for _, m := range reEmail.FindAllString(stripURLs(s, urls), -1) {
			add(Target{Kind: KindEmail, Value: m})
		}
		if tool == "web_search" || tool == "wiki_lookup" {
			why := ""
			if hasQuote(s, nq) {
				why = WhyQuoteInCall
			}
			add(Target{Kind: KindSearch, Value: "（搜索词，原文不显示）", Why: why})
		}
		for _, h := range commandHosts(s) {
			add(Target{Kind: KindHost, Value: h})
		}
		// 这条字符串里有联网目标时，再看整条命令的写法
		if len(targets) > here || netCommand(s) {
			up := uploadsFile(s)
			quoted := hasQuote(s, nq)
			marked := false
			for i := here; i < len(targets); i++ {
				if targets[i].Why == "" {
					switch {
					case up:
						targets[i].Why = WhyUploadFile
					case quoted:
						targets[i].Why = WhyQuoteInCall
					}
				}
				marked = marked || targets[i].Why != ""
			}
			if up && !marked && len(targets) == here {
				add(Target{Kind: KindUpload, Value: "（联网命令带着文件内容，没找到地址）", Why: WhyUploadFile})
			}
		}
	}
	if len(targets) == 0 && !unchecked {
		return nil
	}
	sum := sha256.Sum256([]byte(toolCallID))
	return &Report{ID: "E-" + strings.ToUpper(hex.EncodeToString(sum[:3])), Targets: targets, Unchecked: unchecked}
}

var (
	reURL    = regexp.MustCompile(`(?i)\b(?:https?|ftps?|sftp|ssh|git|wss?|smb)://[^\s"'<>` + "`" + `)\]}\\]+|\bgit@[a-z0-9.-]+:[^\s"'<>` + "`" + `]+`)
	reBare   = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9.-]*\.[a-z]{2,}(?::\d+)?(?:[/?#]\S*)?$`)
	reEmail  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	reB64    = regexp.MustCompile(`[A-Za-z0-9+/=_-]{64,}`)
	reShell  = regexp.MustCompile(`(?i)\$\(|\$\{|\$env:|\$[a-z_][a-z0-9_]*`)
	reNetCmd = regexp.MustCompile(`(?i)(?:^|[\s;|&(])(?:curl(?:\.exe)?|wget|iwr|irm|invoke-webrequest|invoke-restmethod|start-bitstransfer|test-netconnection|nslookup|ssh|scp|sftp|ftp|nc|ncat|telnet|rsync)(?:\s|$)`)
	// 联网命令后面的目标：-Uri x、--url x，或命令名后第一个不是选项的词
	reHostArg = regexp.MustCompile(`(?i)(?:^|[\s;|&(])(?:curl(?:\.exe)?|wget|iwr|irm|invoke-webrequest|invoke-restmethod|nslookup|ssh|scp|sftp|ftp|nc|ncat|telnet|test-netconnection|rsync)\s+((?:-{1,2}[A-Za-z][\w-]*(?:\s+(?:[^\s-][^\s]*))?\s+)*)([^\s;|&)]+)`)
	// 带着文件内容出去：数据选项后面跟 @文件、$(…)、Get-Content；上传选项（-T、-InFile）后面跟什么都算；Get-Content 管道给联网命令
	reUpload = regexp.MustCompile(`(?i)(?:--data-binary|--data-raw|--data-urlencode|--data|--form|-body|\s-d|\s-f)\s+['"]?(?:@|\$\(|\(?get-content)|(?:--upload-file|-infile|\s-t)\s+\S|get-content[^|;]*\|\s*(?:curl|iwr|irm|invoke-webrequest|invoke-restmethod|nc|ncat)|\[(?:system\.)?io\.file\]::read`)
)

func findURLs(s string, bare bool) []string {
	var out []string
	for _, m := range reURL.FindAllString(s, -1) {
		out = append(out, strings.TrimRight(m, ".,;:!?。，；"))
	}
	if bare && len(out) == 0 {
		if t := strings.TrimSpace(s); reBare.MatchString(t) {
			out = append(out, t)
		}
	}
	return out
}

func stripURLs(s string, _ []string) string { return reURL.ReplaceAllString(s, " ") }

func netCommand(s string) bool { return reNetCmd.MatchString(s) }

func uploadsFile(s string) bool { return netCommand(s) && reUpload.MatchString(s) }

// commandHosts 取联网命令（curl、ssh、nslookup……）后面的目标主机；已经是完整地址的由 findURLs 处理，这里只补没写协议头的
func commandHosts(s string) []string {
	var out []string
	for _, m := range reHostArg.FindAllStringSubmatch(s, -1) {
		h := strings.Trim(m[2], `'"`)
		switch {
		case h == "" || strings.HasPrefix(h, "-") || strings.Contains(h, "://") || strings.HasPrefix(h, "$") || strings.HasPrefix(h, "@"):
		case strings.Contains(h, ".") || strings.Contains(h, "@") || strings.Contains(h, ":") || len(m[1]) > 0 && strings.Contains(strings.ToLower(m[1]), "uri"):
			out = append(out, h)
		}
	}
	return out
}

// judgeURL 看一个地址有没有把别处的内容拼进去；返回原因码，空是看不出。
func judgeURL(raw string, nq []string) string {
	// 能解析就按各部分看，解析不了（里面有 $(… 之类）就整串看
	probe := raw
	why := ""
	if u, err := url.Parse(raw); err == nil {
		if u.User != nil {
			if _, hasPass := u.User.Password(); hasPass {
				why = WhyCredentialURL
			}
		}
		probe = u.RawPath + "?" + u.RawQuery + "#" + u.Fragment + u.Path
		if len(u.RawQuery) >= longQuery {
			why = firstSet(why, WhyLongQuery)
		}
		if reB64.MatchString(u.RawQuery + u.Fragment + u.Path) {
			why = firstSet(why, WhyEncodedBlob)
		}
	}
	if reShell.MatchString(raw) && !strings.HasPrefix(raw, "git@") {
		return WhyShellInURL
	}
	if hasQuote(raw, nq) || hasQuote(probe, nq) {
		return WhyQuoteInURL
	}
	return why
}

func firstSet(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// normalizedQuotes 把被引原文压成“去掉空白、小写”的形式，截到一个上限；太短的丢掉
func normalizedQuotes(quotes []string) []string {
	var out []string
	for _, q := range quotes {
		if len(out) >= maxQuotes {
			break
		}
		n := squash(q)
		if utf8.RuneCountInString(n) < minQuoteRun {
			continue
		}
		if r := []rune(n); len(r) > maxQuoteRun {
			n = string(r[:maxQuoteRun])
		}
		out = append(out, n)
	}
	return out
}

// hasQuote：s（百分号编码按解码后也看一遍）里有没有连着 minQuoteRun 个字与某条被引原文相同
func hasQuote(s string, nq []string) bool {
	if len(nq) == 0 {
		return false
	}
	forms := []string{squash(s)}
	if d, err := url.QueryUnescape(s); err == nil && d != s {
		forms = append(forms, squash(d))
	}
	if d, err := url.PathUnescape(s); err == nil && d != s {
		forms = append(forms, squash(d))
	}
	for _, q := range nq {
		r := []rune(q)
		step := 1
		if n := len(r) - minQuoteRun + 1; n > maxWindows {
			step = (n + maxWindows - 1) / maxWindows // 隔着取：窗口长 8、步长不超过 3 时仍不会漏掉连着 8+步长 个字的拼接
		}
		for i := 0; i+minQuoteRun <= len(r); i += step {
			w := string(r[i : i+minQuoteRun])
			for _, f := range forms {
				if strings.Contains(f, w) {
					return true
				}
			}
		}
	}
	return false
}

func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case ' ', '\t', '\n', '\r', '　', '+', '%', '-', '_':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func oneLine(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x2028 || r == 0x2029 {
			r = ' '
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > n {
		return string(r[:n]) + "…"
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
