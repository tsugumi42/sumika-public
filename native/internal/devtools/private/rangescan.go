package private

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"sumika-native/internal/devtools/gitrun"
)

// --range 扫一批提交：先 rev-list --parents 列出要扫的提交，再成批取（不是每个提交起几个 git 进程：全历史 6000 个提交时
// 逐个起进程要两三个小时）。提交对象整个用 cat-file --batch 取（按字节数读，头和正文整段当文本扫，NUL 不截断，
// author、committer 之外的头字段 x-note、mergetag、gpgsig 也扫），diff 每 batchSize 个提交一批用 git log 取，扫完一批就丢。
//
// 按父提交数分三类取 diff，都保证“多扫不少扫”：
//   - 0～1 个父提交：普通 diff；
//   - 2 个父提交（普通合并）：--remerge-diff，只看冲突解决时手改的内容；
//   - 3 个以上（章鱼合并）：git 对它的 --remerge-diff 只在 stderr 打一句 “Skipping remerge-diff for octopus merges.”、标准输出是空的、
//     退出码 0，等于什么都没扫；改用 -m，对每个父提交各给一份 diff。出现 Skipping 的提示一律按失败。
//
// 所有出 diff 的 git 调用都带 diffFlags：用户配置（diff.noprefix、diff.mnemonicPrefix、textconv、外部 diff 程序、log.showRoot、
// log.showSignature）改变不了要扫的内容；环境里设了 GIT_NO_REPLACE_OBJECTS（Run 设的），refs/replace 也换不掉提交。
const diffMarker = "@@COMMIT@@ " // 每个提交的 diff 前一行：diff 正文的行都以空格、+、-、\ 或 diff/index 这类词开头，不会和它混

var batchSize = 200 // 测试里改小，看批与批的边界

// diffFlags：文件名前缀固定成 a/ b/（Collect 靠它认文件名）；不跑 textconv 和外部 diff（它们能把内容换成别的，也会拿别人的文件去跑本机程序）；
// --text 让含 NUL 或标了 -diff 的文本文件也按文本看。
var diffFlags = []string{"-p", "-U0", "--no-color", "--text", "-M", "--no-textconv", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/"}

type rangeCommit struct {
	hash    string
	parents int
}

// parseRevList 读 rev-list --parents 的输出：每行 “提交 父提交…”。
func parseRevList(out string) []rangeCommit {
	var cs []rangeCommit
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) > 0 {
			cs = append(cs, rangeCommit{hash: f[0], parents: len(f) - 1})
		}
	}
	return cs
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// textLines 把一段文本按行变成要扫的行；空行也算一行（保持和旧版一样的行数口径）。
func textLines(where, text string) []Line {
	var ls []Line
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		ls = append(ls, Line{Where: where, Text: l})
	}
	return ls
}

var (
	reHexHash = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	reStamp   = regexp.MustCompile(`\s+[0-9]+\s+[+-][0-9]{4}\s*$`)
)

// commitLines 把一个提交对象的原文变成要扫的行：头字段逐行（author、committer 去掉末尾的时间戳和时区，位置写“作者和提交者”；
// tree、parent 的值是哈希就不扫，哈希里的数字串会误报；其余的头字段含续行写“提交头”），空行以后是说明。
func commitLines(hash, text string) []Line {
	short := shortHash(hash)
	head, body, hasBody := strings.Cut(text, "\n\n")
	var lines []Line
	skip := false
	where := short + " 提交头"
	for _, l := range strings.Split(head, "\n") {
		if !strings.HasPrefix(l, " ") { // 续行（gpgsig、mergetag 的后续各行）沿用上一个字段
			key, val, _ := strings.Cut(l, " ")
			skip = false
			where = short + " 提交头"
			switch key {
			case "author", "committer":
				where = short + " 作者和提交者"
				if st := reStamp.FindString(l); st != "" {
					// 末尾的时间戳删掉之前，先对本机认识的号码比一次（时间戳那串数字也能拿来藏号码）
					lines = append(lines, Line{Where: where + "时间戳", Text: strings.Fields(st)[0], KnownOnly: true})
				}
				l = reStamp.ReplaceAllString(l, "")
			case "tree", "parent":
				skip = reHexHash.MatchString(val)
			}
		}
		if !skip {
			lines = append(lines, Line{Where: where, Text: l})
		}
	}
	if hasBody {
		lines = append(lines, textLines(short+" 提交说明", body)...)
	}
	return lines
}

