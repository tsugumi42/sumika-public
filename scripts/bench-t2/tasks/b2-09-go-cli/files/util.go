package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// newFlags 建一个出错时返回 error、不往屏幕打印用法的 FlagSet
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// readInput 取输入：fs 解析完以后有文件参数就读第一个文件，没有就读 in
func readInput(fs *flag.FlagSet, in io.Reader) ([]byte, error) {
	if fs.NArg() > 0 {
		return os.ReadFile(fs.Arg(0))
	}
	return io.ReadAll(in)
}

// loadStopwords 读停用词文件：一行一个词，去掉空行和首尾空白，统一小写
func loadStopwords(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	words := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if w := strings.ToLower(strings.TrimSpace(sc.Text())); w != "" {
			words[w] = true
		}
	}
	return words, sc.Err()
}

// formatCount 把次数和一行文字拼成“次数在前、右对齐 4 位”的一行（不带换行）
func formatCount(n int, line string) string {
	return fmt.Sprintf("%4d %s", n, line)
}
