//go:build windows

package pool

import (
	"runtime"
	"testing"
)

// ProcessRSS 改成只用标准库的 syscall（不再带进 net）以后，仍要读得对：数在合理范围，并且随着真的碰过的内存涨。
func TestProcessRSSTracksTouchedMemory(t *testing.T) {
	before, err := ProcessRSS()
	if err != nil {
		t.Fatal(err)
	}
	if before < 5<<20 || before > 64<<30 {
		t.Fatalf("测试进程的工作集 %d 字节，不在 5 MiB～64 GiB 里，读错了", before)
	}
	buf := make([]byte, 256<<20)
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = 1 // 每页碰一下，工作集才会涨
	}
	after, err := ProcessRSS()
	runtime.KeepAlive(buf)
	if err != nil {
		t.Fatal(err)
	}
	if after < before+128<<20 {
		t.Errorf("碰了 256 MiB 内存，工作集只从 %d 涨到 %d 字节", before, after)
	}
}
