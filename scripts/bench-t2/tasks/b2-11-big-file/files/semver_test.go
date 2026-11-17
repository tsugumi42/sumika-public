package semver

import (
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, s string) Version {
	t.Helper()
	v, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q) 出错：%v", s, err)
	}
	return v
}

func TestParseOK(t *testing.T) {
	cases := []struct {
		in    string
		want  Version
		print string
	}{
		{"1.2.3", Version{Major: 1, Minor: 2, Patch: 3}, "1.2.3"},
		{"v10.0.7", Version{Major: 10, Patch: 7}, "10.0.7"},
		{"0.0.0", Version{}, "0.0.0"},
		{"1.0.0-rc.1", Version{Major: 1, Pre: []string{"rc", "1"}}, "1.0.0-rc.1"},
		{"1.0.0-alpha-1.0", Version{Major: 1, Pre: []string{"alpha-1", "0"}}, "1.0.0-alpha-1.0"},
		{"2.1.0+exp.sha.5114f85", Version{Major: 2, Minor: 1, Build: "exp.sha.5114f85"}, "2.1.0+exp.sha.5114f85"},
		{"1.0.0-beta+001", Version{Major: 1, Pre: []string{"beta"}, Build: "001"}, "1.0.0-beta+001"},
		{"18446744073709551615.0.0", Version{Major: 18446744073709551615}, "18446744073709551615.0.0"},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q) 出错：%v", c.in, err)
			continue
		}
		if got.Major != c.want.Major || got.Minor != c.want.Minor || got.Patch != c.want.Patch || got.Build != c.want.Build || !(len(got.Pre) == 0 && len(c.want.Pre) == 0 || reflect.DeepEqual(got.Pre, c.want.Pre)) {
			t.Errorf("Parse(%q) = %+v，想要 %+v", c.in, got, c.want)
		}
		if s := got.String(); s != c.print {
			t.Errorf("Parse(%q).String() = %q，想要 %q", c.in, s, c.print)
		}
	}
}

func TestParseBad(t *testing.T) {
	for _, in := range []string{
		"", "1", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.03", "1.2.x", "a.b.c", "-1.2.3", "1.-2.3",
		" 1.2.3", "1.2.3 ", "V1.2.3", "vv1.2.3", "1.2.3-", "1.2.3-.a", "1.2.3-a..b", "1.2.3-01", "1.2.3-a_b",
		"1.2.3+", "1.2.3+a..b", "1.2.3+a_b", "18446744073709551616.0.0", "1.2.3-rc.1+", "1.2.3+build-1+2",
	} {
		v, err := Parse(in)
		if err == nil {
			t.Errorf("Parse(%q) 应该出错，得到 %+v", in, v)
		} else if !strings.Contains(err.Error(), in) && in != "" {
			t.Errorf("Parse(%q) 的错误信息要带上原输入，得到 %q", in, err)
		}
	}
}

func TestCompare(t *testing.T) {
	// 每组按优先级从低到高排好；组内相邻两个互相比较，每个和自己比较
	order := []string{
		"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11",
		"1.0.0-rc.1", "1.0.0-rc.1.0", "1.0.0", "1.0.1", "1.1.0", "1.10.0", "2.0.0", "10.0.0",
	}
	for i := range order {
		a := mustParse(t, order[i])
		if c := a.Compare(a); c != 0 {
			t.Errorf("%s 和自己比得到 %d", order[i], c)
		}
		for j := i + 1; j < len(order); j++ {
			b := mustParse(t, order[j])
			if c := a.Compare(b); c != -1 {
				t.Errorf("%s.Compare(%s) = %d，想要 -1", order[i], order[j], c)
			}
			if c := b.Compare(a); c != 1 {
				t.Errorf("%s.Compare(%s) = %d，想要 1", order[j], order[i], c)
			}
		}
	}
	if c := mustParse(t, "1.2.3+a").Compare(mustParse(t, "1.2.3+b")); c != 0 {
		t.Errorf("构建元数据不该参与比较，得到 %d", c)
	}
	if c := mustParse(t, "1.0.0-1").Compare(mustParse(t, "1.0.0-a")); c != -1 {
		t.Errorf("纯数字标识要比字母标识小，得到 %d", c)
	}
	if c := mustParse(t, "1.0.0-Z").Compare(mustParse(t, "1.0.0-a")); c != -1 {
		t.Errorf("字母标识按 ASCII 比（大写在小写前），得到 %d", c)
	}
}

