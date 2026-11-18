package private

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// 号码、名字、邮箱全是编的（搬自 scripts/scan-private.test.ts）。
const (
	fakeReal  = "8642097531"
	fakeGroup = "97531864"
	fakeOld   = "54321"
)

var (
	fakeKey   = "sk" + "-" + "abcdefghijklmnopqrstuv" // 拼出来：源码里直接写会被提交前的扫描自己拦下
	ctx       = "2468" + "1357"
	ctx9      = ctx + "9"
	fakePhone = "139" + "12345678"
	fakeMail  = "someone.fake" + "@" + "mail.invalid.zz"
)

func testKnown() *Known {
	return NewKnown([]string{fakeReal, fakeGroup, fakeOld}, []string{"编的昵称甲", "Zorvex", "Ab_c"}, []string{fakeMail, "σίσυφος-fake", "οσοσ", "οςοσ", "σσσσ", "ςςςς", "οσ-ο"})
}

func kinds(text string) []string {
	var out []string
	for _, h := range Scan([]Line{{Where: "a.md:1", Text: text, File: "a.md"}}, testKnown()) {
		k := "提醒"
		if h.Block {
			k = "拦"
		}
		out = append(out, k+":"+h.Value)
	}
	return out
}

func blocked(text string) bool {
	for _, k := range kinds(text) {
		if strings.HasPrefix(k, "拦:") {
			return true
		}
	}
	return false
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestKnownNumbersBlockAnywhere(t *testing.T) {
	for _, text := range []string{
		"号是 " + fakeReal, "qq_group_" + fakeGroup, "onebot_" + fakeReal + ".json", `"uid":"` + fakeReal + `"`,
		"qq:private:" + fakeReal + ":" + fakeGroup, "https://example.invalid/u/" + fakeReal, "bug " + fakeReal,
		"max-age=" + fakeGroup, "v" + fakeReal, "老号 " + fakeOld + " 在群里", "#" + fakeGroup,
	} {
		if !blocked(text) {
			t.Errorf("认识的号码应该拦：%s", text)
		}
	}
	if blocked("1" + fakeReal + "9") {
		t.Error("认识的号码不算在更长的数字里")
	}
}

func TestUnknownNumbers(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"Firefox bug 1882408 修了", nil}, {"Chromium bug 343503163", nil}, {"max-age=31536000", nil}, {"00000000-0000-0000", nil},
		{"https://x.invalid/status/2120201406820884794", nil}, {"日期 20261111", nil}, {"时间戳 1789000000", nil}, {"版本 1.23456789.0", nil},
		{"陌生数字 13572468", []string{"提醒:13572468"}}, {"另一个 2468013579", []string{"提醒:2468013579"}},
	}
	for _, c := range cases {
		eq(t, kinds(c.text), c.want)
	}
}

func TestNumberContext(t *testing.T) {
	for _, text := range []string{"qq: " + ctx, "群号 " + ctx, "uin=" + ctx9, `user_id "` + ctx + `"`, "好友 " + ctx9} {
		if !blocked(text) {
			t.Errorf("号码上下文里的陌生数字应该拦：%s", text)
		}
	}
}

func TestMobile(t *testing.T) {
	eq(t, kinds("电话 "+fakePhone), []string{"拦:" + fakePhone})
	eq(t, kinds("https://x.invalid/"+fakePhone), nil)
}

func TestNamesKeysEmails(t *testing.T) {
	eq(t, kinds("昨天编的昵称甲说"), []string{"拦:编的昵称甲"})
	eq(t, kinds("Zorvex said"), []string{"拦:Zorvex"})
	eq(t, kinds("AZorvexB"), nil)
	found := false
	for _, k := range kinds("key = " + fakeKey) {
		if strings.HasPrefix(k, "拦:sk-") {
			found = true
		}
	}
	if !found {
		t.Error("sk- 密钥应该拦")
	}
	eq(t, kinds("desktop-windows-25-cpu-5-mb-s-sustained"), nil)

	for _, text := range []string{strings.ToUpper(fakeMail), "<" + fakeMail + ">", "mailto:" + fakeMail, strings.Replace(fakeMail, "@", "+work@", 1), strings.Replace(fakeMail, "@", "%40", 1)} {
		if !blocked(text) {
			t.Errorf("名单里的邮箱各种写法都应该拦：%s", text)
		}
	}
	eq(t, kinds("联系 stranger@corp-made-up.io"), []string{"提醒:stranger@corp-made-up.io"})
	eq(t, kinds("12345+someone@users.noreply.github.com、a@example.com、b@x.invalid、noreply@anthropic.com"), nil)
}

