package main

// 链接解析模板的命令行入口（sumika-ctl link preview / link template，规格-链接解析模板第十节）接进桌面程序：
// 只读，取数走后台共享的链接服务和缓存，只渲染文字，不发消息、不下载媒体、不改配置。

import (
	"context"
	"errors"

	backend "sumika-native/internal/app"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/ctlserver"
)

var _ ctlserver.LinkBackend = (*ctlBackend)(nil)

func linkErr(err error) error {
	if errors.Is(err, backend.ErrLinkInvalid) {
		return ctlproto.Errorf(ctlproto.CodeInvalid, err.Error())
	}
	return err
}

func (b *ctlBackend) LinkPreview(ctx context.Context, rawURL string, a ctlproto.LinkArgs) (ctlproto.LinkPreviewResult, error) {
	r, err := b.checkpointReader() // 取桌面的读入口：只看历史时回 readonly，演示模式回 demo
	if err != nil {
		return ctlproto.LinkPreviewResult{}, err
	}
	out, err := r.LinkPreview(ctx, backend.LinkPreviewRequest{URL: rawURL, Template: a.Template, Site: a.Site, Sample: a.Sample})
	if err != nil {
		return ctlproto.LinkPreviewResult{}, linkErr(err)
	}
	return ctlproto.LinkPreviewResult{Site: out.Site, Template: out.Template, Forward: out.Forward, Source: out.Source, Messages: out.Messages, Notes: out.Notes}, nil
}

func (b *ctlBackend) LinkTemplates(ctx context.Context, site string) (ctlproto.LinkTemplates, error) {
	r, err := b.checkpointReader()
	if err != nil {
		return ctlproto.LinkTemplates{}, err
	}
	states, err := r.LinkTemplates(ctx, site)
	if err != nil {
		return ctlproto.LinkTemplates{}, linkErr(err)
	}
	var res ctlproto.LinkTemplates
	for _, s := range states {
		info := ctlproto.LinkSiteInfo{Key: s.Key, Label: s.Label, Direct: s.Direct, Forward: s.Forward, ForwardMode: s.ForwardMode, ForwardOver: s.ForwardOver, ForwardOverBytes: s.ForwardOverBytes, DescLimit: s.DescLimit}
		for _, f := range s.Fields {
			kind := "text"
			switch f.Kind {
			case 1:
				kind = "slot"
			case 2, 3, 4:
				kind = "layout"
			}
			info.Placeholders = append(info.Placeholders, ctlproto.LinkField{Names: f.Names, Kind: kind, Sample: f.Sample, Doc: f.Doc})
		}
		res.Sites = append(res.Sites, info)
	}
	return res, nil
}
