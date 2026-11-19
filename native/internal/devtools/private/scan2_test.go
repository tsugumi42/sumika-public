package private

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// 第四轮复核：同一行文字换个写法就能躲过按连续 ASCII 比较的规则。下面每一组都是“换了写法也要拦”的回归测试。
// 带密钥样子的字符串都在运行时拼出来，源码里直接写会被仓库自己的扫描拦下。

func utf16Bytes(s string, bigEndian bool) string {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		if bigEndian {
			b = append(b, byte(u>>8), byte(u))
		} else {
			b = append(b, byte(u), byte(u>>8))
		}
	}
	return string(b)
}

func TestScanUTF16(t *testing.T) {
	for _, be := range []bool{false, true} {
		for _, text := range []string{"号 " + fakeReal, "写信给 " + fakeMail, "编的昵称甲", "Zorvex 说", "qq_group_" + fakeGroup} {
			enc := utf16Bytes(text, be)
			if !blocked(enc + "\x00") { // 大端文件的换行 00 0A 的 NUL 留在上一行末尾
				t.Errorf("UTF-16（大端 %v）里的要拦：%q", be, text)
			}
			if !blocked(utf16Bytes("\ufeff"+text, be)) { // 第一行靠 BOM 认
				t.Errorf("UTF-16（大端 %v、带 BOM）里的要拦：%q", be, text)
			}
			if !blocked("\x00" + enc) { // 按行切开后，小端文件的下一行开头会多一个 NUL，对齐错一位
				t.Errorf("UTF-16（大端 %v、错位一字节）里的要拦：%q", be, text)
			}
		}
	}
	if blocked(utf16Bytes("一句普通的话 12345", false)) {
		t.Error("UTF-16 里没有认识的东西，不拦")
	}
}

func TestLooksUTF16(t *testing.T) {
	if !looksUTF16([]byte(utf16Bytes("\ufeff号 "+fakeReal, false))) || !looksUTF16([]byte(utf16Bytes("号 "+fakeReal+" 又一句话，凑够长度", true))) {
		t.Error("UTF-16 文本要认出来（BOM 或 NUL 规整地隔着）")
	}
	bin := make([]byte, 4096)
	for i := range bin {
		bin[i] = byte(i * 7 % 256) // NUL 零散、奇偶都有
	}
	if looksUTF16(bin) || looksUTF16([]byte("1111222\x00")) {
		t.Error("真正的二进制不能当成 UTF-16")
	}
}

func TestScanFolding(t *testing.T) {
	full := func(s string) string { // 半角 ASCII 换成全角
		var b strings.Builder
		for _, r := range s {
			if r >= '!' && r <= '~' {
				r += 0xFEE0
			}
			b.WriteRune(r)
		}
		return b.String()
	}
	for _, text := range []string{
		full(fakeReal), "号 " + full(fakeGroup), full("Zorvex"), "zorvex", "ZORVEX", "编\u200b的\u200c昵称甲", fakeReal[:4] + "\u200b" + fakeReal[4:], "Zor\u2060vex",
	} {
		if !blocked(text) {
			t.Errorf("换了写法也要拦：%q", text)
		}
	}
	// 名字里有空格：不间断空格、全角空格、多个空格都对得上
	k := NewKnown([]string{fakeReal}, []string{"Ann Lee"}, nil)
	hasName := func(text string) bool {
		for _, h := range Scan([]Line{{Where: "w", Text: text, File: "a.md"}}, k) {
			if h.Kind == "真实名字" {
				return true
			}
		}
		return false
	}
	for _, text := range []string{"ann lee", "Ann  Lee", "Ann\u00a0Lee", "ann\u3000\u3000lee", "ANN \u2009 LEE"} {
		if !hasName(text) {
			t.Errorf("名字换了空白写法要拦：%q", text)
		}
	}
	if hasName("Annelee 和 Ann Leeds") {
		t.Error("英文名要整词对上")
	}
}

