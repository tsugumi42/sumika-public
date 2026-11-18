package api

import (
	"context"
	"sumika-native/internal/config"
)

// LinkSettingsWriter 只供本机设置窗口，不扩大 Reader 或外部工具入口。
type LinkSettingsWriter interface {
	LinkSettings(context.Context) (LinkSettingsView, error)
	SetLinkSettings(context.Context, LinkSettingsChange, string) (LinkSettingsView, error)
}
type LinkSettingsChange = config.LinkChange
type LinkSettingsValues struct {
	ContextEnabled bool
	Expand         config.LinkExpand
}

func LinkValuesOf(c config.LinkReaderConfig) LinkSettingsValues {
	c = c.Clone()
	return LinkSettingsValues{ContextEnabled: c.Context.Enabled, Expand: c.Expand}
}

type KnownLinkConversation struct {
	ID, Name, Kind, Account, Target string
	Unavailable                     bool
}
type LinkUse struct{ JobID, Conversation, Version, Stage string }
type LinkSettingsView struct {
	Configured, Running           LinkSettingsValues
	Version, RunningVersion       string
	RunningKnown                  bool
	RunningReason, DisabledReason string
	KnownConversations            []KnownLinkConversation
	Active                        []LinkUse
	Plugins                       []string
}
