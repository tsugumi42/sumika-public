//go:build windows

package main

import (
	"io"
	"testing"
	"unicode/utf16"
)

// 中文 Windows 控制台按 UTF-16 宽字符给输入：中文和 emoji（代理对）要原样变成 UTF-8 字符串，
// 不能按本地代码页读成乱码；落单的代理项（不成对的半个 emoji）按非法输入拒掉，不悄悄换成替换字符。
func TestReadHiddenLineReadsConsoleUTF16(t *testing.T) {
	cases := []struct {
		name    string
		units   []uint16
		want    string
		wantErr bool
	}{
		{name: "中文和 emoji", units: utf16.Encode([]rune("主密码-中文-🔐-café")), want: "主密码-中文-🔐-café"},
		{name: "落单的代理项", units: []uint16{'a', 0xD83D}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old := readConsoleUnits
			readConsoleUnits = func(io.Writer) ([]uint16, bool, error) { return c.units, true, nil }
			t.Cleanup(func() { readConsoleUnits = old })

			got, err := readHiddenLine(io.Discard, nil)
			if c.wantErr {
				if err == nil {
					t.Fatalf("应报错，读到 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("读控制台：%v", err)
			}
			if got != c.want {
				t.Fatalf("应原样读回 %q，得到 %q", c.want, got)
			}
		})
	}
}
