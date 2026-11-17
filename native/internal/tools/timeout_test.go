package tools

import (
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
)

// 规格第七节 T14：命令自己的超时（没写补 120 秒，最多 600 秒）要比外面套的总超时（630 秒）短，不然外面那句"超时了"先到，
// 她看不到命令自己的输出和"Command timed out"（Bun 的 spec.ts 也是这样留的余量）。审查查出原来只比了两个常数，封顶去掉也照过
func TestShellTimeoutBelowWrapper(t *testing.T) {
	outer := float64(policy.Specs["powershell"].TimeoutMs) / 1000
	for _, c := range []struct {
		args string
		want float64
	}{{`{}`, 120}, {`{"timeout":9999}`, 600}, {`{"timeout":30}`, 30}} {
		args, err := jsjson.Parse(c.args)
		if err != nil {
			t.Fatal(err)
		}
		if got := shellTimeout(args); got != c.want || got >= outer {
			t.Fatalf("%s：命令自己的超时是 %v 秒，应该是 %v 秒、比总超时 %v 秒短", c.args, got, c.want, outer)
		}
	}
}
