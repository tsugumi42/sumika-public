package jstext

import "testing"

func TestDecodeLossyMatchesWHATWG(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"abc中", "abc中"},
		{"a\xffb", "a\ufffdb"},
		{"\xe4\xb8", "\ufffd"},                 // 被截断的三字节序列：一个 U+FFFD
		{"\xe4\xb8x", "\ufffdx"},               // 截断后接 ASCII
		{"\xe0\x80\x80", "\ufffd\ufffd\ufffd"}, // E0 后第一个续字节必须 >= A0：三个
		{"\xed\xa0\x80", "\ufffd\ufffd\ufffd"}, // 代理区：三个
		{"\xf0\x90\x80", "\ufffd"},             // 四字节被截断：一个
		{"\xc0\xaf", "\ufffd\ufffd"},           // C0 不是合法 lead
		{"\x80\x80", "\ufffd\ufffd"},
		{"\xf4\x90\x80\x80", "\ufffd\ufffd\ufffd\ufffd"}, // 超出 U+10FFFF
	}
	for _, c := range cases {
		if got := DecodeLossy([]byte(c.in)); got != c.want {
			t.Errorf("%q：得到 %q，要 %q", c.in, got, c.want)
		}
	}
}

func TestTrimUsesJSSpace(t *testing.T) {
	if got := Trim("\ufeff \u00a0x\u3000\n"); got != "x" {
		t.Fatalf("Trim：%q", got)
	}
	if got := TrimEnd(" x\u2028\r"); got != " x" {
		t.Fatalf("TrimEnd：%q", got)
	}
}
