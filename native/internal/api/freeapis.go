package api

import (
	"context"

	"sumika-native/internal/freeapi"
)

// FreeAPISettingsWriter 是设置页“免费接口”分组用的：看每类每家的状态，置顶、关闭、清退避、立即检测，改探测和次数。
// 只供本机设置窗口，不扩大 Reader 或外部工具入口。
type FreeAPISettingsWriter interface {
	FreeAPI(context.Context) (FreeAPIView, error)
	FreeAPIAct(context.Context, FreeAPIAction) (FreeAPIView, error)
}

// FreeAPI 动作种类
const (
	FreeAPIPin          = "pin"          // ID、Bool
	FreeAPIOff          = "off"          // ID、Bool：关闭为真
	FreeAPIClearBackoff = "clearBackoff" // ID
	FreeAPIProbe        = "probe"        // ID 为空测全部
	FreeAPIProbeOn      = "probeEnabled" // Bool
	FreeAPIProbeHours   = "probeHours"   // Int
	FreeAPILimit        = "limit"        // Tool（weather、exchangeRate、calendar）、Field（actor、conversation）、Int
)

// FreeAPIAction 是页面上的一次操作
type FreeAPIAction struct {
	Kind       string
	ID, Tool   string
	Field      string
	Bool       bool
	Int        int
	ConfigBase string // 改配置的动作带上页面读到的配置版本，对不上就报版本冲突
}

// FreeAPILimitView 是一个工具的次数和今天的总用量
type FreeAPILimitView struct {
	Tool, Name            string // Tool 是配置里的键
	PerActorDaily         int
	PerConversationHourly int
	UsedToday             int
}

// FreeAPIView 是页面要的全部
type FreeAPIView struct {
	Categories   []freeapi.CategoryView
	Events       []freeapi.Event
	ProbeEnabled bool
	ProbeHours   int
	Limits       []FreeAPILimitView
	Version      string // 配置版本
	Note         string // 一行提示（比如检测结果）
}
