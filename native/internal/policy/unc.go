package policy

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"sumika-native/internal/jsjson"

	"sumika-native/internal/jsstr"
)

// ───────────── 网络共享路径、设备路径：一律当碰到受保护目录 ─────────────
// 管理共享（//localhost/C$/…）、\\?\C:\…、\\.\C:\…、\\?\UNC\… 都能指到本机的同一个文件，
// 但规整出来不是 C:\… 的写法，和受保护目录比不上（复核 2026-11-10：指向数据目录的这些写法 read 会被放行）。
// 不尝试把它们还原成本地路径再比（管理共享、映射盘、符号链接还原不全）：写法一认出来就每次问，
// 走受保护路径那一档（Sticky、只这一次，不吃"自动"、"始终允许"、"这个对话"、放行规则、YOLO）。
// 和 Bun 的 UNC_OR_DEVICE（server/src/policy/decide.ts）同一个办法，这边认得稍宽一点，见各函数的说明。

// uncLike：两个以上分隔符开头（\\server\share、//server/share、\\?\、\\.\、\\?\UNC\），或者 NT 对象路径 \??\。
func uncLike(p string) bool {
	if len(p) >= 2 && isSep(p[0]) && isSep(p[1]) {
		return true
	}
	return strings.HasPrefix(p, `\??\`) || strings.HasPrefix(p, `/??/`)
}

// trimPathHead 去掉工具会去掉的前导 @ 和空白（含不换行空格等），多去不吃亏：多认出来只会多问。
func trimPathHead(p string) string {
	return strings.TrimFunc(unicodeSp.ReplaceAllString(p, " "), func(r rune) bool { return r == '@' || jsstr.IsSpace(r) })
}

// IsNetworkOrDevicePath 是这个路径参数（工具收到的原文）写成了网络共享或设备路径。
// 原文和 ToolPath 前半段处理过的（file://主机/共享/… 会变成 \\主机\共享\…）都看；不依赖 Clean 的结果，
// 因为 Linux 上 Clean 会把开头的两个斜杠并成一个。
func IsNetworkOrDevicePath(p string) bool {
	return uncLike(trimPathHead(p)) || uncLike(trimPathHead(toolPathPre(p)))
}

// openTargetArg：open 工具的 target（和工具一样先去首尾空白）。网址也按路径看一遍——它落不到受保护目录上，不会被多问；别的 scheme 工具会拒绝，看不清就问
func openTargetArg(args jsjson.Value) (string, bool) {
	v := args.Obj["target"]
	if v.Kind != jsjson.String {
		return "", false
	}
	t := strings.TrimSpace(v.Str)
	if t == "" {
		return "", false
	}
	return t, true
}

// rootedAlternatives：无盘符的根相对路径（\data\x、/data/x）在不同地方解释得不一样——策略的 ToolPath 按进程当前盘的根，
// 工具（resolveToCwd）把它当相对路径接在工作目录后面，还有按工作目录所在盘的根。三种都可能指到受保护目录，
// 这里给出另外两种，判受保护时一起看，任一碰到就问（不去统一工具和策略的解析）。
func rootedAlternatives(raw, workdir string) []string {
	p := trimPathHead(toolPathPre(raw))
	if p == "" || !isSep(p[0]) || (len(p) > 1 && isSep(p[1])) {
		return nil
	}
	return []string{resolve(workdir, p), filepath.Clean(filepath.Join(workdir, p))}
}

// isBareDrive：只有盘符（D:）。cd D:、ls D: 是切到或列出 D 盘的当前目录，不是 D:foo 那样的相对路径。
func isBareDrive(p string) bool {
	return len(p) == 2 && p[1] == ':' && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z')
}

// isShareShape：命令里的值像网络共享——两个以上分隔符，后面是 ?/. 再接分隔符（设备路径），或是主机名样子再接分隔符和共享名。
// `//\s*TODO`、`// TODO`、`//comment` 这类正则、注释不是。
func isShareShape(s string) bool {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `\??\`) || strings.HasPrefix(s, "/??/") {
		return true
	}
	j := 0
	for j < len(s) && isSep(s[j]) {
		j++
	}
	return j >= 2 && shareHeadOK(s[j:])
}

func shareHeadOK(rest string) bool {
	if len(rest) > 1 && (rest[0] == '?' || rest[0] == '.') && isSep(rest[1]) {
		return true
	}
	k := 0
	for k < len(rest) && isHostByte(rest[k]) {
		k++
	}
	if k == 0 || k >= len(rest)-1 || !isSep(rest[k]) {
		return false
	}
	return !strings.ContainsRune(" \t\r\n'\"*?[", rune(rest[k+1])) // 共享名不能是空白、引号、通配符：//x/* 这类通配和正则不是
}

// isHostByte：主机名里能出现的字节。从宽认：WebDAV 的 host@SSL、host@8080，中文名的 NAS（UTF-8 多字节），
// 带 !~+%: 的都算；只排除分隔符、空白、引号、通配符和命令里的结构符号。正则、注释不会被误认，
// 靠的是 shareHeadOK 还要求“主机名后面紧跟分隔符再接共享名”（'//\s*TODO'、"// TODO" 主机名那段是空的）。
// 2026-11-11 复核：原来只收 [A-Za-z0-9_.$-]，上面这些写法不算共享，只读命令在 auto、yolo 下直接放行。
func isHostByte(c byte) bool {
	if c >= 0x80 {
		return true
	}
	return c > ' ' && c != 0x7f && !strings.ContainsRune("\\/'\"`*?[]|<>(){};,=", rune(c))
}

// isDriveRelative：盘符后面不是分隔符（C:sdata\c.yml、C:..\x、C:）。这是“C 盘的当前目录”下的相对路径，
// 当前目录是什么我们不知道，字面上比不了受保护目录——看不清就问。C:\…、C:/… 不是；网址的 scheme 不止一个字母，也不是。
func isDriveRelative(p string) bool {
	return len(p) >= 2 && p[1] == ':' && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z') && (len(p) == 2 || !isSep(p[2]))
}

// IsNetworkSharePath 是网络共享路径（`\\主机\共享`、`//主机/共享`、`\\?\UNC\主机\共享`、`\\.\UNC\…`）：解析它的真实路径会真的连过去。
// 本机的设备路径（`\\?\D:\x`、`\\.\D:\x`、`\\?\Volume{GUID}\x`）不算：它们落在本机的盘上，能安全解析成普通的 D:\x，不能因为写法特殊就不解析（解析以后才比得上受保护目录）
func IsNetworkSharePath(p string) bool {
	p = trimPathHead(p)
	if len(p) < 3 || !isSep(p[0]) || !isSep(p[1]) {
		return false
	}
	rest := p[2:]
	if len(rest) >= 2 && (rest[0] == '?' || rest[0] == '.') && isSep(rest[1]) {
		r := rest[2:]
		return len(r) >= 4 && strings.EqualFold(r[:3], "UNC") && isSep(r[3])
	}
	return shareHeadOK(rest)
}

// IsOpaquePath 是这个路径参数（工具收到的原文）落不到一个确定的本地位置上：网络共享、设备路径、驱动器相对路径。
// 一律当碰到受保护目录（每次问），也不对它做任何文件系统访问（联接解析、读内容算指纹、预览）。
func IsOpaquePath(p string) bool {
	return IsNetworkOrDevicePath(p) || isDriveRelative(trimPathHead(p)) || isDriveRelative(trimPathHead(toolPathPre(p)))
}

// commandHasDriveRelative：命令文字里有驱动器相对路径写法（Get-Content C:sdata\c.yml）。
// 盘符是单个字母，前面是开头、空白、引号、括号或 = , ; | < > & { @ : 和反引号，后面紧跟字母数字或 _ . - ~ $ %，
// 而且这一段（到空白、引号、右括号为止）里有 \ 或 /；C:\、C:/、C: 后面是空白或引号、H:mm、T:5、{a:1} 这类不算。
// 代价：C:c.yml 这种没有分隔符的写法命令文字里看不出（工具路径参数那边照样问）。
func commandHasDriveRelative(cmd string) bool {
	for i := 0; i+2 < len(cmd); i++ {
		if cmd[i+1] != ':' || !(cmd[i]|0x20 >= 'a' && cmd[i]|0x20 <= 'z') {
			continue
		}
		if i > 0 {
			prev, _ := utf8.DecodeLastRuneInString(cmd[:i])
			if !jsstr.IsSpace(prev) && !strings.ContainsRune("'\"(=,;|<>&{@:`", prev) {
				continue
			}
		}
		c := cmd[i+2]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_.-~$%", c) >= 0 {
			// 盘符后面这一段（到空白、引号、右括号为止）里要有分隔符才像路径：H:mm、T:5、{a:1} 都没有，不报
			for k := i + 2; k < len(cmd) && !strings.ContainsRune(" \t\r\n'\")", rune(cmd[k])); k++ {
				if isSep(cmd[k]) {
					return true
				}
			}
		}
	}
	return false
}

// commandHasNetworkPath：命令文字里有网络共享、设备路径写法（分析器看不穿的写法也在这一关兜住）。
// 一串分隔符（两个以上）后面紧跟非空白才算；前面紧挨着字母数字、_ 、. 、- 的不算（a//b、..\\x、x\\y 是路径中间），
// 冒号后面的两个斜杠是网址（https://）不算，但 -Path://localhost/… 这种参数写法和反斜杠的算。
// 引号里单独写的 '//'、'\\'（-replace '\\','/'、Split('\\')）后面紧跟引号，不算（Bun 会算，这边更宽松）；'\\server' 这种照算。
func commandHasNetworkPath(cmd string) bool {
	for i := 0; i+1 < len(cmd); i++ {
		if !isSep(cmd[i]) || !isSep(cmd[i+1]) || (i > 0 && isSep(cmd[i-1])) {
			continue
		}
		j := i
		for j < len(cmd) && isSep(cmd[j]) {
			j++
		}
		if j >= len(cmd) {
			return false
		}
		// 后面是空白、引号、括号、逗号、分号、竖线：这串分隔符自己就是个完整的写法（'\\'、"//"、Split('\\')），不是共享路径的开头
		if r, _ := utf8.DecodeRuneInString(cmd[j:]); jsstr.IsSpace(r) || strings.ContainsRune("'\"),;|", r) {
			i = j
			continue
		}
		// 两个以上分隔符之后要像共享：设备路径（\\?\、\\.\），或主机名再接分隔符和共享名。'//\s*TODO'、'//\d+' 这类正则不是
		if !shareHeadOK(cmd[j:]) {
			i = j
			continue
		}
		if i == 0 {
			return true
		}
		prev, _ := utf8.DecodeLastRuneInString(cmd[:i])
		switch {
		case prev == ':':
			k := i - 1
			w := k
			for w > 0 && (isWordByte(cmd[w-1]) || cmd[w-1] == '?') { // https?:// 这类正则里的可选 s
				w--
			}
			word := cmd[w:k]
			onlySlash := !strings.ContainsRune(cmd[i:j], '\\')
			drive := len(word) == 1 && unicode.IsLetter(rune(word[0]))
			scheme := len(word) >= 2 && unicode.IsLetter(rune(word[0]))
			if drive || (scheme && onlySlash) {
				i = j
				continue
			}
			return true
		case unicode.IsLetter(prev) || unicode.IsDigit(prev) || prev == '_' || prev == '.' || prev == '-':
			i = j
			continue
		}
		return true
	}
	return false
}

// localOfShare：拒绝规则用——指向本机某个盘的网络共享、设备路径写法还原成 X:\…，让路径规则也认得出来。
// 只认 \\?\X:\…、\\.\X:\…、\\?\UNC\localhost\X$\…、\\localhost\X$\…、\\127.0.0.1\X$\…（正反斜杠都行）；认不出就原样返回。
// 只在拒绝方向用（多拒不吃亏）；放行、ask 方向不还原（管理共享、映射盘、符号链接还原不全，那边靠“一律问”兜底）。
func localOfShare(p string) string {
	q := strings.ReplaceAll(p, "/", `\`)
	low := strings.ToLower(q)
	for _, pre := range []string{`\\?\unc\`, `\\`} {
		if !strings.HasPrefix(low, pre) {
			continue
		}
		rest := q[len(pre):]
		if strings.HasPrefix(rest, "?") || strings.HasPrefix(rest, ".") {
			break // \\?\X:\…、\\.\X:\… 在下面
		}
		// \\主机\X$\…：主机是本机的各种叫法才还原
		host, tail, _ := strings.Cut(rest, `\`)
		if isLocalHost(host) && len(tail) >= 2 && tail[1] == '$' && isDeviceRoot(tail[0]) {
			return tail[:1] + ":" + tail[2:]
		}
		return p
	}
	for _, pre := range []string{`\\?\`, `\\.\`} {
		if strings.HasPrefix(q, pre) && len(q) >= len(pre)+2 && q[len(pre)+1] == ':' && isDeviceRoot(q[len(pre)]) {
			return q[len(pre):]
		}
	}
	return p
}

// isLocalHost：本机的各种叫法——localhost（末尾可以带点）、127.x.x.x 和 127.1 这类简写、::1 的 ipv6-literal 写法、本机的主机名
func isLocalHost(h string) bool {
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	switch {
	case h == "localhost" || h == "::1.ipv6-literal.net" || h == "0.0.0.0":
		return true
	case h == "127" || strings.HasPrefix(h, "127."):
		for _, c := range h {
			if (c < '0' || c > '9') && c != '.' {
				return false
			}
		}
		return true
	}
	if name, err := os.Hostname(); err == nil && h == strings.ToLower(name) {
		return true
	}
	return false
}

// dropTrailingDotSpace 把路径每一段规整成 Windows 打开文件时认的样子：去掉段末尾的点和空格，
// 去掉段里冒号后面的数据流名（c.yml::$DATA、sdata::$INDEX_ALLOCATION、.env:x 都指到同一个文件或目录）。
// 只用来和受保护目录、规则比：字面上比不上，实际指到同一个地方。
// 开头的盘符（C:）、单独的 . 和 .. 段、全是点和空格的段原样留着。
func dropTrailingDotSpace(p string) string {
	if !strings.ContainsAny(p, ". :") {
		return p
	}
	var b strings.Builder
	start := 0
	for i := 0; i <= len(p); i++ {
		if i < len(p) && !isSep(p[i]) {
			continue
		}
		seg := p[start:i]
		if k := strings.IndexByte(seg, ':'); k > 0 && !(start == 0 && k == 1) {
			seg = seg[:k]
		}
		if t := strings.TrimRight(seg, ". "); t != "" {
			seg = t
		}
		b.WriteString(seg)
		if i < len(p) {
			b.WriteByte(p[i])
		}
		start = i + 1
	}
	return b.String()
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '.' || c == '-'
}
