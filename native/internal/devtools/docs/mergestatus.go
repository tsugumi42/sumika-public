package docs

// D14 文档里还写着“未合 main / 待合并 / 在分支 X 上”，提到的分支其实已经合进 main：只提醒，指到文件和行。
// 证据：LESSONS 证据存档“做完一步……”条，2026-10-28、11-02、11-03、11-06、11-12、11-19 都出现过；
// 2026-11-13 先做成只看规格状态头（照搬 scripts/check-spec-merged.ts），11-19 漏的是子系统参考的标题行
// （`docs/subsystems/tools.md` 的“feat/write-eol，未合 main”），复盘-11-19 第 4 条改成全部现行文档逐行查。
//
// 怎么算“已合”：只看 main 历史里合并提交的标题（PR 合并、git merge、合并队列的“合并 分支名”）点名的源分支。
// 不按“分支还在、是 main 的祖先”判断：刚从 main 开出来、还没提交的分支也是祖先，会误报；
// 也不把“分支不存在”当已合：CI 和新克隆里本来就没有那些分支。
// main 取本地 main 和每个远端的 main（origin 是公开仓库；private 是切换前的私有仓库存档，私有时期合的分支只在那段历史里）。

import (
	"fmt"
	"regexp"
	"strings"

	"sumika-native/internal/devtools/gitrun"
)

var (
	// 明说没合的：一行里有就算。
	notMergedRe = regexp.MustCompile(`(?:未|没)合[` + jsSpace + `]*main|(?:未|没)合(?:并|进|入)|待合(?:并|入)|还没合`)
	// “在分支 X 上”是描述，同一行又写了“已合”就不算（“在分支 X 上做，11-07 已合 main”）。
	onBranchRe = regexp.MustCompile(`在分支[^，。；;,]{0,60}上`)
	// 分支名：ASCII 的一段，按整段去合并记录里查，所以 `feat/x-pub` 不会被当成 `feat/x`。
	branchTokRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._/-]*[A-Za-z0-9]`)

	prMergeRe  = regexp.MustCompile(`^Merge pull request #(\d+) from [^/\s]+/(\S+)`)
	gitMergeRe = regexp.MustCompile(`^Merge (remote-tracking )?branch '([^']+)'`)
	cnMergeRe  = regexp.MustCompile(`^合并 ([A-Za-z0-9][A-Za-z0-9._/-]*[A-Za-z0-9])`)
)

// mergeInfo 是一个分支最近一次合进 main 的那个合并提交。
type mergeInfo struct {
	hash string // 短哈希
	pr   string // PR 号，不是 PR 合并的为空
	ref  string // 在哪个 main 上找到的，如 origin/main
	rank int    // origin/main 0、本地 main 1、其他远端 2：同一分支在几处都有时取靠前的
}

// branchLike：至少带一个 / 或 -，免得把普通英文词当分支名。
func branchLike(s string) bool { return strings.ContainsAny(s, "/-") }

// mergedSource 从合并提交标题里取被合进来的源分支；不是合并标题、或源是 main 本身（把 main 合进功能分支）的返回空。
func mergedSource(subject string) (branch, pr string) {
	if m := prMergeRe.FindStringSubmatch(subject); m != nil {
		branch, pr = m[2], m[1]
	} else if m := gitMergeRe.FindStringSubmatch(subject); m != nil {
		branch = m[2]
		if m[1] != "" { // remote-tracking branch 'origin/feat/x'：去掉远端名
			if _, rest, ok := strings.Cut(branch, "/"); ok {
				branch = rest
			}
		}
	} else if m := cnMergeRe.FindStringSubmatch(subject); m != nil {
		branch = m[1]
	}
	if branch == "main" || !branchLike(branch) {
		return "", ""
	}
	return branch, pr
}

func refRank(ref string) int {
	switch ref {
	case "refs/remotes/origin/main":
		return 0
	case "refs/heads/main":
		return 1
	}
	return 2
}

