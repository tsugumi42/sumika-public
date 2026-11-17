package ctlserver

import (
	"context"
	"strings"

	"sumika-native/internal/ctlproto"
)

// ArtistBackend 是画师表命令的可选扩展：表在正在运行的程序里，没接的宿主不实现它。
// ArtistList 只读；其余三个会改表（导入、确认、移除），导入的文件由程序自己读。
type ArtistBackend interface {
	ArtistList(ctx context.Context, platform, status string, n int) (ctlproto.ArtistListResult, error)
	ArtistImport(ctx context.Context, platform, path string) (ctlproto.ArtistChange, error)
	ArtistUndo(ctx context.Context, platform, batch string) (ctlproto.ArtistChange, error)
	ArtistConfirm(ctx context.Context, platform string, ids []string) (ctlproto.ArtistChange, error)
	ArtistRemove(ctx context.Context, platform string, ids []string) (ctlproto.ArtistChange, error)
}

const artistUsage = "用法：artist list [--status pending|confirmed|declined|all] [-n 行数] | artist import <文件> | artist import --undo <批次> | artist confirm <ID>... | artist remove <ID>..."

// artist 校验 artist 请求：子命令只认 list、import、confirm、remove；参数形状不对一律 invalid，不调后端。
// 只有 list 只读（readOnly 表）；演示数据模式一律不做。
func (s *Server) artist(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) == 0 {
		return invalid("%s", artistUsage)
	}
	var a ctlproto.ArtistArgs
	if req.Artist != nil {
		a = *req.Artist
	}
	if msg := a.Validate(); msg != "" {
		return invalid("%s", msg)
	}
	platform := a.Platform
	if platform == "" {
		platform = "x"
	}
	sub, rest := req.Args[0], req.Args[1:]
	if sub != ctlproto.ArtistListCmd && (a.Status != "" || req.N != 0) {
		return invalid("--status、-n 只用于 artist list")
	}
	if sub != ctlproto.ArtistImportCmd && a.Undo != "" {
		return invalid("--undo 只用于 artist import")
	}
	var run func(b ArtistBackend) (any, error)
	switch sub {
	case ctlproto.ArtistListCmd:
		if len(rest) != 0 {
			return invalid("%s", artistUsage)
		}
		n := req.N
		if n == 0 {
			n = ctlproto.DefaultArtistRows
		}
		if n < 0 || n > ctlproto.MaxArtistRows {
			return invalid("-n 要在 1 到 %d 之间", ctlproto.MaxArtistRows)
		}
		status := a.Status
		if status == "" {
			status = "pending"
		}
		run = func(b ArtistBackend) (any, error) { return b.ArtistList(s.ctx, platform, status, n) }
	case ctlproto.ArtistImportCmd:
		switch {
		case a.Undo != "" && len(rest) == 0:
			run = func(b ArtistBackend) (any, error) { return b.ArtistUndo(s.ctx, platform, a.Undo) }
		case a.Undo == "" && len(rest) == 1:
			path := rest[0]
			if path == "" || len(path) > ctlproto.MaxArtistPath || strings.ContainsAny(path, "\r\n\x00") {
				return invalid("导入文件的路径不合法")
			}
			run = func(b ArtistBackend) (any, error) { return b.ArtistImport(s.ctx, platform, path) }
		default:
			return invalid("用法：artist import <文件> 或 artist import --undo <批次>")
		}
	case ctlproto.ArtistConfirmCmd, ctlproto.ArtistRemoveCmd:
		if len(rest) == 0 || len(rest) > ctlproto.MaxArtistIDs {
			return invalid("要写 1 到 %d 个账号 ID", ctlproto.MaxArtistIDs)
		}
		for _, id := range rest {
			if !ctlproto.ValidArtistID(id) {
				return invalid("账号 ID 里有不合法的字符")
			}
		}
		if sub == ctlproto.ArtistConfirmCmd {
			run = func(b ArtistBackend) (any, error) { return b.ArtistConfirm(s.ctx, platform, rest) }
		} else {
			run = func(b ArtistBackend) (any, error) { return b.ArtistRemove(s.ctx, platform, rest) }
		}
	default:
		return invalid("不认识的 artist 子命令，现在只有 list、import、confirm、remove")
	}
	if r, ok := s.demo("artist " + sub); ok {
		return r, nil
	}
	b, ok := s.b.(ArtistBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "画师表接口不可用"), nil
	}
	res, err := run(b)
	if err != nil {
		return s.fromErr(err), nil
	}
	if l, ok := res.(ctlproto.ArtistListResult); ok {
		// 简介里来的名字、账号是外来文字：出去前清掉控制字符，其余原样；客户端打印前还会再清一遍
		for i := range l.Rows {
			l.Rows[i].Handle = ctlproto.Clean(l.Rows[i].Handle)
			l.Rows[i].Name = ctlproto.Clean(l.Rows[i].Name)
			for k := range l.Rows[i].Clues {
				l.Rows[i].Clues[k] = ctlproto.Clean(l.Rows[i].Clues[k])
			}
		}
		res = l
	}
	return success(res), nil
}