func TestBump(t *testing.T) {
	cases := []struct{ in, part, want string }{
		{"1.2.3", "major", "2.0.0"},
		{"1.2.3-rc.1+b5", "major", "2.0.0"},
		{"1.2.3", "minor", "1.3.0"},
		{"1.2.3-rc.1", "minor", "1.3.0"},
		{"1.2.3", "patch", "1.2.4"},
		{"1.2.3+meta", "patch", "1.2.4"},
		{"1.2.3-rc.1", "patch", "1.2.3"},
		{"1.2.3-rc.1+meta", "patch", "1.2.3"},
		{"1.2.3", "prerelease", "1.2.4-rc.1"},
		{"1.2.3+meta", "prerelease", "1.2.4-rc.1"},
		{"1.2.4-rc.1", "prerelease", "1.2.4-rc.2"},
		{"1.2.4-rc.9+x", "prerelease", "1.2.4-rc.10"},
		{"1.2.4-beta", "prerelease", "1.2.4-beta.1"},
		{"1.2.4-3", "prerelease", "1.2.4-4"},
	}
	for _, c := range cases {
		v := mustParse(t, c.in)
		before := v.String()
		got, err := v.Bump(c.part)
		if err != nil {
			t.Errorf("%s.Bump(%q) 出错：%v", c.in, c.part, err)
			continue
		}
		if got.String() != c.want {
			t.Errorf("%s.Bump(%q) = %s，想要 %s", c.in, c.part, got, c.want)
		}
		if v.String() != before {
			t.Errorf("%s.Bump(%q) 改动了原值：%s", c.in, c.part, v)
		}
	}
	for _, part := range []string{"", "Major", "build", "pre"} {
		if _, err := mustParse(t, "1.0.0").Bump(part); err == nil {
			t.Errorf("Bump(%q) 应该出错", part)
		}
	}
	// 递增不能改到原版本共用的预发布切片
	orig := mustParse(t, "1.0.0-rc.1")
	_, _ = orig.Bump("prerelease")
	if !reflect.DeepEqual(orig.Pre, []string{"rc", "1"}) {
		t.Errorf("Bump 改到了原值的预发布：%v", orig.Pre)
	}
}

func TestConstraintBad(t *testing.T) {
	for _, in := range []string{
		"||", "1.2.3 ||", "|| 1.2.3", "1.2.3 || || 2.0.0", "!=1.2", "!=1.x", "!=*", ">=", ">= ", "~", "^ ",
		"1.2.3.4", "1.x.3", "1.2.3-beta.1 - 2.0.0 || ", ">=1.2.3 - 2.0.0", "1.2-beta", "1.2.x-beta", "01.2.3", "a.b.c",
		"=>1.2.3", "~>1.2.3", ">>1.2.3", "1.2.3 -", "- 1.2.3", "1.2.3 - 2.0.0 - 3.0.0", "^1.2.3 - 2.0.0", "1.2.3 - >2.0.0",
	} {
		if c, err := ParseConstraint(in); err == nil {
			t.Errorf("ParseConstraint(%q) 应该出错，得到 %+v", in, c)
		}
	}
}