// mergedBranches 读本地 main 和各远端 main 历史里的合并提交，返回 源分支 → 最近一次合并。
// 子进程清掉全部 GIT_*（gitrun 传 nil env）：钩子里继承来的 GIT_INDEX_FILE 不影响查提交历史，
// worktree 里靠当前目录找到仓库，refs 和主仓库共用。没有任何 main（新的空仓库）返回空表；git 报错返回 error。
func mergedBranches(root string) (map[string]mergeInfo, error) {
	out, code, err := gitrun.Run(root, nil, nil, "for-each-ref", "--format=%(refname)", "refs/heads/main", "refs/remotes")
	if err != nil || code != 0 {
		return nil, fmt.Errorf("列不出 main：%v %s", err, out)
	}
	var refs []string
	for _, r := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		r = strings.TrimSpace(r)
		rest, isRemote := strings.CutPrefix(r, "refs/remotes/")
		if r == "refs/heads/main" || isRemote && strings.Count(rest, "/") == 1 && strings.HasSuffix(rest, "/main") {
			refs = append(refs, r)
		}
	}
	got := map[string]mergeInfo{}
	if len(refs) == 0 {
		return got, nil
	}
	args := append([]string{"log", "--merges", "--source", "--format=%h%x09%S%x09%s"}, refs...)
	out, code, err = gitrun.Run(root, nil, nil, args...)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("读不了合并记录：%v %s", err, out)
	}
	// git log 按时间从新到旧，同一分支第一次见到的就是最近一次合并
	for _, l := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(strings.TrimRight(l, "\r"), "\t", 3)
		if len(parts) != 3 {
			continue
		}
		b, pr := mergedSource(parts[2])
		if b == "" {
			continue
		}
		rank := refRank(parts[1])
		if old, ok := got[b]; ok && old.rank <= rank {
			continue
		}
		ref := strings.TrimPrefix(strings.TrimPrefix(parts[1], "refs/remotes/"), "refs/heads/")
		got[b] = mergeInfo{hash: parts[0], pr: pr, ref: ref, rank: rank}
	}
	return got, nil
}

// staleBranches：这一行写着没合，提到的分支里哪些其实已合（按出现顺序、去重）。
// 分支名后面、到下一个逗号句号之前写了“已合”的不算（“`feat/a` 已合 main，`feat/b` 待合并”）。
func staleBranches(line string, merged map[string]mergeInfo) []string {
	if !notMergedRe.MatchString(line) && !(onBranchRe.MatchString(line) && !strings.Contains(line, "已合")) {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, ix := range branchTokRe.FindAllStringIndex(line, -1) {
		b := line[ix[0]:ix[1]]
		if _, ok := merged[b]; !ok || seen[b] {
			continue
		}
		clause := line[ix[1]:]
		if i := strings.IndexAny(clause, "，。；;,"); i >= 0 {
			clause = clause[:i]
		}
		if strings.Contains(clause, "已合") {
			continue
		}
		seen[b] = true
		out = append(out, b)
	}
	return out
}

// mergeStatusScope：现行文档。调研笔记、历史层、收件箱是当时的记录，决策记录只增不改，都不查。
func mergeStatusScope(f string) bool {
	if !strings.HasSuffix(f, ".md") {
		return false
	}
	if !strings.Contains(f, "/") {
		return true // 仓库根的 AGENTS、README 这些
	}
	return strings.HasPrefix(f, "docs/") && !strings.HasPrefix(f, "docs/notes/") && !strings.HasPrefix(f, "docs/archive/") &&
		!strings.HasPrefix(f, "docs/inbox/") && f != "docs/决策记录.md"
}

func (m mergeInfo) where() string {
	s := m.hash
	if m.pr != "" {
		s = "PR #" + m.pr + "，" + m.hash
	}
	if m.rank == 2 {
		s = m.ref + " 上的 " + s
	}
	return s
}

func (c *checker) d14(files []string) {
	var merged map[string]mergeInfo // 有要查的行才去读 git
	for _, f := range files {
		if !mergeStatusScope(f) {
			continue
		}
		for i, l := range strings.Split(c.read(f), "\n") {
			// 先用字面量粗筛，绝大多数行没有“合”也没有“在分支”
			if !strings.Contains(l, "合") && !strings.Contains(l, "在分支") {
				continue
			}
			if merged == nil {
				var err error
				if merged, err = mergedBranches(c.opt.Root); err != nil {
					c.bad("D14 合并状态没跟上", "（git）", "查不了哪些分支已合进 main，这项没查："+err.Error(), LevelWarn)
					return
				}
			}
			br := staleBranches(l, merged)
			if len(br) == 0 {
				continue
			}
			var parts []string
			for _, b := range br {
				parts = append(parts, fmt.Sprintf("%s 已合进 main（%s）", b, merged[b].where()))
			}
			c.bad("D14 合并状态没跟上", fmt.Sprintf("%s:%d", f, i+1),
				fmt.Sprintf("这行还写着没合 / 在分支上，但 %s；改成“已合 main（%s）”", strings.Join(parts, "、"), merged[br[0]].hash), LevelWarn)
		}
	}
}
