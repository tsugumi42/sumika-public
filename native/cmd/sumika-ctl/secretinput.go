package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// secretStdin 是 secrets 命令读值、主密码、恢复码的地方；测试换成脚本内容。
// 值、主密码、恢复码只从这里读，不接受命令行参数（会留在进程列表和命令历史里）。
var secretStdin io.Reader = os.Stdin

// errNoInput 是标准输入到头了、一个字符也没有。
var errNoInput = errors.New("没有从标准输入读到内容（值、主密码、恢复码从标准输入读）")

// usageError 是命令行本身写错了（值写在参数里、两次主密码不一致）：按“被拒”退出（3），不发请求。
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

// secretReader 按顺序读输入：同一个 bufio.Reader 读完几行，管道里一次给的多行不会被前一行吞掉。
type secretReader struct {
	stderr io.Writer
	in     *bufio.Reader
	first  bool // 还没读过：管道开头的 BOM 只在这一行去掉
}

func newSecretReader(stderr io.Writer) *secretReader {
	return &secretReader{stderr: stderr, in: bufio.NewReader(secretStdin), first: true}
}

// stdinIsPipe 标准输入是管道、文件（不是控制台）时为真：那种输入有明确的结尾，读完一行还能看到
// 后面剩下的内容；控制台上回车以后输入流还开着，不能等，也不需要等。
func stdinIsPipe() bool {
	f, ok := secretStdin.(*os.File)
	if !ok {
		return true // 测试里换成的脚本输入按管道算
	}
	fi, err := f.Stat()
	if err != nil {
		return true
	}
	return fi.Mode()&os.ModeCharDevice == 0
}

// read 读一行（去尾部的换行）。prompt 写到 stderr：--json 时 stdout 只有一个 JSON 对象。
// 标准输入是控制台时由 readHiddenLine 关掉回显。
func (r *secretReader) read(prompt string) (string, error) {
	if prompt != "" && r.stderr != nil {
		fmt.Fprint(r.stderr, prompt)
	}
	line, err := readHiddenLine(r.stderr, r.in)
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if r.first {
		r.first = false
		// PowerShell 的管道会带上 BOM：它不是值的一部分，只去掉开头这一个。
		line = strings.TrimPrefix(line, "\uFEFF")
	}
	if !utf8.ValidString(line) {
		// JSON 会把不合法的字节悄悄换成替换字符，存下去的值和输入的不一样（规格 5.3.3：值必须是合法 UTF-8）。
		return "", errors.New("标准输入读到的不是合法的 UTF-8 文本：换成 UTF-8 的终端，或者从管道给 UTF-8 文本")
	}
	return line, nil
}

// readOne 读一行，并要求管道里后面没有别的内容：一次只收一行，不会把一个文件喂进来只悄悄存了
// 第一行。标准输入是控制台时不看后面（回车以后输入流还开着）。
func (r *secretReader) readOne(prompt string) (string, error) {
	line, err := r.read(prompt)
	if err != nil {
		return "", err
	}
	if !stdinIsPipe() {
		return line, nil
	}
	rest, err := io.ReadAll(r.in)
	if err != nil {
		return "", fmt.Errorf("读标准输入出错：%w", err)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return "", usageError{"值只能是一行：标准输入里后面还有别的内容，没有发请求"}
	}
	return line, nil
}

// readLine 读一行；最后一行没有换行（管道结尾）也算读到了。
func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err == nil {
		return line, nil
	}
	if line != "" {
		return line, nil
	}
	if errors.Is(err, io.EOF) {
		return "", errNoInput
	}
	return "", fmt.Errorf("读标准输入出错：%w", err)
}
