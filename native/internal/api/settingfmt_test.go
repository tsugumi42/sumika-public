package api

import "testing"

func TestSettingDisplay(t *testing.T) {
	think := map[string]string{"low": "低", "medium": "中", "high": "高"}
	cases := []struct {
		name string
		s    Setting
		v    any
		want string
	}{
		{"开", Setting{}, true, "开"},
		{"关不是没填", Setting{}, false, "关"},
		{"思考档位", Setting{Choices: think}, "medium", "中"},
		{"没登记的取值原样显示", Setting{Choices: think}, "turbo", "turbo"},
		{"空字符串", Setting{}, "", "没填"},
		{"空列表", Setting{}, []string{}, "没填"},
		{"nil", Setting{}, nil, "没填"},
		{"列表用顿号", Setting{}, []string{"甲", "乙"}, "甲、乙"},
		{"列表里的取值也翻", Setting{Choices: map[string]string{"fact": "事实", "promise": "承诺"}}, []string{"fact", "promise", "x"}, "事实、承诺、x"},
		{"整数带单位", Setting{Unit: "token"}, 20000, "20000 token"},
		{"小数带单位", Setting{Unit: "秒"}, 5.0, "5 秒"},
		{"小数不带单位", Setting{}, 0.85, "0.85"},
		{"0 是个数不是没填", Setting{Unit: "分钟"}, 0, "0 分钟"},
		{"0 有叫法时用叫法", Setting{Unit: "token", Choices: map[string]string{"0": "跟着模型"}}, 0, "跟着模型"},
		{"int64", Setting{Unit: "条"}, int64(40), "40 条"},
	}
	for _, c := range cases {
		if got := c.s.Display(c.v); got != c.want {
			t.Errorf("%s：%q，应该是 %q", c.name, got, c.want)
		}
	}
}

func TestSettingSource(t *testing.T) {
	cases := []struct {
		name       string
		s          Setting
		text, path string
	}{
		{"写在配置里", Setting{Source: `D:\x\sumika.yml`, Configured: true}, "你的配置", `D:\x\sumika.yml`},
		{"没写用默认", Setting{Source: "default"}, "默认值", ""},
		{"程序状态", Setting{Source: "runtime"}, "程序当前状态", ""},
		{"没有路径的配置来源", Setting{Source: "config", Configured: true}, "你的配置", ""},
	}
	for _, c := range cases {
		if c.s.SourceText() != c.text || c.s.SourcePath() != c.path {
			t.Errorf("%s：%q %q", c.name, c.s.SourceText(), c.s.SourcePath())
		}
	}
}
