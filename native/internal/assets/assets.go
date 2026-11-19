// Package assets 是软件自带素材的公共服务：图标（icons，MingCute）、表情（emoji，Twemoji）、字体（文渊圆体、Maple Mono、Windows 自带字体兜底）。
// 本包管“素材目录在哪”，只有一套找法，chatui、linkcard、主程序和将来给插件的接口都用它，不各自再找一遍：
//
//  1. 调用方明说的目录（设置里给的、测试里临时造的），给了就只用它；
//  2. 环境变量（CHATUI_TWEMOJI 表情目录；SUMIKA_FONT 文渊圆体文件，见 assets/fonts）；
//  3. 程序旁边：exe 所在目录下的 fonts/、twemoji/——正式程序打包后素材放在这里（native/README.md「打包」）；
//  4. 开发时：仓库 native/fonts/、native/twemoji/（go run、样张工具、测试 exe 旁边没有素材目录时靠它）；
//     在 worktree 里跑时这些目录是空的，就回主工作区去找（worktree 的 .git 文件里写着主仓库的位置）。
//
// 大素材（字体 48 MB、Twemoji 四千多个 SVG）不进 git，目录只在本机和打包产物里有；找不到时返回空串和试过的路径，由调用方决定怎么降级、记什么日志。
package assets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 一个 Twemoji 目录是不是有效，看里面有没有这张图（👍）。
const twemojiMarker = "1f44d.svg"

// Twemoji 找 Twemoji 的 SVG 目录；explicit 非空就只用它。找不到返回空串，tried 是试过的目录。
func Twemoji(explicit string) (dir string, tried []string) {
	var cands []string
	if explicit != "" {
		cands = []string{explicit}
	} else {
		if p := os.Getenv("CHATUI_TWEMOJI"); p != "" {
			cands = append(cands, p)
		}
		cands = append(cands, besideExe("twemoji")...)
		cands = append(cands, devDirs("twemoji")...)
	}
	for _, d := range cands {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, twemojiMarker)); err == nil {
			return d, nil
		}
		tried = append(tried, d)
	}
	return "", tried
}

// TwemojiDir 是 Twemoji 的目录，找不到返回空串；不关心试过哪些路径的调用方用它。
func TwemojiDir() string {
	d, _ := Twemoji("")
	return d
}

// FontDirs 是素材字体目录（放文渊圆体和 Maple 的地方），按先后找：exe 旁的 fonts/，再是开发用的 native/fonts/（worktree 里再加主工作区的）。
// 字体文件很大不进 git，目录里不一定有；是否存在由 assets/fonts 一个个文件去查。
func FontDirs() []string {
	return append(besideExe("fonts"), devDirs("fonts")...)
}

// besideExe 是程序旁边的素材目录 <exe 目录>/<name>；取不到 exe 路径时没有。
func besideExe(name string) []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(filepath.Dir(exe), name)}
}

// devDirs 是开发用的素材目录：本仓库的 native/<name>，在 worktree 里再加主工作区的。
func devDirs(name string) []string {
	var out []string
	for _, root := range devRoots() {
		out = append(out, filepath.Join(root, name))
	}
	return out
}

// devRoots 是 native/ 目录：先本仓库，在 worktree 里再加主工作区（worktree 的仓库根下 .git 是个文件："gitdir: <主仓库>/.git/worktrees/<名字>"）。
func devRoots() []string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil
	}
	native := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	out := []string{native}
	b, err := os.ReadFile(filepath.Join(native, "..", ".git"))
	if err != nil {
		return out
	}
	line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	if i := strings.Index(filepath.ToSlash(line), "/.git/worktrees/"); i > 0 {
		out = append(out, filepath.Join(filepath.FromSlash(filepath.ToSlash(line)[:i]), "native"))
	}
	return out
}