func TestScanLongKnownNumbers(t *testing.T) {
	for _, text := range []string{"0" + fakeReal, fakeReal + "00", "9" + fakeReal + "9", "12" + fakeGroup + "3456", "00000" + fakeReal + "00000"} {
		if !blocked(text) {
			t.Errorf("认识的号码嵌在更长的数字里要拦：%s", text)
		}
	}
	// 8 位以下的认识的号码（fakeOld 5 位）不按“包含”算，不然随便哪串数字都会撞上
	if blocked("1" + fakeOld + "9") {
		t.Error("5 位的认识的号码嵌在更长的数字里不拦")
	}
}

func TestScanMoreSecrets(t *testing.T) {
	jwt := "ey" + "J" + strings.Repeat("a", 12) + "." + strings.Repeat("b", 12) + "." + strings.Repeat("c", 8)
	aws := "AK" + "IA" + strings.Repeat("A1", 8)
	for name, text := range map[string]string{
		"JSON 键带引号": `{"` + "api" + `_key": "` + strings.Repeat("a1", 12) + `"}`,
		"单引号":       "'" + "pass" + "word'" + ": '" + strings.Repeat("a1", 12) + "'",
		"JWT":       "token is " + jwt,
		"AWS":       "id " + aws,
		"Basic":     "Authorization: " + "Ba" + "sic " + strings.Repeat("dXNl", 5),
		"Bearer 头":  `"authorization": "` + "Bea" + "rer " + strings.Repeat("x9", 8) + `"`,
	} {
		if !blocked(text) {
			t.Errorf("%s 要拦：%q", name, text)
		}
	}
	if blocked("Authorization: Basic abc") || blocked("eyJhbGciOi.short.") {
		t.Error("太短的不是密钥")
	}
}

// blockedIn：和 blocked 一样，但可以指定这一行所在的文件（空串 = 提交说明、ref 名这类没有文件的位置）。
func blockedIn(file, text string) bool {
	for _, h := range Scan([]Line{{Where: "w", Text: text, File: file}}, testKnown()) {
		if h.Block {
			return true
		}
	}
	return false
}

// 赋值类规则：值没有引号、又是代码里的变量引用时不算密钥。
//   - 点分的标识符链、任何一段都不含数字：任何文件里都不算；
//   - 函数调用、不含数字的裸标识符：只在代码文件（.go .ts .py 等）里不算；配置文件、无扩展名、提交说明里照拦；
//   - 带引号的字面值、含数字的点分串和裸值、带连字符的值：照拦。
//
// 样例里的关键词和值都在运行时拼，源码里直接写会被仓库自己的扫描拦下。
func TestAssignCodeReferences(t *testing.T) {
	tok, key, pw := "to"+"ken", "api"+"Key", "pass"+"word"
	long := strings.Repeat("a1", 12)
	notBlockedInCode := []string{
		"const " + tok + " = found.entry.raw.accessToken",
		"return { ok: true, " + tok + ": found.entry." + tok + " }",
		"get" + "Api" + "Key: this.ctx.llm.apiKeyFor,",
		pw + " = loadPasswordFromEnvironment()",
		tok + " = someVeryLongVariableNameForIt;",
	}
	for _, f := range []string{"a.go", "src/b.ts", "c.TSX", "d.py", "e.mjs", "f.rs"} {
		for _, text := range notBlockedInCode {
			if blockedIn(f, text) {
				t.Errorf("%s：代码里的变量引用不该当密钥：%q", f, text)
			}
		}
	}
	// 点分的标识符链（没有数字）在任何文件里都不算
	for _, f := range []string{"a.go", "a.yml", ".env", "Makefile", ""} {
		if blockedIn(f, "const "+tok+" = found.entry.raw.accessToken") {
			t.Errorf("%q：不含数字的点分链不拦", f)
		}
	}
	// 函数调用、纯字母的裸值：配置文件、无扩展名、没有文件（提交说明、ref 名）里照拦
	for _, f := range []string{"a.env", ".env", "a.yml", "a.yaml", "a.ini", "a.toml", "a.json", "a.sh", "a.cmd", "a.ps1", "a.conf", "Makefile", "dir.d/noext", ""} {
		for _, text := range []string{
			pw + "=" + "hunter" + "hunter" + "hunterhunter",
			pw + " = loadPasswordFromEnvironment(",
			tok + ": someVeryLongVariableNameForIt",
		} {
			if !blockedIn(f, text) {
				t.Errorf("%q：非代码文件里纯字母的口令和带括号的值照拦：%q", f, text)
			}
		}
	}
	// 带数字的点分串、带引号的字面值、含数字或连字符的裸值：代码文件里也照拦
	for _, text := range []string{
		tok + " = abc1.def2.ghi3jkl4mnop",
		tok + " = a1b2c3.d4e5f6.g7h8i9j0k1l2",
		tok + " = \"" + long + "\"",
		"'" + pw + "': '" + long + "'",
		"{\"" + key + "\": \"" + long + "\"}",
		tok + " = '" + "some.dotted.literal.value.here" + "'",
		pw + "=" + long,
		tok + ": top-secret-sentinel-value",
		tok + " = " + strings.Repeat("a", 16) + "-" + strings.Repeat("b", 8),
	} {
		if !blockedIn("a.go", text) || !blockedIn("a.env", text) {
			t.Errorf("字面值、含数字的点分串照旧要拦：%q", text)
		}
	}
}

