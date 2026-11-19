package notice

// 把公开仓库 main 上的 CI 失败带进上下文。
// 问题：合进 main 以后那一轮 CI、每天定时的整套红了，没有任何会话知道，只有主人收到 GitHub 邮件。
// 做法：每次钩子运行，用 gh 查公开仓库（origin）最近 48 小时里 分支是 main、事件是 push 或 schedule 的运行，挑结论是 failure 的，
// 每条一行：工作流名、北京时间、短提交号、哪个 job 的哪一步红、运行链接。不下载日志。
//
// 信任边界：只认 main 上的 push / schedule。PR 的分支名、标题是外人可写的，所以不过滤也不带进上下文；
// 能进上下文的只有工作流名、job 名、步骤名（都来自 main 上的工作流文件）、提交号、时间，全部清掉控制字符并截短；链接自己用运行编号拼，不用接口返回的 URL。
// 硬时限：gh 调用总共不超过 CITimeout（默认 5 秒），没装、没登录、离线、超时、没有 origin，一律静默少这一段，不挡发消息。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"sumika-native/internal/devtools/gitrun"
)

const (
	ciBranch      = "main"
	ciWindow      = 48 * time.Hour
	ciListLimit   = 30 // gh run list 取多少条
	ciShowMax     = 5  // 一次最多列几条（其余留到下一条消息）
	ciJobsMax     = 4  // 一条里最多写几个红的 job
	ciNameMax     = 60 // 工作流名、job 名、步骤名最长（字符）
	defaultBudget = 5 * time.Second
)

// GhProgram 是 gh 程序名或绝对路径，由 cmd/sumika-dev 的 main 注入（internal/ 下非测试代码里 exec 的第一个参数不许是字面量，archcheck 的外部程序规则）。
var GhProgram string

// GhFunc 跑一次 gh，返回标准输出。测试里换成假的，不连网。
type GhFunc func(ctx context.Context, args ...string) ([]byte, error)

// CIOptions 是 CI 查询的设置。
type CIOptions struct {
	Repo        string        // owner/name；从 origin 推出
	Gh          GhFunc        // 空就用真的 gh（GhProgram）
	Timeout     time.Duration // gh 调用总共的时限；0 是 5 秒
	CacheTTL    time.Duration // 查到的结果缓存多久（缓存文件在报告目录里，各会话共用）；0 不缓存
	FailBackoff time.Duration // gh 查询失败后多久内不再重试（只在开了缓存时有效）
}

// RealGh 是真的 gh：不弹交互提示、不检查更新、不上色，标准输入是空的，超时就杀掉。
func RealGh(ctx context.Context, args ...string) ([]byte, error) {
	if GhProgram == "" {
		return nil, errors.New("gh 程序名没有注入")
	}
	cmd := exec.CommandContext(ctx, GhProgram, args...)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_SPINNER_DISABLED=1", "NO_COLOR=1")
	cmd.WaitDelay = 300 * time.Millisecond // 子进程拿着管道不放时也不拖住我们
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("gh 超时：%w", ctx.Err())
		}
		msg := strings.TrimSpace(se.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		return nil, fmt.Errorf("gh 失败：%v %s", err, clean(msg))
	}
	return so.Bytes(), nil
}

type ghRun struct {
	ID         int64  `json:"databaseId"`
	Workflow   string `json:"workflowName"`
	Event      string `json:"event"`
	Branch     string `json:"headBranch"`
	SHA        string `json:"headSha"`
	CreatedAt  string `json:"createdAt"`
	Conclusion string `json:"conclusion"`
}

type ghJobs struct {
	Jobs []struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
		Steps      []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
		} `json:"steps"`
	} `json:"jobs"`
}

var (
	shaRe     = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	repoPart  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	beijing   = time.FixedZone("UTC+8", 8*3600)
	spaceRuns = regexp.MustCompile(`\s+`)
)

// clean 把外来的名字压成一行可以放心放进上下文的文字：控制字符和换行变空格、连续空白合成一个、超过 ciNameMax 个字符截断。
func clean(s string) string { return cleanN(s, ciNameMax) }

// cleanN 同 clean，最长 max 个字符。
func cleanN(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(spaceRuns.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// RepoFromRemote 从 git remote 的地址推出 owner/name，只认 github.com（https、ssh 两种写法，带不带 .git 都行）。
func RepoFromRemote(remote string) (string, bool) {
	r := strings.TrimSpace(remote)
	var path string
	switch {
	case strings.HasPrefix(r, "git@"): // git@github.com:owner/name.git
		host, p, ok := strings.Cut(strings.TrimPrefix(r, "git@"), ":")
		if !ok || !strings.EqualFold(host, "github.com") {
			return "", false
		}
		path = p
	default: // https://…、http://…、ssh://git@github.com/owner/name
		i := strings.Index(r, "://")
		if i < 0 {
			return "", false
		}
		rest := r[i+3:]
		host, p, ok := strings.Cut(rest, "/")
		if !ok {
			return "", false
		}
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:] // 去掉 user:token@
		}
		if !strings.EqualFold(host, "github.com") {
			return "", false
		}
		path = p
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	owner, name, ok := strings.Cut(path, "/")
	if !ok || !repoPart.MatchString(owner) || !repoPart.MatchString(name) || strings.Contains(name, "/") {
		return "", false
	}
	return owner + "/" + name, true
}

// OriginRepo 在 dir 里问 git 的 origin 是哪个 GitHub 仓库；没有 origin、不是 GitHub、git 起不来都返回错误。
func OriginRepo(dir string) (string, error) {
	out, code, err := gitrun.Run(dir, nil, nil, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("git 起不来：%v", err)
	}
	if code != 0 {
		return "", errors.New("没有 origin")
	}
	repo, ok := RepoFromRemote(string(out))
	if !ok {
		return "", errors.New("origin 不是 GitHub 仓库")
	}
	return repo, nil
}

func orDash(s string) string {
	if s == "" {
		return "（无名工作流）"
	}
	return s
}

// failedJobs 写“哪个 job 的哪一步红”，问不到就写没取到。
func failedJobs(ctx context.Context, gh GhFunc, repo string, id int64) string {
	raw, err := gh(ctx, "run", "view", fmt.Sprint(id), "--repo", repo, "--json", "jobs")
	if err != nil {
		return "（job 详情没取到）"
	}
	var jobs ghJobs
	if err := json.Unmarshal(raw, &jobs); err != nil {
		return "（job 详情没取到）"
	}
	var items []string
	for _, j := range jobs.Jobs {
		if j.Conclusion != "failure" {
			continue
		}
		step := ""
		for _, s := range j.Steps {
			if s.Conclusion == "failure" {
				step = clean(s.Name)
				break
			}
		}
		if step == "" {
			items = append(items, orDash(clean(j.Name))+"（没有标红的步骤）")
		} else {
			items = append(items, fmt.Sprintf("%s 的「%s」", orDash(clean(j.Name)), step))
		}
	}
	if len(items) == 0 {
		return "（没有标红的 job）"
	}
	more := ""
	if len(items) > ciJobsMax {
		more = fmt.Sprintf("，另有 %d 个 job 也红了", len(items)-ciJobsMax)
		items = items[:ciJobsMax]
	}
	return "红在 " + strings.Join(items, "；") + more
}
