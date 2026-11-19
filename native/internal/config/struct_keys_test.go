package config

import (
	"strings"
	"testing"
)

// 要解码成结构体的映射里写了非文字的键：以前整份配置静默回到默认值，现在报错并指出行号和位置
func TestParseRejectsNonStringKeysInStructs(t *testing.T) {
	for _, c := range []struct{ src, where string }{
		{"0: #000000000000\n", "最外层"},             // 性质测试撞出的原始种子
		{"console:\n  port: 7999\n0: x\n", "最外层"}, // 混在正常键中间，console 也会一起丢
		{"console:\n  true: 1\n", "console"},
		{"linkReader:\n  expand:\n    null: 1\n", "linkReader.expand"},
		{"characters:\n  suzune:\n    1.5: x\n", "characters.suzune"}, // map 里的值是结构体：照样查
	} {
		_, err := Parse([]byte(c.src), "t.yml")
		if err == nil || !strings.Contains(err.Error(), c.where) || !strings.Contains(err.Error(), "第 ") {
			t.Fatalf("%q：要报错并指出 %s，得到 %v", c.src, c.where, err)
		}
	}
}

// 要解码成 map 的地方照收，引号写法照收
func TestParseKeepsNonStringKeysInMaps(t *testing.T) {
	for _, src := range []string{
		"linkReader:\n  expand:\n    senders:\n      groups:\n        123456: { mode: all }\n",
		"\"0\": x\nconsole:\n  port: 7999\n",
	} {
		c, err := Parse([]byte(src), "t.yml")
		if err != nil {
			t.Fatalf("%q 不该报错：%v", src, err)
		}
		if strings.Contains(src, "7999") && c.Console.Port != 7999 {
			t.Fatalf("%q：console.port 没读进来：%d", src, c.Console.Port)
		}
	}
}
