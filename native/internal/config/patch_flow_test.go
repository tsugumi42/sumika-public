package config

import "testing"

// 只有 CR 换行的空 flow 映射：goccy 的 End 行号会和 byteAt 的 \n 定位对不上，补丁曾在这里越界 panic（fuzz 输入 "{\r}"）。
func TestPatchFlowMapOnlyCR(t *testing.T) {
	out, c, err := PatchLink([]byte("{\r}"), "sumika.yml", LinkChange{Key: LinkEnabled, Bool: true})
	if err != nil {
		t.Fatal(err)
	}
	if !c.LinkReader.Expand.Enabled {
		t.Fatalf("补丁没生效：%+v\n%s", c.LinkReader.Expand, out)
	}
	again, err := Parse(out, "sumika.yml")
	if err != nil || !again.LinkReader.Expand.Enabled {
		t.Fatalf("补丁写出来的 YAML 解析不回来：%v\n%s", err, out)
	}
}
