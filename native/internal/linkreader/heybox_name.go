package linkreader

import (
	"context"
	"net/url"
	"strings"
)

// heyboxGameName 补小黑盒游戏页的游戏名：game_introduction 不给名字，小黑盒的游戏编号就是 Steam appid，
// 照同一个编号问 Steam 商店（只要 basic 字段）。取不到只记原因，不拿简介第一句冒充标题。
// transient 说这次没取到是不是临时的（网络、超时、5xx、限流）：是的话整条结果不进缓存，下次再问；
// Steam 明确回了 success:false、404 这类就照常缓存，缓存期内显示“游戏 编号”。
func (f *Fetcher) heyboxGameName(ctx context.Context, appid string) (name Field[string], transient bool) {
	endpoint := "https://store.steampowered.com/api/appdetails?appids=" + url.QueryEscape(appid) + "&cc=cn&l=schinese&filters=basic"
	body, status, h, err := f.request(ctx, endpoint, steamStoreSource)
	if err != nil {
		return missing[string]("steam_name_" + outcomeKind(err)), transientKind(err)
	}
	if err := steamHTTP(status, h, steamStoreSource); err != nil {
		return missing[string]("steam_name_not_returned"), transientKind(err) || status == 429
	}
	d, err := decodeSteamEnvelope[steamAppData](body, appid, steamStoreSource, "app_unavailable_or_missing")
	if err != nil || string(d.AppID) != appid || d.Name == nil || strings.TrimSpace(*d.Name) == "" {
		return missing[string]("steam_name_not_returned"), false
	}
	return available(strings.TrimSpace(*d.Name)), false
}
