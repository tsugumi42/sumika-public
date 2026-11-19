package private

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"sumika-native/internal/devtools/gitrun"
)

const usageLine = "用法：--staged | --range A..B | --range 提交 --not 已有的提交… | --allow 编号… [--allow-missing]"

// Run 是 sumika-dev check private 的入口，root 是仓库根（程序所在的那棵树）。
//
//	--staged              暂存区（提交前）
//	--range 提交范围… [--line 文字]… [--tag 标签对象]…   这些提交（交给 rev-list 的参数，如 A..B 或 “X --not Y Z”）的新增行、提交说明、作者和提交者（推送前）
//	--allow 编号 [编号…]   把上次报出来、核对过不是真实的那几条登记成误报（只存哈希）
//	--tree 目录           扫一个目录里的全部文本文件（导出公开版的自检用，退出码 1 有要拦的、2 名单读不了）
//	--allow-missing       名单读不到时也照常跑（默认拒绝：没有名单就认不出真号码）
//
// 名单和误报登记都在主工作区的 data/（worktree 里没有 data/），按 git 的 common dir 找主工作区。
func Run(args []string, root string, stdout, stderr io.Writer) int {
	// 有意继承钩子环境：--staged 要读钩子给的 GIT_INDEX_FILE。GIT_NO_REPLACE_OBJECTS：refs/replace/ 能让 git 把一个提交换成另一个的内容，扫到的就不是要推的
	env := append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1")
	gitOut := func(a ...string) (string, int) {
		out, errOut, code, err := gitrun.RunStderr(root, env, nil, a...)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return "", 2
		}
		if code != 0 {
			fmt.Fprintln(stderr, string(errOut))
			return "", 2
		}
		return string(out), 0
	}
	// SUMIKA_SCAN_DATA、SUMIKA_SCAN_MAIBOT 能把名单改指到别处，只在测试构建（go test、-tags scantest）里认；生产构建里设了就拒绝（红），
	// 不是悄悄忽略：悄悄忽略的话，有人以为改过了名单。Windows 的环境变量名不分大小写，os.Getenv 也一样
	scanData, scanMaibot := os.Getenv("SUMIKA_SCAN_DATA"), os.Getenv("SUMIKA_SCAN_MAIBOT")
	if (scanData != "" || scanMaibot != "") && !testHooksAllowed() {
		fmt.Fprintln(stderr, "隐私扫描：环境里设了 SUMIKA_SCAN_DATA 或 SUMIKA_SCAN_MAIBOT（能把名单改指到别处，只在测试构建里生效），拒绝扫描。去掉它们再试")
		return 2
	}
	common := ""
	if out, _, code, err := gitrun.RunStderr(root, env, nil, "rev-parse", "--git-common-dir"); err == nil && code == 0 {
		common = jsTrim(string(out))
	}
	var main string
	switch {
	case common == "":
		main = filepath.Dir(root)
	case filepath.IsAbs(common):
		main = filepath.Dir(filepath.Clean(common))
	default:
		main = filepath.Dir(filepath.Join(root, common))
	}
	if d := scanData; d != "" {
		fmt.Fprintf(stderr, "警告：名单目录被 SUMIKA_SCAN_DATA 改到了 %s，只该在测试里用\n", d)
		main = d // 只给测试用：把“主工作区”（名单和误报登记所在的 data/ 的上一级）指到编造的临时目录，不影响程序自己的源码根和重编
	}
	allowPath := filepath.Join(main, "data/dev/private-scan-allow.json")
	lastPath := filepath.Join(main, "data/dev/private-scan-last.json")

	// 名单和误报登记在 data/ 里，不该进 git：被跟踪的话（别人能改、也会被推出去）拒绝读，红
	listFiles := []string{"data/dev/private-scan-allow.json", "data/go/chat/qq.db", "data/dev/human-replies/groups.json", "data/config/sumika.yml", "data/dev/private-strings.json"}
	if out, code, err := gitrun.Run(main, env, nil, append([]string{"ls-files", "--"}, listFiles...)...); err == nil && code == 0 && strings.TrimSpace(string(out)) != "" {
		fmt.Fprintln(stderr, "隐私扫描：名单或误报登记文件被 git 跟踪了，拒绝读（data/ 不该进 git；先 git rm --cached）："+strings.Join(strings.Fields(string(out)), "、"))
		return 2
	}

	allowed := newSet()
	if b, err := os.ReadFile(allowPath); err == nil {
		var arr []string
		if json.Unmarshal(b, &arr) != nil {
			fmt.Fprintln(stderr, "隐私扫描：误报登记文件读不了："+allowPath)
			return 1
		}
		for _, s := range arr {
			allowed.add(s)
		}
	}
	allowMissing := false
	var extraText, tagObjs []string // --line 文本（分支名、tag 名这类也要扫的文字）、--tag 标签对象
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--allow-missing":
			allowMissing = true
		case args[i] == "--line" && i+1 < len(args):
			extraText = append(extraText, args[i+1])
			i++
		case args[i] == "--tag" && i+1 < len(args):
			tagObjs = append(tagObjs, args[i+1])
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	args = rest

	if len(args) > 0 && args[0] == "--allow" {
		last := map[string]string{}
		if b, err := os.ReadFile(lastPath); err == nil {
			_ = json.Unmarshal(b, &last)
		}
		for _, id := range args[1:] {
			if last[id] == "" {
				fmt.Fprintf(stderr, "没有编号 %s（只认上一次扫描报出来的编号）\n", id)
				return 1
			}
			allowed.add(last[id])
		}
		sorted := append([]string{}, allowed.ord...)
		sort.Strings(sorted)
		b, _ := json.MarshalIndent(sorted, "", " ")
		if err := os.WriteFile(allowPath, b, 0o644); err != nil {
			fmt.Fprintln(stderr, "隐私扫描：写不了误报登记："+err.Error())
			return 1
		}
		fmt.Fprintf(stdout, "登记了 %d 条误报，一共 %d 条（%s）\n", len(args)-1, allowed.size(), allowPath)
		return 0
	}

	tree := len(args) >= 2 && args[0] == "--tree"
	k, notes, err := LoadKnown(Sources{
		QQDB:        filepath.Join(main, "data/go/chat/qq.db"),
		MaibotDB:    scanMaibot, // 只给测试换成编的库；它只影响名字和号码的一部分来源，聊天库缺了照样拒绝
		GroupsJSON:  filepath.Join(main, "data/dev/human-replies/groups.json"),
		ConfigYML:   filepath.Join(main, "data/config/sumika.yml"),
		PrivateJSON: filepath.Join(main, "data/dev/private-strings.json"),
	})
	if err != nil {
		msg := "读不了"
		if se, ok := err.(*SourceError); ok {
			msg = se.Msg
		}
		if tree {
			fmt.Fprintf(stderr, "隐私名单读不了（%s），不导出\n", msg)
			return 2
		}
		fmt.Fprintf(stderr, "隐私扫描：名单%s，没扫。修好名单再提交；确实要跳过名单就加 --allow-missing\n", msg)
		return 2
	}
	if tree {
		if k.numbers.size() == 0 {
			fmt.Fprintln(stderr, "隐私名单里没有本机号码，认不出真号码，不导出")
			return 2
		}
		return runTree(args[1], k, allowed, stdout, stderr)
	}
	// 推送前（--range）：私人字符串名单和群名表缺失或为空就失败关闭，不分 --allow-missing；提交时（--staged）照旧只在输出里提示
	if len(args) > 0 && args[0] == "--range" {
		for _, f := range [][2]string{{"私人字符串名单", filepath.Join(main, "data/dev/private-strings.json")}, {"群名表", filepath.Join(main, "data/dev/human-replies/groups.json")}} {
			if !nonEmptyJSON(f[1]) {
				fmt.Fprintf(stderr, "隐私扫描：%s缺失或为空（%s），推送前不放行。数据目录：%s\\data\n", f[0], filepath.Base(f[1]), main)
				return 2
			}
		}
	}
	if !allowMissing && (contains(notes, "缺 Go 聊天库") || k.numbers.size() == 0) {
		n := strings.Join(notes, "、")
		if n == "" {
			n = "认识的号码 0 个"
		}
		fmt.Fprintf(stderr, "隐私扫描：没有本机号码名单（%s），认不出真号码，没扫。数据目录：%s\\data；确实要跳过名单就加 --allow-missing\n", n, main)
		return 2
	}

	// 每扫一批就把命中收下、行丢掉：全历史的 diff 有三百多 MB，不整段留在内存里
	var hits []Hit
	total := 0
	var dataPaths []string // 新增或修改的路径以 data/ 开头的（删除不算）
	addLines := func(ls []Line) {
		total += len(ls)
		hits = append(hits, Scan(ls, k)...)
		for _, l := range ls {
			if strings.HasSuffix(l.Where, " 文件名") && strings.HasPrefix(l.File, "data/") && !l.Del {
				dataPaths = append(dataPaths, l.Where)
			}
		}
	}
	switch {
	case len(args) > 0 && args[0] == "--staged":
		d, code := gitOut(append([]string{"diff", "--cached"}, diffFlags...)...) // diffFlags 见 rangescan.go：--text、固定 a/ b/ 前缀、不跑 textconv 和外部 diff
		if code != 0 {
			return code
		}
		ls, err := Collect(d, func(f string, n int) string { return f + ":" + strconv.Itoa(n) })
		if err != nil {
			fmt.Fprintln(stderr, "隐私扫描：读暂存区的 diff 失败，没扫完："+err.Error())
			return 2
		}
		addLines(ls)
	case len(args) > 0 && args[0] == "--range":
		// 后面的参数整个交给 rev-list：一段 A..B，或 “提交 --not 远端已有的提交…”（推送前给“这次会新发到远端的全部提交”）；
		// 末尾的 -- 让它们只能当提交名，不会被当成路径
		rng := []string{"undefined"}
		if len(args) > 1 {
			rng = args[1:]
		}
		revs, code := gitOut(append(append([]string{"rev-list", "--parents"}, rng...), "--")...)
		if code != 0 {
			return code
		}
		for _, t := range extraText {
			addLines([]Line{{Where: "分支名或标签名", Text: t}})
		}
		for _, t := range tagObjs {
			ls, err := tagLines(root, env, t)
			if err != nil {
				fmt.Fprintln(stderr, "隐私扫描：读不了标签对象 "+shortHash(t)+"："+err.Error())
				return 2
			}
			addLines(ls)
		}
		if err := scanRange(root, env, revs, addLines); err != nil {
			fmt.Fprintln(stderr, "隐私扫描：取提交内容失败，没扫完："+err.Error())
			return 2
		}
	default:
		fmt.Fprintln(stderr, usageLine)
		return 2
	}

	var real []Hit
	for _, h := range hits {
		if !allowed.has(AllowKey(h)) {
			real = append(real, h)
		}
	}
	type group struct {
		key  string
		hits []Hit
	}
	var groups []*group
	idx := map[string]*group{}
	for _, h := range real {
		key := AllowKey(h)
		g := idx[key]
		if g == nil {
			g = &group{key: key}
			idx[key] = g
			groups = append(groups, g)
		}
		g.hits = append(g.hits, h)
	}
	note := ""
	if len(notes) > 0 {
		note = "（" + strings.Join(notes, "、") + "）"
	}
	fmt.Fprintf(stdout, "隐私扫描：%d 行；认识的号码 %d 个、名字 %d 个、私人字符串 %d 条%s；登记过的误报 %d 条\n",
		total, k.numbers.size(), k.names.size(), k.secrets.size(), note, allowed.size())
	ids := map[string]string{}
	var idOrder []string
	blocking := 0
	for i, g := range groups {
		id := strconv.Itoa(i + 1)
		ids[id] = g.key
		idOrder = append(idOrder, id)
		h := g.hits[0]
		kind := "提醒"
		if h.Block {
			kind = "拦"
			blocking++
		}
		keep := 1
		if strings.Contains(h.Kind, "号码") || strings.Contains(h.Kind, "数字") || strings.Contains(h.Kind, "手机号") {
			keep = 2
		}
		fmt.Fprintf(stdout, "  [%s] %s %s %s（%d 个字符）  %d 处，例：%s\n", id, kind, h.Kind, mask(h.Value, keep), jsLen(h.Value), len(g.hits), MaskWhere(h.Where, k))
	}
	var lb bytes.Buffer
	lb.WriteByte('{')
	for i, id := range idOrder {
		if i > 0 {
			lb.WriteByte(',')
		}
		kb, _ := json.Marshal(id)
		vb, _ := json.Marshal(ids[id])
		lb.Write(kb)
		lb.WriteByte(':')
		lb.Write(vb)
	}
	lb.WriteByte('}')
	if err := os.WriteFile(lastPath, lb.Bytes(), 0o644); err != nil {
		fmt.Fprintln(stderr, "隐私扫描：写不了扫描编号："+err.Error())
		return 1
	}
	if len(dataPaths) > 0 {
		blocking++
		fmt.Fprintf(stdout, "有 %d 处新增或修改的路径在 data/ 下（例：%s）：data/ 里是名单、聊天库、配置这类本机数据，不该进 git；git rm --cached 拿掉，别提交\n", len(dataPaths), MaskWhere(dataPaths[0], k))
	}
	if blocking > 0 {
		fmt.Fprintf(stdout, "有 %d 个要拦的：本机认识的号码、像号码的数字、私人字符串、真实名字或像密钥的串。是真实的就改掉再提交；核对过不是就 sumika-dev check private --allow 编号（认识的号码只对这个文件放行）\n", blocking)
		return 1
	}
	if len(groups) > 0 {
		fmt.Fprintf(stdout, "只有提醒：%d 个本机不认识的长数字或邮箱，看一眼不是号码就行（不想再看到就 --allow 编号）\n", len(groups))
	} else {
		fmt.Fprintln(stdout, "没发现")
	}
	return 0
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// maskValue 是导出自检里的打码：前 2 个码点原样，后面每个码点一个“＊”。
func maskValue(s string) string {
	r := []rune(s)
	keep := 2
	if len(r) < keep {
		keep = len(r)
	}
	n := len(r) - keep
	if n < 1 {
		n = 1
	}
	return string(r[:keep]) + strings.Repeat("＊", n)
}

