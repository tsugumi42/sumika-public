// Package embedinputs 是"生产实际会送给嵌入和重排的字符串"的文件格式（规格-Go本地模型 第五节"真实的"一段）：
// memory-eval -dump-embed-inputs 写（原来读它的 cmd/models-parity 和 Python 的 parity_ref.py 已删，ADR-389）。
//
// 文件里是各可见范围的记忆和群消息原文混在一起，所以只许写在临时目录或被 git 忽略的路径；
// 写之前要过 CheckPrivatePath，不放在 git 工作树里没被忽略的地方（复核方提的，规格 GM6）。
package embedinputs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"sumika-native/internal/toolchain"
)

// Version 是格式版本；读到别的版本就报错。
const Version = 1

// File 的每个列表去重、保持第一次出现的顺序。
type File struct {
	Version   int           `json:"version"`
	Items     []string      `json:"items"`     // 库里每条记忆送去嵌入的文本（document）
	Queries   []string      `json:"queries"`   // 考题里的查询（query）
	Group     []string      `json:"group"`     // 真实群消息当查询送去嵌入的文本（query）
	Sentences []string      `json:"sentences"` // 守卫切出的句子：每条记忆的文本经 memory.OutboundSentences
	Rerank    []RerankInput `json:"rerank"`    // 重排的（查询，候选）
}

// RerankInput 是一次重排调用：一个查询，一组候选。
type RerankInput struct {
	Query string   `json:"query"`
	Docs  []string `json:"docs"`
}

// Texts 返回所有要嵌入的文本数（不去重）。
func (f File) Texts() int { return len(f.Items) + len(f.Queries) + len(f.Group) + len(f.Sentences) }

// Read 读一份文件。
func Read(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("解析 %s：%w", filepath.Base(path), err)
	}
	if f.Version != Version {
		return File{}, fmt.Errorf("%s 的格式版本是 %d，这个程序认 %d", filepath.Base(path), f.Version, Version)
	}
	return f, nil
}

// Write 先过 CheckPrivatePath，再写（0600）。目录不存在会建。
func Write(path string, f File) error {
	if err := CheckPrivatePath(path); err != nil {
		return err
	}
	f.Version = Version
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(f); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o600)
}

// CheckPrivatePath 拒绝 git 工作树里没被忽略的路径：这份文件里是真实聊天和记忆的原文，不能让它有机会被 git add 进去。
// 不在任何 git 工作树里（临时目录、会话草稿目录）、或在工作树里但被 .gitignore 管着，都放行。
// 判断不了（git 报了别的错）按拒绝处理：宁可多问一句。机器上没有 git 时放行（没有 git 也就没有"不小心提交"）。
func CheckPrivatePath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)
	for { // 目标目录可能还没建：从最近的已存在的上级目录问 git
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			break
		}
		up := filepath.Dir(dir)
		if up == dir {
			return nil
		}
		dir = up
	}
	git, ok := toolchain.Lookup("git")
	if !ok {
		return nil
	}
	out, err := exec.Command(git.Path, "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) { // 128：不在任何仓库里
			return nil
		}
		return fmt.Errorf("问 git %s 是不是在工作树里失败：%w", dir, err)
	}
	if string(bytes.TrimSpace(out)) != "true" { // 在 .git 目录里面之类
		return fmt.Errorf("%s 在 git 目录里，不能放真实样本", abs)
	}
	err = exec.Command(git.Path, "-C", dir, "check-ignore", "-q", "--", abs).Run()
	if err == nil {
		return nil // 被忽略
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return fmt.Errorf("%s 在 git 工作树里，而且没被 .gitignore 忽略：真实样本只许放临时目录或被忽略的路径", abs)
	}
	return fmt.Errorf("问 git %s 有没有被忽略失败：%w", abs, err)
}
