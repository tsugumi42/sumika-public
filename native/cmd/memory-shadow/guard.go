package main

// 路径守卫：拷谁、往哪写。两道门，都只收紧（规格-记忆写入上线 W6 技术路线第 1、2 点）：
//   - 源：只拷 Go 的副本（<数据目录>/go/ 下），不拷 Bun 的原库。记忆库用 W5 的 paths.CheckMemoryPath（memory-prepare、
//     写入的前提检查是同一个函数）；trace.db、go/chat 下的聊天库不在 go/characters/ 下，用 strictGuard 按真实位置比；联接和符号链接都不认；
//   - 目的：-work 必须在 <数据目录>/dev/ 下，并且不会被 git 提交（聊天正文的副本和报告不能进仓库）。

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"sumika-native/internal/memory"
	"sumika-native/internal/paths"
	"sumika-native/internal/safepath"
)

// samePath 比两条已经解开链接的路径是不是同一个位置：Windows 上不分大小写
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// pathGuard 查数据目录下的一个相对路径（斜杠写法）是不是"字面上就在那儿"。拷的每个文件（含 -wal、-shm）都过一遍
type pathGuard func(data, rel string) error

// strictGuard：rel 解开所有符号链接和联接以后，还等于"解开后的数据目录 + rel"，否则拒绝（Bun 的库、被指到别处的目录、
// 数据目录里的联接都过不了）。文件必须存在（EvalSymlinks 要求）
func strictGuard(data, rel string) error {
	realData, err := filepath.EvalSymlinks(data)
	if err != nil {
		return fmt.Errorf("数据目录 %s 解不开：%w", data, err)
	}
	want := filepath.Join(realData, filepath.FromSlash(rel))
	got, err := filepath.EvalSymlinks(filepath.Join(data, filepath.FromSlash(rel)))
	if err != nil {
		return fmt.Errorf("看 %s：%w", rel, err)
	}
	if !samePath(got, want) {
		return fmt.Errorf("%s 的真实位置不在数据目录的同一处（联接或符号链接？），不拷", rel)
	}
	return nil
}

// memoryRel 是这个角色的 Go 记忆库在数据目录里的相对路径
func memoryRel(char string) string { return "go/characters/" + char + "/memory.db" }

// checkMemorySource 只拷固定角色目录里的普通记忆库；复用写入侧的路径守卫。
func checkMemorySource(data, char string) (string, error) {
	if err := memory.ValidCharacterID(char); err != nil {
		return "", err
	}
	got := (paths.Paths{Root: data}).MemoryDB(char)
	rel := memoryRel(char)
	if _, err := os.Stat(got); err != nil {
		return "", fmt.Errorf("Go 的记忆库 %s 不存在：%w", rel, err)
	}
	if err := paths.CheckMemoryPath(data, got); err != nil {
		return "", fmt.Errorf("记忆库 %s 过不了副本路径检查，不拷：%w", rel, err)
	}
	return rel, nil
}

// ignoreVerdict 是 git check-ignore 的三种结局
type ignoreVerdict int

const (
	verdictIgnored ignoreVerdict = iota // 被忽略：不会被提交
	verdictTracked                      // 在仓库里、没被忽略：会冒出来被提交
	verdictNoRepo                       // 不在任何仓库里：也提交不了
)

// ignoreCheck 问一个路径有没有被 git 忽略；测试换成假的
type ignoreCheck func(path string) (ignoreVerdict, error)

// hasGitEntry：dir 或它的上级里有没有 .git（目录，或 worktree / 子模块那种文件）。只看文件系统，不问 git
func hasGitEntry(dir string) bool {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// gitCheckIgnore 真的跑 git check-ignore：0 = 忽略，1 = 没忽略，128 = 不在仓库里（或 git 出了别的致命错误）。
// 128 是 git 通用的 fatal，"dubious ownership"（重装系统后目录属主变过）、坏的 .git 文件、坏索引、坏配置也是它，
// 光看退出码分不开，所以 128 时再看文件系统：往上没有任何 .git 才算根本不在仓库里，有 .git 却报 128 就是核实不了，不放行。
// 找不到 git 或别的错也当失败
func gitCheckIgnore(path string) (ignoreVerdict, error) {
	dir := path
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return 0, fmt.Errorf("%s 的上级目录都不存在", path)
		}
		dir = parent
	}
	cmd := exec.Command("git", "check-ignore", "-q", "--", path)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return verdictIgnored, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		switch ee.ExitCode() {
		case 1:
			return verdictTracked, nil
		case 128:
			if hasGitEntry(dir) {
				return 0, fmt.Errorf("git check-ignore 报错（退出码 128），而 %s 在一个带 .git 的目录树里：目录属主不对（dubious ownership）、.git 或索引坏了都会这样，核实不了 -work 会不会被提交，不放行", path)
			}
			return verdictNoRepo, nil
		}
	}
	return 0, fmt.Errorf("跑 git check-ignore 失败，核实不了 -work 会不会被提交：%w", err)
}

// 数据目录里别的角色读得到的位置：-work 不许落在它们里面，也不许是它们的父目录
var sensitiveDirs = []string{"go", "characters", "shared", "config", "logs"}

// checkWorkDir 返回 -work 的绝对路径。要求：在 <数据目录>/dev/ 下面（真实位置，联接不认），不在 go、characters、shared 这些里面，
// 并且 git 不会提交它（被忽略，或者根本不在仓库里）。路径本身不要求已经存在
func checkWorkDir(data, work string, ignored ignoreCheck) (string, error) {
	absData, err := filepath.Abs(data)
	if err != nil {
		return "", err
	}
	absWork, err := filepath.Abs(work)
	if err != nil {
		return "", err
	}
	dev := filepath.Join(absData, "dev")
	// 相对路径（-work ./x）会按当前目录补成绝对路径，落进仓库工作树的话在这里就过不了"在 dev 下面"
	if err := safepath.Inside(nil, dev, absWork); err != nil || samePath(absWork, dev) {
		return "", fmt.Errorf("-work 必须在 %s 下面（聊天库的副本和报告只许放在这里，不能进仓库工作树）：%s", dev, absWork)
	}
	// dev 本身不能是指到数据目录外面的联接
	if err := safepath.Inside(nil, absData, dev); err != nil {
		return "", fmt.Errorf("%s 解开以后不在数据目录里，不用", dev)
	}
	for _, d := range sensitiveDirs {
		dir := filepath.Join(absData, d)
		if err := safepath.Inside(nil, dir, absWork); err == nil {
			return "", fmt.Errorf("-work 不能在 %s 里面：别的角色和程序读得到那里", dir)
		}
	}
	if ignored == nil {
		ignored = gitCheckIgnore
	}
	if err := os.MkdirAll(absWork, 0o755); err != nil {
		return "", err
	}
	v, err := ignored(absWork)
	if err != nil {
		os.Remove(absWork)
		return "", err
	}
	if v == verdictTracked {
		os.Remove(absWork)
		return "", fmt.Errorf("-work（%s）没有被 git 忽略，会冒出来被提交：先让 .gitignore 管住它", absWork)
	}
	return absWork, nil
}