func TestConstraintCheck(t *testing.T) {
	cases := []struct {
		rng string
		yes []string
		no  []string
	}{
		{"", []string{"0.0.0", "1.2.3", "99.0.0"}, []string{"1.0.0-rc.1"}},
		{"  ", []string{"1.2.3"}, nil},
		{"*", []string{"0.0.1", "7.7.7"}, []string{"7.7.7-beta"}},
		{"x", []string{"1.0.0"}, nil},
		{"1.2.3", []string{"1.2.3", "v1.2.3+meta"}, []string{"1.2.4", "1.2.2", "1.2.3-rc.1"}},
		{"=1.2.3", []string{"1.2.3"}, []string{"1.2.4"}},
		{"v1.2.3", []string{"1.2.3"}, []string{"1.2.4"}},
		{"!=1.2.3", []string{"1.2.4", "1.2.2", "0.0.1"}, []string{"1.2.3", "1.2.3+b"}},
		{">1.2.3", []string{"1.2.4", "2.0.0"}, []string{"1.2.3", "1.0.0"}},
		{">=1.2.3", []string{"1.2.3", "1.2.4", "9.0.0"}, []string{"1.2.2", "0.9.9"}},
		{"<1.2.3", []string{"1.2.2", "0.0.1"}, []string{"1.2.3", "2.0.0"}},
		{"<=1.2.3", []string{"1.2.3", "1.0.0"}, []string{"1.2.4"}},
		{">= 1.2.3", []string{"1.2.3"}, []string{"1.2.2"}},
		{">= 1.2.3 < 2.0.0", []string{"1.2.3", "1.9.9"}, []string{"2.0.0", "1.2.2"}},
		{">=1.2.3 <2.0.0 !=1.5.0", []string{"1.4.0", "1.5.1"}, []string{"1.5.0", "2.0.0"}},
		{"1", []string{"1.0.0", "1.9.9"}, []string{"0.9.9", "2.0.0"}},
		{"1.2", []string{"1.2.0", "1.2.9"}, []string{"1.1.9", "1.3.0"}},
		{"1.x", []string{"1.0.0", "1.9.9"}, []string{"2.0.0", "0.1.0"}},
		{"1.2.*", []string{"1.2.0", "1.2.99"}, []string{"1.3.0"}},
		{"1.X.x", []string{"1.4.0"}, []string{"2.0.0"}},
		{">1.2", []string{"1.3.0", "2.0.0"}, []string{"1.2.9", "1.2.0"}},
		{">1", []string{"2.0.0"}, []string{"1.9.9"}},
		{">=1.2", []string{"1.2.0", "1.3.0"}, []string{"1.1.9"}},
		{"<1.2", []string{"1.1.9"}, []string{"1.2.0"}},
		{"<=1.2", []string{"1.2.9", "1.0.0"}, []string{"1.3.0"}},
		{"<=1", []string{"1.9.9"}, []string{"2.0.0"}},
		{"~1.2.3", []string{"1.2.3", "1.2.9"}, []string{"1.2.2", "1.3.0"}},
		{"~1.2", []string{"1.2.0", "1.2.9"}, []string{"1.3.0", "1.1.9"}},
		{"~1", []string{"1.0.0", "1.9.9"}, []string{"2.0.0", "0.9.9"}},
		{"~0.2.3", []string{"0.2.3", "0.2.9"}, []string{"0.3.0"}},
		{"^1.2.3", []string{"1.2.3", "1.9.9"}, []string{"1.2.2", "2.0.0"}},
		{"^0.2.3", []string{"0.2.3", "0.2.9"}, []string{"0.3.0", "0.2.2"}},
		{"^0.0.3", []string{"0.0.3"}, []string{"0.0.4", "0.0.2"}},
		{"^0.0", []string{"0.0.0", "0.0.9"}, []string{"0.1.0"}},
		{"^0", []string{"0.0.0", "0.9.9"}, []string{"1.0.0"}},
		{"^1.x", []string{"1.0.0", "1.9.9"}, []string{"2.0.0"}},
		{"^0.0.0", []string{"0.0.0"}, []string{"0.0.1"}},
		{"^ 1.2.3", []string{"1.5.0"}, []string{"2.0.0"}},
		{"1.2.3 - 2.3.4", []string{"1.2.3", "2.0.0", "2.3.4"}, []string{"1.2.2", "2.3.5"}},
		{"1.2 - 2.3", []string{"1.2.0", "2.3.9"}, []string{"1.1.9", "2.4.0"}},
		{"1 - 2", []string{"1.0.0", "2.9.9"}, []string{"0.9.9", "3.0.0"}},
		{"1.2.3 - 2", []string{"1.2.3", "2.9.9"}, []string{"3.0.0"}},
		{"^1.2.3 || ^3.0.0", []string{"1.5.0", "3.1.0"}, []string{"2.5.0", "4.0.0"}},
		{"<1.0.0 || >=2.0.0 <2.5.0 || 4.x", []string{"0.5.0", "2.4.9", "4.1.1"}, []string{"1.0.0", "2.5.0", "3.0.0", "5.0.0"}},
		{"1.2.3 - 2.0.0 || 5.0.0", []string{"1.5.0", "5.0.0"}, []string{"3.0.0"}},
		// 预发布规则
		{"^1.2.0", []string{"1.2.0", "1.3.0"}, []string{"1.2.3-beta.2", "1.3.0-rc.1"}},
		{">=1.2.3-beta.1", []string{"1.2.3-beta.2", "1.2.3", "1.3.0"}, []string{"1.2.3-alpha", "1.2.3-beta", "1.3.0-rc.1"}},
		{">=1.2.3-beta.1 <2.0.0", []string{"1.2.3-beta.5", "1.9.0"}, []string{"1.2.4-beta.1", "2.0.0-rc.1"}},
		{"<2.0.0-rc.1", []string{"1.9.9", "2.0.0-beta"}, []string{"2.0.0-rc.1", "2.0.0", "1.9.9-beta"}},
		{"1.2.3-beta.2", []string{"1.2.3-beta.2"}, []string{"1.2.3-beta.3", "1.2.3"}},
		{"!=1.2.3-beta.2", []string{"1.2.3-beta.3", "1.2.3"}, []string{"1.2.3-beta.2"}},
		{"^1.2.3-beta.2 || >=3.0.0", []string{"1.2.3-beta.4", "1.4.0", "3.0.0"}, []string{"1.2.3-beta.1", "1.4.0-rc.1", "2.0.0"}},
		{"1.2.3-beta.1 - 1.2.3", []string{"1.2.3-beta.2", "1.2.3"}, []string{"1.2.3-alpha", "1.2.4-beta.1"}},
		// 构建部分解析后忽略
		{">=1.2.3+build.5", []string{"1.2.3", "1.2.4"}, []string{"1.2.2"}},
	}
	for _, c := range cases {
		cons, err := ParseConstraint(c.rng)
		if err != nil {
			t.Errorf("ParseConstraint(%q) 出错：%v", c.rng, err)
			continue
		}
		for _, s := range c.yes {
			if !cons.Check(mustParse(t, s)) {
				t.Errorf("范围 %q 应该满足 %s", c.rng, s)
			}
		}
		for _, s := range c.no {
			if cons.Check(mustParse(t, s)) {
				t.Errorf("范围 %q 不该满足 %s", c.rng, s)
			}
		}
	}
}