// runTree 扫目录里所有文本文件（前 8192 字节里有 NUL、又不像 UTF-16 的当二进制跳过），输出的行和导出器自检里的隐私一段一致。
func runTree(dir string, k *Known, allowed *oset, stdout, stderr io.Writer) int {
	var rels []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			rels = append(rels, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(stderr, "隐私扫描：读不了目录："+err.Error())
		return 2
	}
	var hits []Hit
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			fmt.Fprintln(stderr, "隐私扫描：读不了文件："+rel)
			return 2
		}
		head := b
		if len(head) > 8192 {
			head = head[:8192]
		}
		if bytes.IndexByte(head, 0) >= 0 && !looksUTF16(head) {
			continue // 真正的二进制；UTF-16 文本（有 BOM 或 NUL 规整地隔着）照扫，Scan 会按去 NUL 和解码的副本比
		}
		var lines []Line
		for i, t := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
			lines = append(lines, Line{Where: rel + ":" + strconv.Itoa(i+1), Text: t, File: rel})
		}
		for _, h := range Scan(lines, k) {
			if !allowed.has(AllowKey(h)) {
				hits = append(hits, h)
			}
		}
	}
	var blocking, reminders []Hit
	for _, h := range hits {
		if h.Block {
			blocking = append(blocking, h)
		} else {
			reminders = append(reminders, h)
		}
	}
	var order []string
	groups := map[string][]Hit{}
	for _, h := range blocking {
		key := AllowKey(h)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], h)
	}
	fmt.Fprintf(stdout, "  隐私扫描：拦 %d 组 / %d 处，提醒 %d 处\n", len(order), len(blocking), len(reminders))
	for _, key := range order {
		hs := groups[key]
		h := hs[0]
		fmt.Fprintf(stdout, "    拦 %s %s %s（%d 处）\n", h.Kind, maskValue(h.Value), MaskWhere(h.Where, k), len(hs))
	}
	for i, h := range reminders {
		if i >= 10 {
			break
		}
		fmt.Fprintf(stdout, "    提醒 %s %s %s\n", h.Kind, maskValue(h.Value), MaskWhere(h.Where, k))
	}
	if len(reminders) > 10 {
		fmt.Fprintf(stdout, "    …还有 %d 处提醒\n", len(reminders)-10)
	}
	if len(blocking) > 0 {
		return 1
	}
	return 0
}

// nonEmptyJSON：文件在、不是空白、也不是 []、{}、null。
func nonEmptyJSON(p string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	switch strings.TrimSpace(string(b)) {
	case "", "[]", "{}", "null":
		return false
	}
	return true
}
