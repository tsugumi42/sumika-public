package config

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestLinkFakeIPRanges(t *testing.T) {
	d := DefaultLinks()
	if len(d.FakeIPRanges) != 3 || d.FakeIPRanges[0] != "198.18.0.0/15" || d.FakeIPRanges[1] != "2001:2::/48" || d.FakeIPRanges[2] != "fdfe:dcba:9876::/48" || len(d.check()) != 0 {
		t.Fatalf("默认值：%v %v", d.FakeIPRanges, d.check())
	}
	// 改默认值的副本不能改到包级默认
	d.FakeIPRanges[0] = "10.0.0.0/8"
	if DefaultLinks().FakeIPRanges[0] != "198.18.0.0/15" || DefaultFakeIPRanges()[0] != "198.18.0.0/15" {
		t.Fatal("默认值被改了")
	}
	for _, c := range []struct {
		src  string
		want []string
		bad  string
	}{
		{"fakeIPRanges: [\"198.18.0.1/16\"]", []string{"198.18.0.1/16"}, ""},
		{"fakeIPRanges: [\"198.18.0.0/15\", \"fdfe:dcba:9876::/48\"]", []string{"198.18.0.0/15", "fdfe:dcba:9876::/48"}, ""},
		{"fakeIPRanges: []", []string{}, ""},                        // 显式空表：没有假地址段
		{"proxy: http://127.0.0.1:7897", DefaultFakeIPRanges(), ""}, // 没写这个键：保持默认
		{"fakeIPRanges: [\"nope\"]", nil, "fakeIPRanges"},
		{"fakeIPRanges: [\"0.0.0.0/0\"]", nil, "范围太大"},
		{"fakeIPRanges: [\"::/0\"]", nil, "范围太大"},
		{"fakeIPRanges: [\"\"]", nil, "不是 CIDR"},
		{"fakeIPRanges: [\"198.18.0.0\"]", nil, "不是 CIDR"},
		{"fakeIPRanges: [\"::ffff:198.18.0.0/111\"]", nil, "映射"},
		// 配成真私有地址段：校验不过（否则名单内主机碰到真私有地址就不再整次拒绝）
		{"fakeIPRanges: [\"127.0.0.0/8\"]", nil, "重叠"},
		{"fakeIPRanges: [\"198.18.0.0/15\", \"10.0.0.0/8\"]", nil, "重叠"},
		{"fakeIPRanges: [\"192.168.0.0/16\"]", nil, "重叠"},
		{"fakeIPRanges: [\"169.254.0.0/16\"]", nil, "重叠"},
		{"fakeIPRanges: [\"fe80::/16\"]", nil, "重叠"},
		{"fakeIPRanges: [\"::/16\"]", nil, "重叠"},
		{"fakeIPRanges: [\"fd00::/16\"]", nil, "重叠"},
		{"fakeIPRanges: [\"fc00::/18\"]", []string{"fc00::/18"}, ""}, // sing-box 的 inet6 默认段不在真实 ULA 里
	} {
		v := DefaultLinks()
		if err := yaml.Unmarshal([]byte(c.src), &v); err != nil {
			t.Fatal(c.src, err)
		}
		got := strings.Join(v.check(), ";")
		if (c.bad == "") != (got == "") || !strings.Contains(got, c.bad) {
			t.Fatalf("%s：%q", c.src, got)
		}
		if c.bad == "" {
			if v.FakeIPRanges == nil || strings.Join(v.FakeIPRanges, ",") != strings.Join(c.want, ",") {
				t.Fatalf("%s：读成 %#v", c.src, v.FakeIPRanges)
			}
		}
	}
	// 条数上限
	many := make([]string, 17)
	for i := range many {
		many[i] = "198.18.0.0/15"
	}
	v := DefaultLinks()
	v.FakeIPRanges = many
	if len(v.check()) == 0 {
		t.Fatal("17 段该被拒")
	}
	// 类型错误
	// null / 留空和其他列表键一样按类型错误拒绝加载（要默认就别写这个键，要没有假地址段就写 []）
	for _, src := range []string{"fakeIPRanges: \"198.18.0.0/15\"", "fakeIPRanges: [1, 2]", "fakeIPRanges: null", "fakeIPRanges:", "fakeIPRanges: ~"} {
		v := DefaultLinks()
		if err := yaml.Unmarshal([]byte(src), &v); err == nil {
			t.Fatalf("类型错误该报错：%s", src)
		}
	}
}