func TestSort(t *testing.T) {
	in := []string{"1.10.0", "1.2.0", "1.0.0-rc.1", "1.0.0", "1.0.0-alpha", "2.0.0+b", "2.0.0+a", "0.1.0"}
	want := []string{"0.1.0", "1.0.0-alpha", "1.0.0-rc.1", "1.0.0", "1.2.0", "1.10.0", "2.0.0+b", "2.0.0+a"}
	var vs []Version
	for _, s := range in {
		vs = append(vs, mustParse(t, s))
	}
	Sort(vs)
	var got []string
	for _, v := range vs {
		got = append(got, v.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sort 得到 %v，想要 %v（相等的要保持原来的先后）", got, want)
	}
	Sort(nil)
	Sort([]Version{})
}

func TestMaxSatisfying(t *testing.T) {
	var vs []Version
	for _, s := range []string{"1.2.0", "1.9.0+first", "1.9.0+second", "2.0.0-rc.1", "2.0.0", "0.5.0", "1.10.0-beta.1"} {
		vs = append(vs, mustParse(t, s))
	}
	cases := []struct {
		rng, want string
		ok        bool
	}{
		{"^1.2.0", "1.9.0+first", true},
		{"~1.2", "1.2.0", true},
		{"*", "2.0.0", true},
		{"<2.0.0", "1.9.0+first", true},
		{">=2.0.0-rc.1 <2.0.0", "2.0.0-rc.1", true},
		{">=1.10.0-beta.1", "2.0.0", true},
		{"^3.0.0", "", false},
		{"0.5.0 || 3.0.0", "0.5.0", true},
	}
	for _, c := range cases {
		cons, err := ParseConstraint(c.rng)
		if err != nil {
			t.Fatalf("ParseConstraint(%q)：%v", c.rng, err)
		}
		got, ok := MaxSatisfying(vs, cons)
		if ok != c.ok {
			t.Errorf("MaxSatisfying(%q) 的 ok = %v，想要 %v", c.rng, ok, c.ok)
			continue
		}
		if ok && got.String() != c.want {
			t.Errorf("MaxSatisfying(%q) = %s，想要 %s", c.rng, got, c.want)
		}
		if !ok && !reflect.DeepEqual(got, Version{}) && got.String() != "0.0.0" {
			t.Errorf("没有满足的时候要返回零值，得到 %+v", got)
		}
	}
	cons, _ := ParseConstraint("*")
	if _, ok := MaxSatisfying(nil, cons); ok {
		t.Errorf("空切片不该有结果")
	}
}
