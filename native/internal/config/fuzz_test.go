package config

// 性质测试：配置的读→写→读往返。写路径是设置页用的 PatchLink（把 linkReader.expand.enabled 改成给定值），
// 不是整份 Config 序列化；所以这里测两件事：同一份 YAML 读两次结构相同；解析成功的 YAML 走补丁后仍能解析、
// 补丁键生效，成功或报清楚的错，绝不 panic。

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// FuzzConfigParsePatch 覆盖 Parse 和 PatchLink 的往返边界。
func FuzzConfigParsePatch(f *testing.F) {
	seeds := [][]byte{
		[]byte(""),
		[]byte("console: { port: 7999 }\n"),
		[]byte("linkReader:\n  expand:\n    enabled: false\n    maxLinks: 3\n"),
		[]byte("characters:\n  a: { model: m-a, groupReminder: true }\n"),
		[]byte("memory:\n  write:\n    characters:\n"),
		[]byte("console: [1]\n"),
		[]byte("a: 1\n---\nb: 2\n"),
		[]byte(strings.Repeat("# 注释\nother: {}\n", 200)),
	}
	for _, b := range seeds {
		f.Add(b, false)
		f.Add(b, true)
	}
	f.Fuzz(func(t *testing.T, raw []byte, on bool) {
		orig := append([]byte(nil), raw...)
		c1, err1 := Parse(raw, "fuzz.yml")
		c2, err2 := Parse(raw, "fuzz.yml")
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("同一份 YAML 两次解析结论不同：%v / %v\n%s", err1, err2, raw)
		}
		if err1 == nil && !reflect.DeepEqual(c1, c2) {
			t.Fatalf("同一份 YAML 两次解析结构不同：\n%+v\n%+v\n%s", c1, c2, raw)
		}
		if err1 != nil && err1.Error() == "" {
			t.Fatalf("解析失败却没有错误文字：%q", raw)
		}
		if err1 != nil {
			return
		}

		out, c3, err := PatchLink(raw, "fuzz.yml", LinkChange{Key: LinkEnabled, Bool: on})
		if err != nil {
			if err.Error() == "" {
				t.Fatalf("补丁失败却没有错误文字：%q", raw)
			}
			return
		}
		if !bytes.Equal(raw, orig) {
			t.Fatalf("PatchLink 改了输入字节：\n前 %q\n后 %q", orig, raw)
		}
		c4, err := Parse(out, "fuzz.yml")
		if err != nil {
			t.Fatalf("补丁写出来的 YAML 解析不回来：%v\n%s", err, out)
		}
		if !reflect.DeepEqual(c3, c4) {
			t.Fatalf("PatchLink 返回的配置和重新解析的不一样：\n%+v\n%+v\n%s", c3, c4, out)
		}
		if c4.LinkReader.Expand.Enabled != on {
			t.Fatalf("补丁没生效：要 %v，得到 %v\n%s", on, c4.LinkReader.Expand.Enabled, out)
		}
	})
}