func TestLockFiles(t *testing.T) {
	k := testKnown()
	if h := Scan([]Line{{Where: "bun.lock:3", Text: fakeReal, File: "bun.lock"}}, k); len(h) != 0 {
		t.Error("锁文件不扫")
	}
	if h := Scan([]Line{{Where: "x/unlock.md:3", Text: fakeReal, File: "x/unlock.md"}}, k); len(h) != 1 {
		t.Error("别的文件名里带 lock 照扫")
	}
}

func TestCollect(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/a.md b/a.md", "index 1..2 100644", "--- a/a.md", "+++ b/a.md", "@@ -0,0 +1,2 @@",
		"++ b/x " + fakeReal, "+普通的一行",
		"diff --git a/qq_" + fakeGroup + ".json b/qq_" + fakeGroup + ".json", "new file mode 100644", "--- /dev/null", "+++ b/qq_" + fakeGroup + ".json", "@@ -0,0 +1 @@", "+{}",
		"diff --git a/old.md b/new.md", "similarity index 90%", "rename from old.md", "rename to new.md",
	}, "\n")
	lines := Collect(diff, func(f string, n int) string { return fmt.Sprintf("%s:%d", f, n) })
	var okNum, okLine, okName, okRename bool
	for _, l := range lines {
		if strings.Contains(l.Text, fakeReal) && l.Where == "a.md:1" && l.File == "a.md" {
			okNum = true
		}
		if l.Text == "普通的一行" && l.Where == "a.md:2" {
			okLine = true
		}
		if l.Text == "new.md" && strings.Contains(l.Where, "文件名") {
			okRename = true
		}
	}
	for _, h := range Scan(lines, testKnown()) {
		if h.Value == fakeGroup && strings.Contains(h.Where, "文件名") {
			okName = true
		}
	}
	if !okNum || !okLine || !okName || !okRename {
		t.Errorf("读 diff：正文 ++ 行 %v、行号 %v、文件名 %v、改名 %v", okNum, okLine, okName, okRename)
	}
	w := MaskWhere("qq_"+fakeGroup+".json:1 编的昵称甲", testKnown())
	if strings.Contains(w, fakeGroup) || strings.Contains(w, "编的昵称甲") {
		t.Errorf("位置里的号码和名字要打码：%s", w)
	}
}

func TestAllowKeyScope(t *testing.T) {
	k := testKnown()
	one := func(text, file string) Hit { return Scan([]Line{{Where: file + ":1", Text: text, File: file}}, k)[0] }
	a, b := one(fakeReal, "a.md"), one(fakeReal, "b.md")
	if AllowKey(a) == AllowKey(b) {
		t.Error("认识的号码只对那个文件放行")
	}
	w1, w2 := one("陌生数字 13572468", "a.md"), one("陌生数字 13572468", "b.md")
	if AllowKey(w1) != AllowKey(w2) {
		t.Error("其余按值登记")
	}
	later := NewKnown([]string{fakeReal, fakeGroup, fakeOld, "13572468"}, []string{"编的昵称甲", "Zorvex"}, []string{fakeMail})
	now := Scan([]Line{{Where: "a.md:1", Text: "陌生数字 13572468", File: "a.md"}}, later)[0]
	if now.Kind != "号码" || AllowKey(now) == AllowKey(w1) {
		t.Error("先按值登了不认识的数字，后来它进了名单：按值的登记对“号码”类不生效")
	}
}

