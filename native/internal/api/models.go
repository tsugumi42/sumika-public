package api

import (
	"context"
	"errors"
	"sumika-native/internal/config"
	"time"
)

// 模型页是桌面可选能力，不扩大 Reader，也不交给 QQ、本机控制接口或模型工具。
type ModelSettingsWriter interface {
	ModelSettings(context.Context) (ModelSettingsView, error)
	RefreshModels(context.Context) (ModelSettingsView, error)
	SetModelSettings(context.Context, ModelSettingsChange, string) (ModelSettingsView, error)
}
type ModelSettingsChange = config.ModelChange
type ModelSettingsValues = config.ModelValues

var ErrModelsUnavailable = errors.New("开始接收以后才能选模型")
var ErrModelSettingsClosed = errors.New("模型设置接口已关闭")
var ErrModelSettingsVersion = config.ErrConfigVersion

type ModelSettingsView struct {
	Configured                      ModelSettingsValues
	Running                         ModelSettingsValues
	RunningKnown                    bool
	Version, RunningVersion, Source string
	Models                          []ModelOption
	Groups                          []ModelGroup
	ModelsReadAt                    time.Time
	ModelsError                     string
	ModelsEditable                  bool
	ModelsDisabledReason            string
	Characters                      []ModelCharacterSettings
	ReadOnly                        []ModelReadOnly
	ApplyErrors                     []string
	Active                          []ModelUse
	RunningReason                   string
	// ModelsDev 是 models.dev 资料库的状态（模型窗口和单次输出的第三层来源，ADR-334）
	ModelsDev ModelsDevState
}

// ModelsDevState：Enabled 是开着没；FetchedAt 零值 = 没有缓存；FromCache = 现在用的是缓存（这次没联网成功）
type ModelsDevState struct {
	Enabled   bool
	Entries   int
	FetchedAt time.Time
	FromCache bool
	Error     string
}
type ModelOption struct {
	ID, Name, Provider, Label                          string
	Input, Output, ReasoningLevels                     []string
	ContextWindow                                      int
	ContextReported, VisionReported, ReasoningReported bool
	Subtitle                                           string
	// MaxOutput 是实际用的单次输出上限；WindowSource、OutputSource 是 user / provider / modelsdev / default（default 界面标"未知"）。
	// 下面几个是各来源说的值（0 = 没说），悬停提示"服务商说多少、资料库说多少"用；DevMixed：资料库里各家说法不一致，取了最小值
	MaxOutput                      int
	WindowSource, OutputSource     string
	ProviderWindow, ProviderOutput int
	DevWindow, DevOutput           int
	UserWindow, UserOutput         int
	DevMixed                       bool
}
type ModelGroup struct {
	Provider string
	Models   []ModelOption
}
type ModelCharacterSettings struct {
	ID, ConfiguredModel, ConfiguredBackground, RunningModel, RunningBackground string
	ModelMissing, BackgroundMissing                                            bool
}
type ModelReadOnly struct{ Key, Value, Reason string }

type ModelUse struct {
	Character, Conversation, Version, Model, Thinking string
	Window, Reserve                                   int
	Soft, Hard                                        float64
}

// SavedModelSettingsError 表示磁盘已保存；View 带实际生效状态，界面不能回滚成“没存”。
type SavedModelSettingsError struct{ Reasons []string }

func (e *SavedModelSettingsError) Error() string {
	return "已保存，部分没生效：" + joinModelErrors(e.Reasons)
}
func joinModelErrors(reasons []string) string {
	s := ""
	for _, r := range reasons {
		if s != "" {
			s += "；"
		}
		s += r
	}
	return s
}
