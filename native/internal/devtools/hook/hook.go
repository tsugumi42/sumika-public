// Package hook 是 sumika-dev hook：git 钩子的统一入口（规格-检查统一入口 设计第 1、2 节）。
//
//	sumika-dev hook pre-commit        按暂存区里的文件挑检查（table.go 的 Items），在一个进程里并行跑
//	sumika-dev hook commit-msg <文件>  提交说明的标题检查（commitmsg 包）
//
// lefthook.yml 的 pre-commit、commit-msg 各只有一项，调的就是这里。以前提交前是 lefthook 并行起十来个进程，其中六项各自“程序不在就现编”，
// 几项同时现编、抢着换同一个 exe，抢输的报“文件正被占用”（2026-11-19 两次）；现在钩子只起一个进程，最多编一次，撞车没有了。
// 已迁进 Go 的检查（隐私、文档、冲突标记、gofmt、ui-skill、go vet）直接调函数，不起子进程；还没迁的 bun 项起子进程，每项在 table.go 里注明“迁完删”。
//
// 每一项有自己的输出缓冲，跑完按表里的顺序一项一段地打印：并行项不共用输出文件、临时目录或缓冲（规格“禁止的偷懒写法”）。
// 任何一项红（退出码非零、起不来、崩溃），总退出码就是 1。
package hook

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/devtools/commitmsg"
	"sumika-native/internal/devtools/gitrun"
	"sumika-native/internal/devtools/jstext"
	"sumika-native/internal/devtools/selfbuild"
)

// BunProgram、GoProgram 是 bun、go 程序名（internal/ 里 exec.Command 的第一个参数不许是字面量，和 gitrun.Program 一样用变量）。
// go 优先用编出本程序的那套 Go（selfbuild.GoBinary），找不到再按名字去 PATH 里找。
var (
	BunProgram = "bun"
	GoProgram  = "go"
)

func goProgram() string {
	if p, err := selfbuild.GoBinary(); err == nil {
		return p
	}
	return GoProgram
}

// Ctx 是每项检查共用的只读信息。
type Ctx struct {
	Root string // 仓库根（程序所在的那棵树）
}

// spawn 在 dir 里起子进程，标准输出和标准错误都写进 w（同一个 writer，exec 保证同一时刻只有一个 goroutine 写它）；返回退出码。
// 子进程的环境去掉全部 GIT_*：钩子里继承来的 GIT_DIR、GIT_INDEX_FILE 会让它里面的 git 落到别的仓库或索引上（LESSONS：钩子和测试里起 git 要清 GIT_*）。
// 起不来（程序找不到等）算红，原因写进 w，不当成通过。
func (c *Ctx) spawn(w io.Writer, dir, name string, args ...string) int {
	return c.spawnSplit(w, w, dir, name, args...)
}

func (c *Ctx) spawnSplit(stdout, stderr io.Writer, dir, name string, args ...string) int {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = gitrun.CleanEnv()
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		if code := ee.ExitCode(); code != 0 {
			return code
		}
		return 1
	default:
		fmt.Fprintf(stderr, "起不来 %s：%s\n", name, err)
		return 1
	}
}

// Staged 返回暂存区里新增、修改、改名、复制的文件（不含删除，和 lefthook 的 {staged_files} 一样），路径相对仓库根、正斜杠。
// 有意继承钩子环境：部分提交（git commit <文件>）时 GIT_INDEX_FILE 指向“这次要提交的索引”。
func Staged(root string, env []string) ([]string, error) { return stagedFiles(root, env, "ACMR") }

// stagedAny 返回暂存区里有任何改动（含纯删除）的文件；lefthook 靠它判断“这次提交什么都没暂存”，那时整个钩子跳过。
func stagedAny(root string, env []string) ([]string, error) { return stagedFiles(root, env, "") }

func stagedFiles(root string, env []string, filter string) ([]string, error) {
	args := []string{"diff", "--cached", "--name-only", "-z"}
	if filter != "" {
		args = append(args, "--diff-filter="+filter)
	}
	out, errOut, code, err := gitrun.RunStderr(root, env, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("读不到暂存区的文件：%w", err)
	}
	if code != 0 {
		return nil, fmt.Errorf("读不到暂存区的文件：%s", jstext.Trim(jstext.DecodeLossy(errOut)))
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, strings.ReplaceAll(f, "\\", "/"))
		}
	}
	return files, nil
}

// result 是一项检查的结果。
type result struct {
	code    int
	out     bytes.Buffer
	elapsed time.Duration
}

// Execute 挑出要跑的检查、并行跑完、按表里的顺序打印，返回退出码（有任何一项红就是 1）。
func Execute(c *Ctx, items []Item, staged []string, stdout io.Writer) int {
	sel := Select(items, staged)
	results := make([]*result, len(sel))
	var wg sync.WaitGroup
	for i := range sel {
		results[i] = &result{}
		wg.Add(1)
		go func(s Selected, r *result) {
			defer wg.Done()
			start := time.Now()
			defer func() {
				r.elapsed = time.Since(start)
				if p := recover(); p != nil {
					fmt.Fprintf(&r.out, "检查自己崩溃了：%v\n", p)
					r.code = 1
				}
			}()
			r.code = s.Run(c, s.Files, &r.out)
		}(sel[i], results[i])
	}
	wg.Wait()

	var red []string
	for i, s := range sel {
		r := results[i]
		state := "过"
		if r.code != 0 {
			state = fmt.Sprintf("红（退出码 %d）", r.code)
			red = append(red, s.Name)
		}
		fmt.Fprintf(stdout, "== %s：%s（%.1f 秒）==\n", s.Name, state, r.elapsed.Seconds())
		if text := strings.TrimRight(r.out.String(), "\r\n"); text != "" {
			fmt.Fprintln(stdout, text)
		}
	}
	pass, notPicked := len(sel)-len(red), len(items)-len(sel)
	fmt.Fprintf(stdout, "== 汇总：%d 项过、%d 项红、%d 项没挑上 ==\n", pass, len(red), notPicked)
	if len(red) > 0 {
		fmt.Fprintf(stdout, "红的：%s\n", strings.Join(red, "、"))
		return 1
	}
	return 0
}

// PreCommit 是 sumika-dev hook pre-commit 的入口：root 是仓库根；不收参数。
func PreCommit(_ []string, root string, stdout, _ io.Writer) int {
	// 暂存区里什么改动都没有（git commit --allow-empty、只改说明的 --amend）：和原 lefthook 一样整个跳过，不替空提交去跑隐私、文档、冲突标记
	changed, err := stagedAny(root, os.Environ())
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	if len(changed) == 0 {
		fmt.Fprintln(stdout, "暂存区没有改动，没有要检查的")
		return 0
	}
	staged, err := Staged(root, os.Environ())
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	return Execute(&Ctx{Root: root}, Items, staged, stdout)
}

// Hooks 是 hook 下的钩子名 → 入口（参数是钩子名后面的部分，root 是仓库根）。
var Hooks = map[string]func(args []string, root string, stdout, stderr io.Writer) int{
	"pre-commit": PreCommit,
	"commit-msg": commitmsg.Run,
}
