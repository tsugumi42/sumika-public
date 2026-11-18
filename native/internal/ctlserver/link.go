package ctlserver

import (
	"context"
	"strings"

	"sumika-native/internal/ctlproto"
)

// LinkBackend 是链接解析命令的可选扩展：没接链接解析的宿主不实现它。两个方法都只读——
// 不发消息、不下载媒体、不改配置；预览取数走共享 Service 和 24 小时缓存。
type LinkBackend interface {
	// LinkPreview 取一条链接（Sample 时不联网，用站点示例数据）并按模板渲染成文字
	LinkPreview(ctx context.Context, rawURL string, a ctlproto.LinkArgs) (ctlproto.LinkPreviewResult, error)
	// LinkTemplates 列出站点当前生效的模板；site 非空时带上该站点可用的占位符
	LinkTemplates(ctx context.Context, site string) (ctlproto.LinkTemplates, error)
}

// link 校验 link 请求：子命令只认 preview 和 template；链接只收一个 http(s) 地址，别的一律 invalid，不调后端。
func (s *Server) link(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) == 0 {
		return invalid("link 要写子命令：preview 或 template")
	}
	var a ctlproto.LinkArgs
	if req.Link != nil {
		a = *req.Link
	}
	if msg := a.Validate(); msg != "" {
		return invalid("%s", msg)
	}
	if r, ok := s.demo("link"); ok {
		return r, nil
	}
	b, ok := s.b.(LinkBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "链接解析接口不可用"), nil
	}
	switch req.Args[0] {
	case ctlproto.LinkPreview:
		if a.Sample {
			if len(req.Args) != 1 || a.Site == "" {
				return invalid("link preview --sample 要写 --site <站点键>，不带链接")
			}
			res, err := b.LinkPreview(s.ctx, "", a)
			if err != nil {
				return s.fromErr(err), nil
			}
			return success(res), nil
		}
		if len(req.Args) != 2 {
			return invalid("用法：link preview <链接> [--template <文字或@预设>] [--site <站点键>] 或 link preview --sample --site <站点键>")
		}
		u := strings.TrimSpace(req.Args[1])
		if len(u) > 2048 || !(strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) || strings.ContainsAny(u, "\r\n\x00 ") {
			return invalid("链接要写成一个完整的 http(s) 地址")
		}
		res, err := b.LinkPreview(s.ctx, u, a)
		if err != nil {
			return s.fromErr(err), nil
		}
		return success(res), nil
	case ctlproto.LinkTemplate:
		if len(req.Args) > 2 || a.Template != "" || a.Sample {
			return invalid("用法：link template [站点键]")
		}
		site := ""
		if len(req.Args) == 2 {
			site = req.Args[1]
			if len(site) > 64 || strings.ContainsAny(site, "\r\n\x00 ") {
				return invalid("站点键不合法")
			}
		}
		res, err := b.LinkTemplates(s.ctx, site)
		if err != nil {
			return s.fromErr(err), nil
		}
		return success(res), nil
	}
	return invalid("不认识的 link 子命令，现在只有 preview、template")
}
