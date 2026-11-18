// Package prompts 读提示词、填变量、按"用哪一套"挑场合和风格预设，照 daemon/util/prompts.ts、prompt-store.ts、preset-store.ts 的读路径
// （只读：写、历史版本、新建预设等用到设置页时再搬）。
// 用户在网页里改过的版本（数据目录 config/prompts/）优先，没有就读出厂的（项目的 prompts/）；读的时候不改换行（和 Bun 版一样）
package prompts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sumika-native/internal/textclean"
)

// Section 是固定前缀里的一段（顺序从最稳定到最易变，docs/设计.md「提示词顺序与缓存」）
type Section struct {
	ID     string // 段的名字（core、persona-identity、scene-home……）
	Source string // 来自哪个文件或预设，显示在注入面板里
	Text   string
}

// Store 是提示词从哪读
type Store struct {
	Root    string // 项目根：出厂的在 Root/prompts/
	UserDir string // 数据目录的 config/prompts/；空 = 没有用户版（测试、脚本）
}

var varRE = regexp.MustCompile(`\{\{(\w+)\}\}`) // JS 版不带 u 标志的 \w 也只认 ASCII 字母数字下划线

var crlfRE = regexp.MustCompile(`\r\n?`)

// LF 把换行统一成 \n（\r\n 和单独的 \r 都算）：拼进系统提示的提示词、预设、角色卡都过一遍（Bun 版 util/prompts.ts lf，
// 2026-10-25 主人让统一；以前读进来不改换行，git 签出的出厂提示词是 \r\n，系统提示里两种混着）
func LF(s string) string { return crlfRE.ReplaceAllString(s, "\n") }

// FillVars 先统一换行，再把 {{名字}} 换成变量（没给的换成空），整段去掉首尾空白（JS 的 trim）
func FillVars(raw string, vars map[string]string) string {
	return textclean.Trim(varRE.ReplaceAllStringFunc(LF(raw), func(m string) string { return vars[m[2:len(m)-2]] }))
}

// Read 读提示词原文：用户版优先。用户版存在却读不出来时报错，不退回出厂的（和 prompt-store.ts readPrompt 一样）
func (s Store) Read(name string) (text string, edited bool, err error) {
	if s.UserDir != "" {
		u := filepath.Join(s.UserDir, name+".md")
		if _, e := os.Stat(u); e == nil {
			b, err := os.ReadFile(u)
			return string(b), true, err
		}
	}
	b, err := os.ReadFile(filepath.Join(s.Root, "prompts", name+".md"))
	return string(b), false, err
}

// Render 读提示词 name 并填变量（renderPrompt）
func (s Store) Render(name string, vars map[string]string) (Section, error) {
	raw, edited, err := s.Read(name)
	if err != nil {
		return Section{}, err
	}
	src := "prompts/" + name + ".md"
	if edited {
		src += "（你改过的）"
	}
	return Section{ID: name, Source: src, Text: FillVars(raw, vars)}, nil
}

