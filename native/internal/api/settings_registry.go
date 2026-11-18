package api

import (
	"context"
	"encoding/json"

	"sumika-native/internal/settingsreg"
)

// SettingsRegistry 仅交给可信桌面宿主，不扩展 Reader、QQ 或模型工具。
type SettingsRegistry interface {
	ReadSettingsRegistry(context.Context) (SettingsView, error)
	WriteSetting(context.Context, SettingChange, SettingsVersion) (SettingsView, error)
}

type SettingsVersion struct {
	Config  string
	Desktop string
}

type SettingsView struct {
	Items   []settingsreg.Value
	Version SettingsVersion
	// 写入落盘后的生效提示；不把重启接收的配置冒充已运行值。
	Effect string
	// EmbedMissingModel 只在嵌入因为找不到登记的模型文件起不来时非空：设置页据此显示“找不到模型文件：<路径>”
	// 和“打开下载页”“指定模型目录”两个入口。可执行文件、运行库、模型 id 不认识都不算缺模型。
	EmbedMissingModel *EmbedMissingModel
}

// EmbedMissingModel 是缺哪个模型、在找哪个完整文件、官方页在哪（登记表没有地址就是空）。
type EmbedMissingModel struct {
	Model       string
	Path        string
	DownloadURL string
}

// Value 是一个完整 JSON 值。空、null、错误类型均拒绝；名单等复合项整体提交。
type SettingChange struct {
	Key   string
	Value json.RawMessage
}

// SavedSettingsError 表示已持久化，但返回完整快照失败；界面应重新读取，不能声称未保存。
type SavedSettingsError struct{ Cause error }

func (e *SavedSettingsError) Error() string {
	return "设置已保存，但读取设置失败：" + e.Cause.Error()
}
func (e *SavedSettingsError) Unwrap() error { return e.Cause }