// 对拍：parity.json 是旧版 scan-private.ts 在同一份编造名单上对这批字符串的结论（测试里一条一条比）。
// 文件里的字符串、命中值都存成 base64：直接存明文，仓库自己的隐私扫描会把这些编造的手机号、密钥样式拦下。
// 前缀 × 数字 × 后缀 的组合按嵌套顺序存结果，misc 是手写的一批（密钥、邮箱、名字、锁文件名）。
// 覆盖三处原来用前后查找的地方：数字段的边界（前面 数字 . - / # 字母，后面 数字 或 .数字）、sk- 的词头、/i 只认 ASCII（开尔文符号、长 s）。
func unb64(t *testing.T, s string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func unb64All(t *testing.T, l []string) []string {
	var out []string
	for _, s := range l {
		out = append(out, unb64(t, s))
	}
	return out
}

func encHits(hits []Hit) string {
	var parts []string
	for _, h := range hits {
		b := "0"
		if h.Block {
			b = "1"
		}
		parts = append(parts, h.Kind+"|"+base64.StdEncoding.EncodeToString([]byte(h.Value))+"|"+b)
	}
	return strings.Join(parts, ";")
}

func TestParityWithOldScan(t *testing.T) {
	raw, err := os.ReadFile("testdata/parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Prefixes, Digits, Suffixes, Combos []string
		Misc                               [][3]string
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	prefixes, digits, suffixes := unb64All(t, f.Prefixes), unb64All(t, f.Digits), unb64All(t, f.Suffixes)
	if len(f.Combos) != len(prefixes)*len(digits)*len(suffixes) || len(f.Misc) < 50 {
		t.Fatalf("对拍文件不全：组合 %d（应有 %d），手写 %d", len(f.Combos), len(prefixes)*len(digits)*len(suffixes), len(f.Misc))
	}
	k := testKnown()
	bad, withHits, n := 0, 0, 0
	check := func(text, file, want string) {
		got := encHits(Scan([]Line{{Where: "w", Text: text, File: file}}, k))
		if want != "" {
			withHits++
		}
		if got != want {
			bad++
			if bad <= 10 {
				t.Errorf("新旧结论不一致：%q（文件 %q）\n  旧 %s\n  新 %s", text, file, want, got)
			}
		}
	}
	for _, p := range prefixes {
		for _, d := range digits {
			for _, s := range suffixes {
				check(p+d+s, "a.md", f.Combos[n])
				n++
			}
		}
	}
	for _, m := range f.Misc {
		check(unb64(t, m[0]), unb64(t, m[1]), m[2])
	}
	if bad > 0 {
		t.Errorf("共 %d 条不一致", bad)
	}
	if withHits < 1500 {
		t.Errorf("对拍样本里有命中的只有 %d 条，样本太弱", withHits)
	}
}

// 三个结尾锚定的判断手写过，旧正则留在这里当对照：随机串（专挑关键词、空白、标点、引号、数字）逐个比。
var (
	oldNotNumberBefore = regexp.MustCompile(`(?:bug|issue|issues|crbug|pr|cve-[0-9]{4}-|max-age=|port|0x|v)[` + jsSpace + `]*[#:=]?[` + jsSpace + `]*$`)
	oldNumberContext   = regexp.MustCompile("(?:qq|uin|user_?id|group_?id|gid|uid|群|号|账号|好友)[" + jsSpace + "]*[:：=＝]?[" + jsSpace + "]*[\"'`]?[" + jsSpace + "]*$")
	oldURLBefore       = regexp.MustCompile(`https?://[^` + jsSpace + `]*$`)
)

func TestHandWrittenTailMatchersAgreeWithRegex(t *testing.T) {
	pieces := []string{"qq", "uin", "user", "_", "id", "group", "gid", "uid", "群", "号", "账", "好友", "bug", "issue", "issues", "crbug", "pr", "cve-", "2026", "-", "max-age=", "port", "0x", "v", "x",
		" ", "  ", "	", "　", " ", ":", "：", "=", "＝", "#", "\"", "'", "`", "http://", "https://", "a", "/", "5", "é"}
	rnd := uint64(88172645463325252)
	next := func() int {
		rnd ^= rnd << 13
		rnd ^= rnd >> 7
		rnd ^= rnd << 17
		return int(rnd >> 33)
	}
	hit := [3]int{}
	for i := 0; i < 300000; i++ {
		var b strings.Builder
		for j, n := 0, next()%7; j < n; j++ {
			b.WriteString(pieces[next()%len(pieces)])
		}
		s := b.String()
		if got, want := notNumberBefore(s), oldNotNumberBefore.MatchString(s); got != want {
			t.Fatalf("notNumberBefore(%q)=%v，正则是 %v", s, got, want)
		} else if want {
			hit[0]++
		}
		if got, want := numberContext(s), oldNumberContext.MatchString(s); got != want {
			t.Fatalf("numberContext(%q)=%v，正则是 %v", s, got, want)
		} else if want {
			hit[1]++
		}
		if got, want := urlBefore(s), oldURLBefore.MatchString(s); got != want {
			t.Fatalf("urlBefore(%q)=%v，正则是 %v", s, got, want)
		} else if want {
			hit[2]++
		}
	}
	if hit[0] < 2000 || hit[1] < 2000 || hit[2] < 2000 {
		t.Errorf("随机串里命中的太少，对照没意义：%v", hit)
	}
}

// 一行几十万字符（压缩过的 JSON）也要在秒级扫完；换成手写判断之前用正则要几分钟。
func TestLongLineIsFast(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40000; i++ {
		b.WriteString("x 13572468 yy ")
	}
	start := time.Now()
	hits := Scan([]Line{{Where: "w", Text: b.String(), File: "a.json"}}, testKnown())
	if len(hits) != 40000 {
		t.Errorf("命中 %d", len(hits))
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("长行扫得太慢：%v", d)
	}
}

// 边界判断逐条（数字段前后、sk- 词头、ASCII 大小写），不依赖对拍文件。
func TestBoundaries(t *testing.T) {
	// 数字段前面：数字 . - / # 字母 都不算号码的开头，别的字符算
	for _, p := range []string{".", "-", "/", "#", "a", "Z"} {
		eq(t, kinds(p+"13572468"), nil)
	}
	for _, p := range []string{"", " ", "_", ":", "：", "一", "é"} {
		eq(t, kinds(p+"13572468"), []string{"提醒:13572468"})
	}
	// 后面：.数字 不算，.字母、句号、字母算
	eq(t, kinds("13572468.5"), nil)
	eq(t, kinds("13572468.x"), []string{"提醒:13572468"})
	eq(t, kinds("13572468."), []string{"提醒:13572468"})
	eq(t, kinds("13572468a"), []string{"提醒:13572468"})
	// 长度：7 位、12 位不提醒；8、11 位提醒；11 位 1[3-9] 开头是手机号
	eq(t, kinds("1357246"), nil)
	eq(t, kinds("135724681357"), nil)
	mob := "1357" + "2468135" // 拼出来：直接写会被仓库自己的隐私扫描当手机号拦下
	eq(t, kinds(mob), []string{"拦:" + mob})
	eq(t, kinds("23572468135"), []string{"提醒:23572468135"})
	eq(t, kinds("12572468135"), []string{"提醒:12572468135"})
	// 0 开头不当号码
	eq(t, kinds("03572468"), nil)
	// 名单号码：5～12 位的整段才算，13 位里不算
	eq(t, kinds("x"+fakeOld+"y"), []string{"拦:" + fakeOld})
	eq(t, kinds("1"+fakeOld), nil)
	// sk-：前面是字母数字不算词头；先全局匹配再过滤会吃掉后面真正的那个
	key := "abcdefghijklmnopqrstuvwxyz0123"
	if got := skMatches("Xsk-sk-" + key); len(got) != 1 || got[0] != "sk-"+key {
		t.Errorf("sk- 词头：%q", got)
	}
	if got := skMatches("asksk-" + key); len(got) != 0 {
		t.Errorf("sk- 前面是字母不算：%q", got)
	}
	if got := skMatches("-sk-" + key); len(got) != 1 {
		t.Errorf("sk- 前面是减号算词头：%q", got)
	}
	if got := skMatches("sk-" + key[:15]); len(got) != 0 {
		t.Errorf("sk- 后面要 16 个以上：%q", got)
	}
	// /i 只认 ASCII：长 s 写成的 issue 不是 issue，开尔文符号不是 k
	eq(t, kinds("iſsue 13572468"), []string{"提醒:13572468"})
	eq(t, kinds("ISSUE 13572468"), nil)
	eq(t, kinds("Bug#13572468"), nil)
	eq(t, kinds("QQ "+ctx), []string{"拦:" + ctx})
	if len(kinds("Kpassword="+key)) != 0 {
		// 旧版 /i 下开尔文符号不冒充 k；这里没有 k，只验证不 panic 且整段不被当赋值
		t.Logf("kinds=%v", kinds("Kpassword="+key))
	}
}

// 词尾 Σ：JS 的 toLowerCase 变 ς，不是 σ。名单里的 Σίσυφος-fake 归一后是 σίσυφος-fake（词首 Σ 变 σ），
// 文本 ΣΊΣΥΦΟΣ-FAKE 的最后一个 Σ 在词尾（后面是 -），要变 ς 才对得上；旧版拦，新版补这条以前放过。
func TestFinalSigma(t *testing.T) {
	k := NewKnown(nil, nil, []string{NormMail("Σίσυφος-fake")})
	if got := NormMail("Σίσυφος-fake"); got != "σίσυφος-fake" {
		t.Fatalf("名单归一：%q", got)
	}
	for _, text := range []string{"ΣΊΣΥΦΟΣ-FAKE", "ΑΣ ΣΊΣΥΦΟΣ-FAKE"} {
		if h := Scan([]Line{{Where: "w", Text: text, File: "a.md"}}, k); len(h) != 1 || !h[0].Block {
			t.Errorf("%q 应该拦：%v", text, h)
		}
	}
	for in, want := range map[string]string{"ΟΣ": "ος", "ΣΟ": "σο", "Σ": "σ", "ΟΣΟ": "οσο", "Ο.Σ": "ο.ς", "ΟΣ1": "ος1", "ΑΣ ΑΣ": "ας ας", "'Σ": "'σ"} {
		if got := jsLower(in); got != want {
			t.Errorf("jsLower(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMaskLikeOld(t *testing.T) {
	cases := []struct {
		s    string
		keep int
		want string
	}{
		{"1234567", 2, "12＊＊＊＊＊"}, {"ab", 2, "ab＊"}, {"a", 1, "a＊"}, {"编的昵称甲", 1, "编＊＊＊＊"},
		{"😀abc", 1, "�＊＊＊"}, {"😀abc", 2, "😀＊＊"},
	}
	for _, c := range cases {
		if got := mask(c.s, c.keep); got != c.want {
			t.Errorf("mask(%q,%d)=%q want %q", c.s, c.keep, got, c.want)
		}
	}
}
