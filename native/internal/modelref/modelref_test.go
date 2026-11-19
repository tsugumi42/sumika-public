package modelref

import (
	"errors"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	r := New("magpie", "magpie", "deepseek", "off")
	for _, c := range []struct {
		name, in string
		want     Ref
		err      string
	}{
		{"规范形式", "deepseek/chat", Ref{"deepseek", "chat"}, ""},
		{"裸模型 ID 归默认", "chat", Ref{"magpie", "chat"}, ""},
		{"裸 ID 自己带斜杠（Magpie 的 group/auto-xxx）整串归默认", "group/auto-x", Ref{"magpie", "group/auto-x"}, ""},
		{"带前缀、模型 ID 里还有斜杠：只按第一个斜杠切", "magpie/group/auto-x", Ref{"magpie", "group/auto-x"}, ""},
		{"多个斜杠、首段没声明", "a/b/c", Ref{"magpie", "a/b/c"}, ""},
		{"首段是没声明的服务商：不当服务商，不报错，整串归默认", "openai/gpt", Ref{"magpie", "openai/gpt"}, ""},
		{"首段含非法字符（大写、空格、# :）当裸 ID", "Bad ID/x", Ref{"magpie", "Bad ID/x"}, ""},
		{"首段含冒号", "a:b/x", Ref{"magpie", "a:b/x"}, ""},
		{"首段大小写不同于已声明的 ID", "DeepSeek/x", Ref{"magpie", "DeepSeek/x"}, ""},
		{"停用的服务商照样解析（请求时才报错）", "off/m", Ref{"off", "m"}, ""},
		{"服务商后面没写模型", "deepseek/", Ref{}, "后面没写模型"},
		{"只有一个斜杠", "/x", Ref{"magpie", "/x"}, ""},
		{"空串", "", Ref{}, "没选模型"},
		{"空格不去掉", " chat", Ref{"magpie", " chat"}, ""},
	} {
		got, err := r.Parse(c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s：Parse(%q) 的错 = %v，想要含 %q", c.name, c.in, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s：Parse(%q) = %+v, %v，想要 %+v", c.name, c.in, got, err, c.want)
		}
	}
	if _, err := r.Parse(""); !errors.Is(err, ErrEmpty) {
		t.Errorf("空串的错要能用 errors.Is 认出 ErrEmpty：%v", err)
	}
}

// 没声明任何服务商的零值：所有字符串原样当模型 ID，和改动前一样
func TestZeroResolverIsIdentity(t *testing.T) {
	var r Resolver
	for _, s := range []string{"chat", "group/auto-x", "magpie/x"} {
		ref, err := r.Parse(s)
		if err != nil || ref.Model != s || ref.Provider != "" || ref.String() != s || r.Canonical(s) != s {
			t.Errorf("零值解析 %q = %+v, %v", s, ref, err)
		}
	}
}

func TestCanonicalIsStable(t *testing.T) {
	r := New("magpie", "magpie", "deepseek")
	for in, want := range map[string]string{
		"chat":               "magpie/chat",
		"magpie/chat":        "magpie/chat",
		"group/auto-x":       "magpie/group/auto-x",
		"deepseek/chat":      "deepseek/chat",
		"":                   "",
		"deepseek/":          "deepseek/",
		"magpie/magpie/chat": "magpie/magpie/chat",
		"openai/chat":        "magpie/openai/chat",
	} {
		got := r.Canonical(in)
		if got != want {
			t.Errorf("Canonical(%q) = %q，想要 %q", in, got, want)
		}
		if again := r.Canonical(got); again != got {
			t.Errorf("Canonical 不是幂等的：%q → %q → %q", in, got, again)
		}
	}
}
