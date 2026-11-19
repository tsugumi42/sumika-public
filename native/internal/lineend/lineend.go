// Package lineend 决定她写整个文件时用 LF 还是 CRLF（规格-写文件换行）。
// 审批预览和真写入都只调用 Final，同一组输入得同一串字节，不各算一份。
package lineend

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/editorconfig/editorconfig-core-go/v2"

	"sumika-native/internal/edittext"
	"sumika-native/internal/policy"
	"sumika-native/internal/toolchain"
)

const (
	lf   = "\n"
	crlf = "\r\n"
	bom  = "\uFEFF"

	// SniffMax：已有文件只看开头这么多字节来判断换行和是不是二进制
	SniffMax = 1 << 20
	// binarySniff：前这么多字节里有 NUL 就当二进制（git 的做法）
	binarySniff = 8000
	// gitTimeout：两次 git 查询合起来最多等多久；到点当作不在仓库里
	gitTimeout = time.Second
)

var setting atomic.Value // string：auto / lf / crlf

// SetDefault 设 tools.lineEnding；空或不认识的当 auto。设置改了以后当场调用，下一次写就按新值。
func SetDefault(mode string) {
	switch mode {
	case "lf", "crlf":
		setting.Store(mode)
	default:
		setting.Store("auto")
	}
}

// Default 是当前的 tools.lineEnding
func Default() string {
	if s, _ := setting.Load().(string); s != "" {
		return s
	}
	return "auto"
}

// Final 返回 write 最终落到磁盘上的内容。
// abs 是目标的绝对路径；old 是目标现在的内容（不存在或读不了传 nil，只用开头 SniffMax 字节）；content 是她给的正文。
// 换行按这个顺序取第一个能定下来的：已有文件 → .editorconfig → Git 检出规则 → 配置 tools.lineEnding。
// 先把正文统一成 LF，再换成选定的；正文自己没带 BOM 而原文件带着时保留原文件的 BOM（和 edit 一致）。
func Final(abs string, old []byte, content string) string {
	if len(old) > SniffMax {
		old = old[:SniffMax]
	}
	hadBOM := bytes.HasPrefix(old, []byte(bom))
	// 网络共享、设备路径不去连：找 .editorconfig 和 git 都会碰它，和预览里“不读它”一致
	ending, raw := decide(abs, old, policy.IsOpaquePath(abs))
	if raw {
		return content
	}
	prefix := ""
	if strings.HasPrefix(content, bom) {
		prefix, content = bom, content[len(bom):]
	} else if hadBOM {
		prefix = bom
	}
	content = strings.ReplaceAll(strings.ReplaceAll(content, crlf, lf), "\r", lf)
	if ending == crlf {
		content = strings.ReplaceAll(content, lf, crlf)
	}
	return prefix + content
}

// decide 返回选定的换行；raw 为 true 表示正文原样写（Git 说这个路径 -text）
func decide(abs string, old []byte, opaque bool) (ending string, raw bool) {
	if e, ok := fromExisting(old); ok {
		return e, false
	}
	if !opaque {
		if e, ok := fromEditorconfig(abs); ok {
			return e, false
		}
		if e, rawText, ok := fromGit(abs); ok {
			return e, rawText
		}
	}
	return fromSetting(), false
}

func fromSetting() string {
	switch Default() {
	case "lf":
		return lf
	case "crlf":
		return crlf
	}
	if runtime.GOOS == "windows" {
		return crlf
	}
	return lf
}

// fromExisting 沿用已有文件的换行，判断和 edit 是同一个函数；二进制或一个换行都没有就不算数
func fromExisting(old []byte) (string, bool) {
	if len(old) == 0 {
		return "", false
	}
	head := old
	if len(head) > binarySniff {
		head = head[:binarySniff]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return "", false
	}
	return edittext.LineEnding(strings.TrimPrefix(string(old), bom))
}

// ReadExisting 读目标现在的开头（最多 SniffMax 字节）给 Final 用；不存在、不是普通文件、读不了、
// 是网络共享或设备路径（不去连）都返回 nil
func ReadExisting(abs string) []byte {
	if policy.IsOpaquePath(abs) {
		return nil
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, SniffMax))
	return b
}

// fromEditorconfig 取该路径生效的 end_of_line（root = true 以上的不看，这个库照 editorconfig.org 的规范处理）。
// cr 不支持：按 LF 写并记一行日志；unset 和别的写法当没写
func fromEditorconfig(abs string) (string, bool) {
	def, err := editorconfig.GetDefinitionForFilename(abs)
	if err != nil || def == nil {
		return "", false
	}
	switch strings.ToLower(def.EndOfLine) {
	case "lf":
		return lf, true
	case "crlf":
		return crlf, true
	case "cr":
		slog.Warn(".editorconfig 写了 end_of_line = cr，不支持，按 LF 写", "path", abs)
		return lf, true
	}
	return "", false
}

// 起 git 时保留的 GIT_* 变量：只是指给哪份用户配置，不会让 git 去查别的仓库
var keepGitEnv = map[string]bool{"GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_SYSTEM": true, "GIT_CONFIG_NOSYSTEM": true}

func gitEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToUpper(k), "GIT_") && !keepGitEnv[strings.ToUpper(k)] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// lookGit 找 git：外部程序一律向 toolchain 要（archcheck 管着）；测试换掉它来模拟“没装 git”。
var lookGit = func() (string, bool) {
	r, ok := toolchain.Lookup("git")
	return r.Path, ok
}

// fromGit 照 Git 检出这个文件时会用的换行：-text 原样；eol 属性；core.autocrlf=true；core.eol。
// git 不在、不在仓库里、超时、出错都算没答案。解析交给 git 自己（.gitattributes 的匹配规则很复杂）
func fromGit(abs string) (ending string, raw, ok bool) {
	gitPath, found := lookGit()
	if !found {
		return "", false, false
	}
	dir := filepath.Dir(abs)
	for { // 目标的上级可能还没建（预览时）：从最近的已存在的上级问
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			break
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", false, false
		}
		dir = up
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	run := func(args ...string) (string, bool) {
		cmd := exec.CommandContext(ctx, gitPath, args...)
		cmd.Dir, cmd.Env = dir, gitEnv()
		out, err := cmd.Output()
		return string(out), err == nil
	}
	out, good := run("check-attr", "-z", "eol", "text", "--", abs)
	if !good {
		return "", false, false
	}
	attrs := map[string]string{}
	parts := strings.Split(out, "\x00") // 路径、属性名、值 三个一组
	for i := 0; i+2 < len(parts); i += 3 {
		attrs[parts[i+1]] = parts[i+2]
	}
	if attrs["text"] == "unset" {
		return "", true, true
	}
	switch attrs["eol"] {
	case "lf":
		return lf, false, true
	case "crlf":
		return crlf, false, true
	}
	cfg, good := run("config", "--get-regexp", `^core\.(autocrlf|eol)$`)
	if !good {
		return "", false, false
	}
	conf := map[string]string{}
	for _, line := range strings.Split(cfg, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), " ")
		conf[strings.ToLower(k)] = strings.ToLower(strings.TrimSpace(v))
	}
	if conf["core.autocrlf"] == "true" {
		return crlf, false, true
	}
	switch conf["core.eol"] {
	case "lf":
		return lf, false, true
	case "crlf":
		return crlf, false, true
	}
	return "", false, false
}
