// Package persona 读角色卡，照 daemon/plugins/persona.ts 的 load、splitPersona 和 daemon/util/prompts.ts 的 personaSections（只读）。
// 卡在 characters/<id>/card/（数据目录的新布局），没有 card/ 就是 characters/<id>/ 本身
package persona

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml"

	"sumika-native/internal/identity"
	"sumika-native/internal/prompts"
	"sumika-native/internal/textclean"
)

// Blocks 是 character.md 拆出来的四块；四个标题都没有时整篇在 Whole
type Blocks struct {
	Identity, Behavior, Expression, InPrivate, Whole string
}

// Character 是一个角色（第 1 步只用到这几项）
type Character struct {
	ID, Name, Model string
	// OwnerName 是这个角色怎么称呼所有者（character.yml 的 ownerName）；没写是空串，用 OwnerCall 取默认
	OwnerName string
	Blocks    Blocks
}

// OwnerCall 是这个角色怎么称呼所有者：卡上写了就用它，没写叫"用户"
func (c Character) OwnerCall() string {
	if c.OwnerName != "" {
		return c.OwnerName
	}
	return identity.OwnerDefault
}

// ErrNoModel：还没给她选说话用的模型（Bun 版的 NoModel）
var ErrNoModel = errors.New("还没给她选说话用的模型：去设置 · 模型里选一个")

// CardDir 是卡在哪；只有 card/ 不存在时回到旧布局，其他错误留在原路径交给读取者报。
func CardDir(charactersDir, id string) string {
	card := filepath.Join(charactersDir, id, "card")
	if _, err := os.Stat(card); !errors.Is(err, os.ErrNotExist) {
		return card
	}
	return filepath.Join(charactersDir, id)
}

// Load 读一个角色。model：配置 characters.<id>.model（ADR-084），空就看卡里以前写的 model
func Load(charactersDir, id, model string) (Character, error) {
	dir := CardDir(charactersDir, id)
	raw, md, err := readCard(dir, id)
	if err != nil {
		return Character{}, err
	}
	name := raw["name"].(string)
	if model == "" {
		model, _ = raw["model"].(string)
	}
	if model == "" {
		return Character{}, ErrNoModel
	}
	ownerName, _ := raw["ownerName"].(string)
	return Character{ID: id, Name: name, Model: model, OwnerName: ownerName, Blocks: Split(textclean.Trim(string(md)))}, nil
}

// OwnerCallFor 读一个角色卡怎么称呼所有者；没写、读不出来都是"用户"（消息头标签、记忆整理要它）
func OwnerCallFor(charactersDir, id string) string {
	raw, _, err := readCard(CardDir(charactersDir, id), id)
	if err != nil {
		return identity.OwnerDefault
	}
	ownerName, _ := raw["ownerName"].(string)
	return Character{OwnerName: ownerName}.OwnerCall()
}

// Name 只读卡上的显示名：没选模型的角色也要能读（装配处给记忆插件填 Name，Load 会因为没有模型报错）
func Name(charactersDir, id string) (string, error) {
	raw, _, err := readCard(CardDir(charactersDir, id), id)
	if err != nil {
		return "", err
	}
	return raw["name"].(string), nil
}

// Display 是界面要显示的那几项：名字、颜色、说话用的模型（model 规则同 Load，没选模型返回空、不报错）。
// 只读卡，不写、不进 card/history/。
func Display(charactersDir, id, model string) (name, color, effectiveModel string, err error) {
	raw, _, err := readCard(CardDir(charactersDir, id), id)
	if err != nil {
		return "", "", "", err
	}
	if model == "" {
		model, _ = raw["model"].(string)
	}
	color, _ = raw["color"].(string)
	return raw["name"].(string), color, model, nil
}

