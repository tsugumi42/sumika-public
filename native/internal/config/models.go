package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ModelField 只接受模型页授权的字段；角色 ID 单独传，不能拼任意路径。
type ModelField string

const (
	CharacterModel      ModelField = "character.model"
	CharacterBackground ModelField = "character.background"
	BackgroundModel     ModelField = "defaults.backgroundModel"
	BackgroundThinking  ModelField = "defaults.backgroundThinking"
	ThinkingLocal       ModelField = "agent.thinking.local"
	ThinkingPrivate     ModelField = "agent.thinking.private"
	ThinkingGroup       ModelField = "agent.thinking.group"
	// 对某个模型的窗口、单次输出写覆盖（ModelChange.Model 是模型 ID）；写 0 = 去掉这一项覆盖，回到服务商或 models.dev 的值
	ModelContextOverride ModelField = "modelMeta.overrides.contextWindow"
	ModelOutputOverride  ModelField = "modelMeta.overrides.maxOutputTokens"
	ContextWindow        ModelField = "agent.context.window"
	ContextReserve       ModelField = "agent.context.reserve"
	ContextSoftRatio     ModelField = "agent.context.softRatio"
	ContextHardRatio     ModelField = "agent.context.hardRatio"
	ContextKeepTokens    ModelField = "agent.context.keepTokens"
	ContextAutoCompact   ModelField = "agent.context.autoCompactChat"
)

// ModelChange 每次只改一项。指针区分缺值、空串和零；值的类型必须与 Field 一致。
type ModelChange struct {
	Field     ModelField
	Character string
	// Model 只给 ModelContextOverride、ModelOutputOverride 用：被覆盖的模型 ID
	Model  string
	String *string
	Int    *int
	Float  *float64
	Bool   *bool
}

func (ch ModelChange) Target() ([]string, any, bool, error) {
	count := 0
	for _, set := range []bool{ch.String != nil, ch.Int != nil, ch.Float != nil, ch.Bool != nil} {
		if set {
			count++
		}
	}
	if count != 1 {
		return nil, nil, false, errors.New("修改必须带一个且仅一个类型化的值")
	}
	var value any
	model := false
	var path []string
	switch ch.Field {
	case CharacterModel, CharacterBackground:
		if ch.Character == "" {
			return nil, nil, false, errors.New("角色 ID 不能为空")
		}
		key := "model"
		if ch.Field == CharacterBackground {
			key = "background"
		}
		path = []string{"characters", ch.Character, key}
		model = true
	case BackgroundModel:
		path = []string{"defaults", "backgroundModel"}
		model = true
	case BackgroundThinking, ThinkingLocal, ThinkingPrivate, ThinkingGroup, ContextWindow, ContextReserve, ContextSoftRatio, ContextHardRatio, ContextKeepTokens, ContextAutoCompact:
		path = strings.Split(string(ch.Field), ".")
	case ModelContextOverride, ModelOutputOverride:
		if !ModelIDSafe(ch.Model) {
			return nil, nil, false, errors.New("这个模型 ID 含特殊字符或为空，请直接在配置文件的 modelMeta.overrides 里写")
		}
		key := "contextWindow"
		if ch.Field == ModelOutputOverride {
			key = "maxOutputTokens"
		}
		path = []string{"modelMeta", "overrides", ch.Model, key}
	default:
		return nil, nil, false, errors.New("模型页不允许修改这个键")
	}
	if ch.Model != "" && ch.Field != ModelContextOverride && ch.Field != ModelOutputOverride {
		return nil, nil, false, errors.New("只有模型覆盖能带模型 ID")
	}
	if ch.Field != CharacterModel && ch.Field != CharacterBackground && ch.Character != "" {
		return nil, nil, false, errors.New("全局字段不能带角色 ID")
	}
	switch ch.Field {
	case CharacterModel, CharacterBackground, BackgroundModel, BackgroundThinking, ThinkingLocal, ThinkingPrivate, ThinkingGroup:
		if ch.String == nil {
			return nil, nil, false, errors.New("这项需要字符串")
		}
		value = *ch.String
	case ContextWindow, ContextReserve, ContextKeepTokens, ModelContextOverride, ModelOutputOverride:
		if ch.Int == nil {
			return nil, nil, false, errors.New("这项需要整数")
		}
		value = *ch.Int
		if (ch.Field == ModelContextOverride || ch.Field == ModelOutputOverride) && *ch.Int < 0 {
			return nil, nil, false, errors.New("窗口和单次输出不能是负数（0 = 去掉覆盖）")
		}
	case ContextSoftRatio, ContextHardRatio:
		if ch.Float == nil {
			return nil, nil, false, errors.New("这项需要小数")
		}
		value = *ch.Float
	case ContextAutoCompact:
		if ch.Bool == nil {
			return nil, nil, false, errors.New("这项需要开关")
		}
		value = *ch.Bool
	}
	if model && ch.String != nil {
		if *ch.String != strings.TrimSpace(*ch.String) {
			return nil, nil, false, errors.New("模型 ID 不能带首尾空白")
		}
		if ch.Field == CharacterModel && *ch.String == "" {
			return nil, nil, false, errors.New("说话模型不能清空")
		}
	}
	return path, value, model, nil
}

