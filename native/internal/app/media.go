package app

// 收到的图片、视频和分享卡片封面下载（规格-QQ收图下载和断线补录、规格-QQ视频）：入库回调只排队，
// 下载在另外的工人里做，不占会话队列，不挡入库和回复。只在 receive 模式装；关机时在入库一站关掉以后、关库之前收尾。

import (
	"context"
	"errors"
	"fmt"
	"sumika-native/internal/applog"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/snowluma"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediafetch"
)

// openMedia 装媒体下载。开关关闭时只存地址；历史补扫仍只管图片，视频补扫后续做。
func (a *App) openMedia() error {
	if !a.cfg.QQ.Media.Download {
		a.logf("收图下载关着（qq.media.download），收到的图只存协议端给的地址")
		return nil
	}
	if u, off := a.set.Unavailable()["qq"]; off {
		a.logf("QQ 的库没打开，收图下载不装：%s", u.Reason)
		return nil
	}
	st, err := a.set.For("qq")
	if err != nil {
		return fmt.Errorf("收图下载取 QQ 库：%w", err)
	}
	var refresher mediafetch.Refresher
	if a.adapter != nil {
		refresher = qqRefresher{ad: a.adapter, account: a.account}
	}
	dl := a.opt.MediaDownloader
	if dl == nil {
		dl = mediafetch.NewDownloader(mediafetch.DownloadOptions{})
	}
	f, err := mediafetch.New(mediafetch.Options{
		Store: st, Data: a.paths.Root, Platform: "qq", Downloader: dl,
		Refresher: refresher, FileResolver: qqRefresher{ad: a.adapter, account: a.account}, Clock: a.clock(), Logf: a.logfFor(applog.Mediafetch),
		Warnf: a.warnFor(applog.Mediafetch), Errorf: a.errorFor(applog.Mediafetch),
	})
	if err != nil {
		return err
	}
	a.media = f
	f.Start()
	return nil
}

// closeMedia 取消在途的下载、等工人退出；之后不写库不写文件。没装的跳过
func (a *App) closeMedia() {
	if a.media != nil {
		a.media.Close()
	}
}

// withMedia 只把新存 QQ 消息的媒体排队；陌生人、自检、补录和自己的号发的都下，不看角色是否观察。
func (a *App) withMedia(next func(channel.Stored)) func(channel.Stored) {
	return func(s channel.Stored) {
		if a.media != nil && s.Inbound.Conv.Channel == "qq" && !s.Duplicate {
			a.media.Offer(s.Seq, s.Inbound.Segs)
		}
		next(s)
	}
}

// qqRefresher 通过 get_msg 同时刷新图片和视频地址。
type qqRefresher struct {
	ad      *snowluma.Adapter
	account string
}

func (r qqRefresher) Message(ctx context.Context, conversation, platformID string) ([]types.Seg, error) {
	conv, err := types.ParseConversation(conversation)
	if err != nil {
		return nil, err
	}
	in, err := r.ad.Message(ctx, r.account, conv, platformID)
	if errors.Is(err, snowluma.ErrNoReply) || errors.Is(err, types.ErrQueryBudget) {
		// 没连上、超时、断线或本轮预算耗尽：都只是暂时问不到，不能因此把图记成过期。
		return nil, fmt.Errorf("%w：%v", mediafetch.ErrRefreshUnavailable, err)
	}
	if err != nil {
		return nil, err
	}
	return in.Segs, nil
}

func (r qqRefresher) FileURL(ctx context.Context, conversation string, seg types.Seg) (string, error) {
	if r.ad == nil {
		return "", mediafetch.ErrRefreshUnavailable
	}
	u, err := r.ad.FileURL(ctx, r.account, conversation, seg)
	if errors.Is(err, snowluma.ErrNoReply) || errors.Is(err, types.ErrQueryBudget) {
		return "", mediafetch.ErrRefreshUnavailable
	}
	return u, err
}