// readCard 读 character.yml（拆成表）和 character.md；两处读法只有这一份，Load 和 Name 都用它
func readCard(dir, id string) (map[string]any, []byte, error) {
	yml, err := os.ReadFile(filepath.Join(dir, "character.yml"))
	if err != nil {
		return nil, nil, fmt.Errorf("角色 %s：%w", id, err)
	}
	md, err := os.ReadFile(filepath.Join(dir, "character.md"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("角色 %s：读取失败：%w", id, err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(yml, &raw); err != nil {
		return nil, nil, fmt.Errorf("角色 %s：读取失败：%w", id, err)
	}
	if bad := checkYml(raw); bad != "" {
		return nil, nil, fmt.Errorf("角色 %s：character.yml 有误：%s", id, bad)
	}
	return raw, md, nil
}

// checkYml 照 persona.ts 的 CharacterYml（zod）查 character.yml：name 是不空的字；model 写了就得是不空的字；
// models 写了就得是表、里面的 background / topic 写了就得是字；color 写了就得是字。写了但值是空（null）也不行（zod 的 optional 只放过没写）。
// 返回哪里不对，都对是空串
func checkYml(raw map[string]any) string {
	str := func(v any, nonEmpty bool) bool {
		s, ok := v.(string)
		return ok && (!nonEmpty || s != "")
	}
	if !str(raw["name"], true) {
		return "name 要写一个名字"
	}
	if v, ok := raw["model"]; ok && !str(v, true) {
		return "model 要写模型的名字"
	}
	if v, ok := raw["models"]; ok {
		m, isMap := v.(map[string]any)
		if !isMap {
			return "models 要写成表"
		}
		for _, k := range []string{"background", "topic"} {
			if x, ok := m[k]; ok && !str(x, false) {
				return "models." + k + " 要写模型的名字"
			}
		}
	}
	if v, ok := raw["color"]; ok && !str(v, false) {
		return "color 要写成颜色的字（比如 \"#8a7cff\"）"
	}
	if v, ok := raw["ownerName"]; ok {
		s, isStr := v.(string)
		if !isStr || !validOwnerName(s) {
			return "ownerName 要写成 1～16 个字，不能有括号、冒号、方括号、控制字符或首尾空白，也不能和固定的身份词（" + strings.Join(identity.Reserved, "、") + "）一样"
		}
	}
	return ""
}

// validOwnerName 是卡上的称呼：1～16 个字，不能带括号、冒号、方括号（消息头用它们分名字和身份），
// 不能有控制字符、U+2028/U+2029、首尾空白（纯空白也不行），也不能占用 identity.Reserved 里的固定身份词
func validOwnerName(s string) bool {
	if n := utf8.RuneCountInString(s); n < 1 || n > 16 {
		return false
	}
	if textclean.Trim(s) != s || strings.ContainsAny(s, "()（）:：[]［］{}") || slices.Contains(identity.Reserved, s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

var commentRE = regexp.MustCompile(`(?s)<!--.*?-->`)
var lineRE = regexp.MustCompile(`\r?\n`)

var headings = map[string]int{"身份": 0, "行为": 1, "表达": 2, "私下": 3}

// 这一行是不是那四个二级标题之一：JS 的 /^##\s+(.+?)\s*$/ 拿到的名字是去掉首尾空白的剩下部分（\s 是 JS 的空白）
func heading(line string) (int, bool) {
	rest, ok := strings.CutPrefix(line, "##")
	if !ok || rest == "" {
		return 0, false
	}
	if r := []rune(rest)[0]; !textclean.IsJSSpace(r) {
		return 0, false
	}
	i, ok := headings[textclean.Trim(rest)]
	return i, ok
}

// Split 把 character.md 拆成四块（splitPersona）：去掉 <!-- --> 注释；第一个标题之前的算身份；
// 别的标题留在所在的块里；四个都没有就整篇放进 Whole（换行不动）；拆过的块换行统一成 LF
func Split(md string) Blocks {
	text := textclean.Trim(commentRE.ReplaceAllString(md, ""))
	lines := lineRE.Split(text, -1)
	found := false
	for _, l := range lines {
		if _, ok := heading(l); ok {
			found = true
			break
		}
	}
	if !found {
		return Blocks{Whole: text}
	}
	var acc [4][]string
	cur := 0
	for _, l := range lines {
		if h, ok := heading(l); ok {
			cur = h
			continue
		}
		acc[cur] = append(acc[cur], l)
	}
	j := func(i int) string { return textclean.Trim(strings.Join(acc[i], "\n")) }
	return Blocks{Identity: j(0), Behavior: j(1), Expression: j(2), InPrivate: j(3)}
}

// Sections 是角色卡拼进系统提示的几段（身份 → 行为 → 表达 → 私下）。outsiders：这一轮有外人（只有 QQ 群），有就不拼私下
func Sections(c Character, outsiders bool) []prompts.Section {
	card := "characters/" + c.ID + "/character.md"
	// 换行统一成 \n（拆过的块已经是，整篇一块的老卡、单独的 \r 还没有），和 Bun 版 personaSections 一样
	b := Blocks{Identity: prompts.LF(c.Blocks.Identity), Behavior: prompts.LF(c.Blocks.Behavior), Expression: prompts.LF(c.Blocks.Expression),
		InPrivate: prompts.LF(c.Blocks.InPrivate), Whole: prompts.LF(c.Blocks.Whole)}
	if b.Whole != "" {
		return []prompts.Section{{ID: "persona", Source: card, Text: "人设：\n" + b.Whole}}
	}
	with := func(title, text string) string {
		if text == "" {
			return ""
		}
		return title + "：\n" + text
	}
	out := []prompts.Section{
		{ID: "persona-identity", Source: card + "（## 身份）", Text: with("身份", b.Identity)},
		{ID: "persona-behavior", Source: card + "（## 行为）", Text: with("行为", b.Behavior)},
		{ID: "persona-expression", Source: card + "（## 表达）", Text: with("表达", b.Expression)},
	}
	if !outsiders && b.InPrivate != "" {
		out = append(out, prompts.Section{ID: "persona-private", Source: card + "（## 私下）", Text: "私下：\n" + b.InPrivate})
	}
	return out
}
