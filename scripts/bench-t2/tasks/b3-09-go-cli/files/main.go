package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// command 是所有子命令的统一签名（见 docs/commands.md）
type command func(args []string, in io.Reader, out io.Writer) error

// commands 登记所有子命令：键就是命令行上敲的子命令名
var commands = map[string]command{
	"lines": runLines,
}

func dispatch(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("用法：wordtool <子命令> [参数] [文件]")
	}
	cmd, ok := commands[args[0]]
	if !ok {
		return fmt.Errorf("不认识的子命令 %q", args[0])
	}
	return cmd(args[1:], in, out)
}

func main() {
	if err := dispatch(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "wordtool:", err)
		os.Exit(1)
	}
}
