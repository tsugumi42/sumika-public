package main

import (
	"fmt"
	"io"
	"strings"
)

// countLines 数行数：以 \n 结尾的算一行，最后一行没有换行也算一行，空输入是 0 行
func countLines(data []byte) int {
	n := strings.Count(string(data), "\n")
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

func runLines(args []string, in io.Reader, out io.Writer) error {
	fs := newFlags("lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := readInput(fs, in)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "lines=%d\n", countLines(data))
	return err
}