// commitObjectLines 用 cat-file --batch 一次取一批提交的完整对象。每个对象输出 “<哈希> <类型> <字节数>\n<内容>\n”，按字节数读；
// 缺对象、类型不是提交、数量对不上都返回错误（扫不全就不放行）。
func commitObjectLines(root string, env []string, hashes []string) ([]Line, error) {
	var reenc []string
	out, errOut, code, err := gitrun.RunStderr(root, env, []byte(strings.Join(hashes, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(errOut)))
	}
	var lines []Line
	n := 0
	for len(out) > 0 {
		nl := bytes.IndexByte(out, '\n')
		if nl < 0 {
			return nil, fmt.Errorf("cat-file 的输出读不懂")
		}
		f := strings.Fields(string(out[:nl]))
		if len(f) == 2 && f[1] == "missing" {
			return nil, fmt.Errorf("缺少提交对象 %s", shortHash(f[0]))
		}
		if len(f) != 3 || f[1] != "commit" {
			return nil, fmt.Errorf("cat-file 给了不是提交的对象：%s", shortHash(strings.Join(f, " ")))
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size < 0 || nl+1+size > len(out) {
			return nil, fmt.Errorf("cat-file 的字节数对不上")
		}
		text := string(out[nl+1 : nl+1+size])
		lines = append(lines, commitLines(f[0], text)...)
		if hasOtherEncoding(text) {
			reenc = append(reenc, f[0])
		}
		out = out[nl+1+size:]
		if len(out) > 0 && out[0] == '\n' {
			out = out[1:]
		}
		n++
	}
	if n != len(hashes) {
		return nil, fmt.Errorf("要取 %d 个提交对象，只拿到 %d 个", len(hashes), n)
	}
	for _, h := range reenc {
		msg, errOut, code, err := gitrun.RunStderr(root, env, nil, "log", "--no-walk", "--encoding=UTF-8", "--format=%B", h)
		if err != nil {
			return nil, err
		}
		if code != 0 {
			return nil, fmt.Errorf("%s", strings.TrimSpace(string(errOut)))
		}
		lines = append(lines, textLines(shortHash(h)+" 提交说明（转成 UTF-8）", string(msg))...)
	}
	return lines, nil
}

// hasOtherEncoding：提交头里有 “encoding xxx”，而且不是 UTF-8（说明是用别的编码存的，原始字节里的中文名字比不上）。
func hasOtherEncoding(text string) bool {
	head, _, _ := strings.Cut(text, "\n\n")
	for _, l := range strings.Split(head, "\n") {
		if v, ok := strings.CutPrefix(l, "encoding "); ok {
			v = strings.ToLower(strings.TrimSpace(v))
			return v != "utf-8" && v != "utf8"
		}
	}
	return false
}

// checkMarkers：这一批 git log 的输出里出现过的提交，和请求的提交对得上。多出来的一律错；少了的：普通提交和两个父提交的合并 git 都会出标记行，
// 少了就是没扫全，错；章鱼合并用 -m 时对每个父提交的 diff 都为空的提交 git 可能不出，允许少。
func checkMarkers(requested, got []string, allowMissing bool) error {
	want := map[string]bool{}
	for _, h := range requested {
		want[h] = false
	}
	for _, h := range got {
		seen, ok := want[h]
		if !ok {
			return fmt.Errorf("git log 给出了没请求的提交 %s", shortHash(h))
		}
		_ = seen
		want[h] = true
	}
	if !allowMissing {
		for _, h := range requested {
			if !want[h] {
				return fmt.Errorf("git log 没给出提交 %s 的内容，没扫全", shortHash(h))
			}
		}
	}
	return nil
}

// diffLines 取一批同类提交的 diff，kind：0 普通、1 两个父提交、2 章鱼合并。
func diffLines(root string, env []string, kind int, hashes []string) ([]Line, error) {
	args := append([]string{"log", "--no-walk=unsorted", "--stdin", "--root", "--no-show-signature", "--encoding=UTF-8", "--format=" + diffMarker + "%H"}, diffFlags...)
	switch kind {
	case 1:
		args = append(args, "--remerge-diff")
	case 2:
		args = append(args, "-m")
	}
	out, errOut, code, err := gitrun.RunStderr(root, env, []byte(strings.Join(hashes, "\n")+"\n"), args...)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(errOut)))
	}
	if bytes.Contains(errOut, []byte("Skipping")) {
		return nil, fmt.Errorf("git 跳过了一部分 diff：%s", strings.TrimSpace(string(errOut)))
	}
	var lines []Line
	var got []string
	for i, seg := range strings.Split(string(out), "\n"+diffMarker) {
		if i == 0 {
			if !strings.HasPrefix(seg, diffMarker) {
				continue // 第一个标记前没有东西
			}
			seg = seg[len(diffMarker):]
		}
		first, diff, _ := strings.Cut(seg, "\n")
		full := strings.TrimSpace(first)
		got = append(got, full)
		short := shortHash(full)
		ls, err := Collect(diff, func(f string, n int) string { return short + " " + f + ":" + strconv.Itoa(n) })
		if err != nil {
			return nil, fmt.Errorf("提交 %s：%w", short, err)
		}
		lines = append(lines, ls...)
	}
	if err := checkMarkers(hashes, got, kind == 2); err != nil {
		return nil, err
	}
	return lines, nil
}

