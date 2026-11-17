package spike

import (
	"testing"

	"golang.org/x/sys/windows"
)

// 旧写法 a<<32|b+c<<32|d 因为 | 和 + 同级、从左往右算，得到的不是两个 FILETIME 的和。
// 第一组只有低位：旧写法得 0x0FFF0FFF（两个低位按位或在一起），正确的和是 0x100E100E。
func TestFiletimeSum(t *testing.T) {
	k := windows.Filetime{HighDateTime: 0, LowDateTime: 0x0F0F0F0F}
	u := windows.Filetime{HighDateTime: 0, LowDateTime: 0x00FF00FF}
	if got, want := filetimeSum(k, u), uint64(0x100E100E); got != want {
		t.Fatalf("低位相加：得 %d，应为 %d", got, want)
	}
	// 高位也有值：一个 FILETIME 的高位不能和另一个的低位混在一起
	k = windows.Filetime{HighDateTime: 1, LowDateTime: 0xFFFFFFFF}
	u = windows.Filetime{HighDateTime: 2, LowDateTime: 5}
	want := uint64(1)<<32 + 0xFFFFFFFF + uint64(2)<<32 + 5
	if got := filetimeSum(k, u); got != want {
		t.Fatalf("带高位：得 %d，应为 %d", got, want)
	}
}