// Join 把几段拼成系统提示：空段不要，段之间两个换行
func Join(sections []Section) string {
	var parts []string
	for _, s := range sections {
		if s.Text != "" {
			parts = append(parts, s.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// ───────────── 预设（preset-store.ts 的读路径） ─────────────

// DefaultStyle 是没选风格时用的内置风格（出厂是空的）
const DefaultStyle = "all-ages"

type builtin struct{ id, name string }

var builtins = map[string][]builtin{
	"style": {{"all-ages", "全年龄向"}, {"adult", "成人向"}},
	"scene": {{"scene-home", "本地：主窗口私聊"}, {"scene-home-group", "本地：主窗口群聊"}, {"scene-private", "QQ 私聊"}, {"scene-group", "QQ 群聊"}},
}

func isBuiltin(kind, id string) bool {
	for _, b := range builtins[kind] {
		if b.id == id {
			return true
		}
	}
	return false
}

var customIDRE = regexp.MustCompile(`^u[a-z0-9]{6,24}$`)

type customMeta struct {
	ID   string
	Name *string // 没写名字（或者是 null）时，显示名退回 id（`?.name ?? id`）
}

// 一个 JSON 值在 JS 模板字符串里会印成什么：字、数字、真假原样，别的（表、列表）都按 [object Object] 算——只在显示名上用到
func jsText(raw json.RawMessage) *string {
	var v any
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return nil
	}
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case float64, bool:
		s = string(raw)
	default:
		s = "[object Object]"
	}
	return &s
}

// 自定义预设的名字表（presets/index.json，readIndex）。读坏了当成没有：Bun 版会把坏文件改名挪走，这里只读、不动它。
// 和 Bun 版一样只查到"是不是列表"这一层：列表里不是表、id 不是字的那几项认不出来，别的照样用
func (s Store) index() map[string][]customMeta {
	out := map[string][]customMeta{"style": nil, "scene": nil}
	if s.UserDir == "" {
		return out
	}
	b, err := os.ReadFile(filepath.Join(s.UserDir, "presets", "index.json"))
	if err != nil {
		return out
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return out
	}
	for _, k := range []string{"style", "scene"} {
		var list []json.RawMessage
		if json.Unmarshal(raw[k], &list) != nil {
			continue
		}
		for _, item := range list {
			var m map[string]json.RawMessage
			if json.Unmarshal(item, &m) != nil {
				continue
			}
			var id string
			if json.Unmarshal(m["id"], &id) != nil {
				continue
			}
			out[k] = append(out[k], customMeta{ID: id, Name: jsText(m["name"])})
		}
	}
	return out
}

// 预设的显示名；没有这个预设返回 ok=false（listPresets + metaOf；没写名字的退回 id）
func (s Store) presetName(kind, id string) (string, bool) {
	for _, b := range builtins[kind] {
		if b.id == id {
			return b.name, true
		}
	}
	for _, c := range s.index()[kind] {
		if c.ID == id {
			if c.Name == nil {
				return id, true
			}
			return *c.Name, true
		}
	}
	return "", false
}

// 预设的正文：内置场合读同名提示词（用户版优先）；内置风格读用户版，没有是空；自定义读自己的文件，文件没了是空
func (s Store) presetText(kind, id string) (string, error) {
	if isBuiltin(kind, id) {
		if kind == "scene" {
			t, _, err := s.Read(id)
			return t, err
		}
		if s.UserDir == "" {
			return "", nil
		}
		b, err := os.ReadFile(filepath.Join(s.UserDir, "presets", "builtin", kind, id+".md"))
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return string(b), err
	}
	if _, ok := s.presetName(kind, id); !ok || !customIDRE.MatchString(id) {
		return "", fmt.Errorf("没有这个预设：%s", id)
	}
	b, err := os.ReadFile(filepath.Join(s.UserDir, "presets", kind, id+".md"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// selection.json（readSelection）：只查 defaults、conversations 是不是表，里面的每一项用到时才看（Bun 版也是），
// 一个不相干的会话写坏了不影响别的
type selection struct {
	defaults, conversations map[string]json.RawMessage
}

func (s Store) selection() selection {
	var sel selection
	if s.UserDir == "" {
		return sel
	}
	b, err := os.ReadFile(filepath.Join(s.UserDir, "selection.json"))
	if err != nil {
		return sel
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(b, &top) != nil {
		return sel
	}
	json.Unmarshal(top["defaults"], &sel.defaults)
	json.Unmarshal(top["conversations"], &sel.conversations)
	return sel
}

// 一份选择（{"style": …, "scene": …}）里某一项：没写或者是 null 时 ok=false（JS 的 ?? 会接着看下一份）；
// 写了别的值（数字、空串）ok=true，但只有不空的字才算选了（JS 里 !id 当没选，预设的 id 又都是字）
func field(choice json.RawMessage, kind string) (id string, ok bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(choice, &m) != nil {
		return "", false // 不是表（null、数字……）：JS 里取它的属性是 undefined
	}
	v, has := m[kind]
	if !has || string(v) == "null" {
		return "", false
	}
	json.Unmarshal(v, &id) // 不是字就留空，下面当没选
	return id, true
}

// 这个会话用哪套：会话自己选的 → 平台 × 私聊 / 群聊的默认（每一项分开取）；选的不在了当没选（resolveChoice）
func (s Store) resolve(platform, scene, conversation string) (style, sceneID string) {
	sel := s.selection()
	slot := sel.defaults[platform+":"+scene]
	var own json.RawMessage
	if conversation != "" {
		own = sel.conversations[conversation]
	}
	pick := func(kind string) string {
		id, ok := field(own, kind)
		if !ok {
			id, _ = field(slot, kind)
		}
		if id == "" || (kind == "style" && id == DefaultStyle) {
			return ""
		}
		if _, ok := s.presetName(kind, id); !ok {
			return ""
		}
		return id
	}
	return pick("style"), pick("scene")
}

// ScenePrompts 是场合那一段加风格那一段（scenePrompts）。platform：本地是 local，别的用渠道名；builtin：内置的场合（scene-home……）
func (s Store) ScenePrompts(platform, scene, conversation, builtin string, vars map[string]string) ([]Section, error) {
	style, sc := s.resolve(platform, scene, conversation)
	var out []Section
	if sc != "" && sc != builtin {
		name, _ := s.presetName("scene", sc)
		t, err := s.presetText("scene", sc)
		if err != nil {
			return nil, err
		}
		out = append(out, Section{ID: builtin, Source: "预设：" + name, Text: FillVars(t, vars)})
	} else {
		sec, err := s.Render(builtin, vars)
		if err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	if style == "" {
		style = DefaultStyle
	}
	t, err := s.presetText("style", style)
	if err != nil {
		return nil, err
	}
	if t = FillVars(t, vars); t != "" {
		name, ok := s.presetName("style", style)
		if !ok {
			name = style
		}
		out = append(out, Section{ID: "style", Source: "预设：" + name, Text: t})
	}
	return out, nil
}
