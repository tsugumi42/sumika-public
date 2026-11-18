package app

import "testing"

// 配置里 backup.minFreeGB: 0 是“不查剩余空间”，换成备份服务的负数；正数换成字节
func TestMinFreeBytes(t *testing.T) {
	if got := minFreeBytes(0); got >= 0 {
		t.Fatalf("0 该换成负数（不查）：%d", got)
	}
	if got := minFreeBytes(-3); got >= 0 {
		t.Fatalf("负数也是不查：%d", got)
	}
	if got := minFreeBytes(5); got != 5<<30 {
		t.Fatalf("5 GB：%d", got)
	}
}