// ModelValues 是模型页字段的独立副本，不包含账号、权限或记忆写入授权。
type ModelValues struct {
	Characters            map[string]ModelCharacter
	BackgroundModel       string
	FallbackModels        []string
	BackgroundThinking    Thinking
	Local, Private, Group Thinking
	Context               ModelContext
	// Overrides 是对各模型窗口、单次输出的覆盖（modelMeta.overrides）
	Overrides map[string]ModelOverride
}
type ModelCharacter struct {
	Model, Background string
	FallbackModels    []string
}
type ModelContext struct {
	Window, Reserve, KeepTokens int
	SoftRatio, HardRatio        float64
	AutoCompactChat             bool
}

func ModelsOf(c Config) ModelValues {
	v := ModelValues{Characters: make(map[string]ModelCharacter, len(c.Characters)), BackgroundModel: c.Defaults.BackgroundModel, FallbackModels: slices.Clone(c.Defaults.FallbackModels), BackgroundThinking: c.Defaults.BackgroundThinking, Local: c.Agent.Thinking.Local, Private: c.Agent.Thinking.Private, Group: c.Agent.Thinking.Group}
	for id, ch := range c.Characters {
		v.Characters[id] = ModelCharacter{Model: ch.Model, Background: ch.Background, FallbackModels: slices.Clone(ch.FallbackModels)}
	}
	x := c.Agent.Context
	v.Context = ModelContext{x.Window, x.Reserve, x.KeepTokens, x.SoftRatio, x.HardRatio, x.AutoCompactChat}
	v.Overrides = make(map[string]ModelOverride, len(c.ModelMeta.Overrides))
	for id, o := range c.ModelMeta.Overrides {
		v.Overrides[id] = o
	}
	return v
}
func (v ModelValues) Background(id string) string {
	if s := strings.TrimSpace(v.Characters[id].Background); s != "" {
		return s
	}
	return strings.TrimSpace(v.BackgroundModel)
}
func (v ModelValues) Fallback(id string) []string {
	if models := v.Characters[id].FallbackModels; models != nil {
		return models
	}
	return v.FallbackModels
}
func (v ModelValues) Clone() ModelValues {
	m := make(map[string]ModelCharacter, len(v.Characters))
	for k, x := range v.Characters {
		x.FallbackModels = slices.Clone(x.FallbackModels)
		m[k] = x
	}
	v.Characters = m
	v.FallbackModels = slices.Clone(v.FallbackModels)
	o := make(map[string]ModelOverride, len(v.Overrides))
	for k, x := range v.Overrides {
		o[k] = x
	}
	v.Overrides = o
	return v
}
func (ch ModelChange) StringValue() string {
	if ch.String == nil {
		return ""
	}
	return *ch.String
}
func modelError(path []string) error {
	return fmt.Errorf("%s：不能安全定位这项 YAML，请改用块映射和单行标量", strings.Join(path, "."))
}