// scanRange 扫 rev-list --parents 列出的全部提交，一批一批交给 add。失败返回原因。
func scanRange(root string, env []string, revListOut string, add func([]Line)) error {
	commits := parseRevList(revListOut)
	if len(commits) == 0 {
		return nil
	}
	all := make([]string, len(commits))
	groups := [3][]string{}
	for i, c := range commits {
		all[i] = c.hash
		kind := 0
		if c.parents == 2 {
			kind = 1
		} else if c.parents > 2 {
			kind = 2
		}
		groups[kind] = append(groups[kind], c.hash)
	}
	for start := 0; start < len(all); start += batchSize {
		end := start + batchSize
		if end > len(all) {
			end = len(all)
		}
		ls, err := commitObjectLines(root, env, all[start:end])
		if err != nil {
			return err
		}
		add(ls)
	}
	for kind, hs := range groups {
		for start := 0; start < len(hs); start += batchSize {
			end := start + batchSize
			if end > len(hs) {
				end = len(hs)
			}
			ls, err := diffLines(root, env, kind, hs[start:end])
			if err != nil {
				return err
			}
			add(ls)
		}
	}
	return nil
}

// maxTagDepth：标签指向标签最多一层层读到这里；读到头还有下一层就报错（读不完不放行）。
const maxTagDepth = 10

// tagLines 标签对象的文本（tagger 和标签说明）；标签指向标签就一层层往下读。
func tagLines(root string, env []string, obj string) ([]Line, error) {
	var lines []Line
	for depth := 0; depth <= maxTagDepth; depth++ {
		typ, errOut, code, err := gitrun.RunStderr(root, env, nil, "cat-file", "-t", obj)
		if err != nil {
			return nil, err
		}
		if code != 0 {
			return nil, fmt.Errorf("%s", strings.TrimSpace(string(errOut)))
		}
		if strings.TrimSpace(string(typ)) != "tag" {
			return lines, nil
		}
		txt, errOut, code, err := gitrun.RunStderr(root, env, nil, "cat-file", "-p", obj)
		if err != nil {
			return nil, err
		}
		if code != 0 {
			return nil, fmt.Errorf("%s", strings.TrimSpace(string(errOut)))
		}
		lines = append(lines, textLines(shortHash(obj)+" 标签对象", string(txt))...)
		next := ""
		for _, l := range strings.Split(string(txt), "\n") {
			if strings.HasPrefix(l, "object ") {
				next = strings.TrimSpace(l[len("object "):])
				break
			}
			if l == "" {
				break
			}
		}
		if next == "" {
			return lines, nil
		}
		obj = next
	}
	return nil, fmt.Errorf("标签嵌套超过 %d 层，没读完", maxTagDepth)
}