// 只有头和载荷两段的 JWT（没有签名段）也拦
func TestTwoPartJWT(t *testing.T) {
	two := "ey" + "J" + strings.Repeat("a", 12) + "." + "ey" + "J" + strings.Repeat("b", 12)
	if !blocked("token is " + two) {
		t.Error("两段 JWT 要拦")
	}
	if blocked("eyJhbGciOi.short") {
		t.Error("太短的不拦")
	}
}

// 本机 Magpie 网关的固定占位令牌（公开约定的值）不拦，其他令牌照拦
func TestMagpieLocalTokenAllowed(t *testing.T) {
	hdr := "Authori" + "zation: Bear" + "er "
	if blocked(hdr+"magpie-local") || blocked(`"`+hdr+`magpie-local"`) {
		t.Error("magpie-local 是公开约定的占位令牌，不拦")
	}
	if !blocked(hdr+"magpie-local-extra-x") || !blocked(hdr+"magpie-remote") || !blocked(hdr+strings.Repeat("a1", 8)) {
		t.Error("只放这一个值，别的令牌照拦")
	}
}

func TestStampKnownOnly(t *testing.T) {
	h := strings.Repeat("a", 40)
	text := "tree " + h + "\nauthor a <x@example.com> " + fakeGroup + " +0800\ncommitter a <x@example.com> 1700000000 +0800\n\n说明\n"
	var hit bool
	for _, hh := range Scan(commitLines(h, text), testKnown()) {
		if hh.Value == fakeGroup && strings.Contains(hh.Where, "时间戳") {
			hit = true
		}
	}
	if !hit {
		t.Error("时间戳那串数字删掉之前先对认识的号码比一次")
	}
	// 普通时间戳不出任何提醒
	for _, hh := range Scan(commitLines(h, "author a <x@example.com> 1700000000 +0800\n\n"), testKnown()) {
		if strings.Contains(hh.Where, "时间戳") {
			t.Errorf("普通时间戳不该有命中：%v", hh)
		}
	}
}

func TestHasOtherEncoding(t *testing.T) {
	for text, want := range map[string]bool{
		"tree x\nencoding gbk\n\n说明":   true,
		"tree x\nencoding UTF-8\n\n说明": false,
		"tree x\n\nencoding gbk":       false, // 说明里的不算
		"tree x\n\n说明":                 false,
	} {
		if hasOtherEncoding(text) != want {
			t.Errorf("%q 应该是 %v", text, want)
		}
	}
}
