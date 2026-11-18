package api

import (
	"fmt"
	"strconv"
	"strings"
)

// Display 把设置里的一个值写成给主人看的中文：开 / 关、取值的叫法（Choices）、列表用顿号连起来、
// 数字带单位、空的写"没填"。没登记叫法的取值原样显示，不吞掉。
// v 可以是 Value，也可以是 EffectiveValue（运行中的值用同一套写法）。
func (s Setting) Display(v any) string {
	choose := func(raw string) string {
		if c, ok := s.Choices[raw]; ok {
			return c
		}
		return raw
	}
	switch x := v.(type) {
	case nil:
		return "没填"
	case string:
		if x == "" {
			return "没填"
		}
		return choose(x)
	case []string:
		if len(x) == 0 {
			return "没填"
		}
		out := make([]string, len(x))
		for i, item := range x {
			out[i] = choose(item)
		}
		return strings.Join(out, "、")
	case bool:
		if x {
			return "开"
		}
		return "关"
	case int:
		return s.number(strconv.Itoa(x))
	case int64:
		return s.number(strconv.FormatInt(x, 10))
	case float64:
		return s.number(strconv.FormatFloat(x, 'f', -1, 64))
	}
	return fmt.Sprint(v)
}

// number 先看数字本身有没有叫法（0 = 跟着模型），没有再带上单位
func (s Setting) number(text string) string {
	if c, ok := s.Choices[text]; ok {
		return c
	}
	if s.Unit != "" {
		return text + " " + s.Unit
	}
	return text
}

// SourceText 这一项的值是从哪来的：你写在配置里的、没写所以用默认的、还是程序当前的状态。
// 配置文件的完整路径不在这里（Source 里有），界面放进悬停提示。
func (s Setting) SourceText() string {
	switch {
	case s.Source == "runtime":
		return "程序当前状态"
	case s.Configured:
		return "你的配置"
	}
	return "默认值"
}

// SourcePath 配置文件的完整路径；不是来自配置文件（默认值、程序状态）时为空
func (s Setting) SourcePath() string {
	if !s.Configured || s.Source == "" || s.Source == "config" || s.Source == "runtime" || s.Source == "default" {
		return ""
	}
	return s.Source
}
