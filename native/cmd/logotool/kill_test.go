package main

import (
	"strings"
	"testing"
)

// work 为空或全空白时不能去结束进程：PowerShell 里拿空串做 Contains 恒真，会把本机所有进程都当成"本次拉起的 Edge"
func TestKillByWorkDirRefusesBlank(t *testing.T) {
	var ran []string
	old := runKillScript
	runKillScript = func(script string) { ran = append(ran, script) }
	t.Cleanup(func() { runKillScript = old })

	for _, w := range []string{"", " ", "\t\r\n", "   \t"} {
		killByWorkDir(w)
		if len(ran) != 0 {
			t.Fatalf("work=%q 不该执行结束脚本：%v", w, ran)
		}
	}
	killByWorkDir(`C:\Temp\logo-123456`)
	if len(ran) != 1 || !strings.Contains(ran[0], `C:\Temp\logo-123456`) {
		t.Fatalf("正常的工作目录该执行一次脚本：%v", ran)
	}
}
