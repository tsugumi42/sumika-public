package api

import (
	"context"

	"sumika-native/internal/config"
)

// LocationSettings 是设置页里“所在地”这一项要的：搜候选、读、写。只供本机设置窗口，不扩大 Reader 或外部工具入口。
// 写入走设置页和设置工具共用的服务（带配置版本检查、备份）。
type LocationSettings interface {
	// SearchPlaces 按地名查候选（Open-Meteo，中文、英文、拼音都行）；最多 8 个
	SearchPlaces(ctx context.Context, query string) ([]Place, error)
	Location(ctx context.Context) (LocationView, error)
	// SetLocation 写入所在地；nil 是清空。base 是读到的 Version，别处改过会返回 config.ErrConfigVersion 和最新的视图
	SetLocation(ctx context.Context, loc *config.Location, base string) (LocationView, error)
}

// Place 是一个地点候选
type Place struct {
	ID       string
	Label    string // 某区，某市，中国
	Detail   string // 候选行右边的补充：城市名，说明群里会说到哪一级
	Location config.Location
}

// LocationView 是所在地的保存值和配置版本
type LocationView struct {
	Location config.Location
	Version  string
}
