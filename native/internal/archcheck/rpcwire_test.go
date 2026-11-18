package archcheck

import (
	"strings"
	"testing"
)

// 界面协议的线上类型包（规格-Bun后台第1批 4.4）不引项目里的别的包：协议客户端可以只链接它，
// 生成文件也不会因为别的包改名而跟着变。
func TestRPCWireHasNoProjectDeps(t *testing.T) {
	const self = "sumika-native/internal/rpcwire"
	saw := false
	for _, d := range depsOf(t, "./internal/rpcwire") {
		switch {
		case d == self:
			saw = true
		case strings.HasPrefix(d, "sumika-native/"):
			t.Errorf("%s 依赖了 %s：线上类型包只许用标准库", self, d)
		}
	}
	if !saw {
		t.Fatalf("go list 的结果里没有 %s：改名或搬家了就把这个测试一起改", self)
	}
}
