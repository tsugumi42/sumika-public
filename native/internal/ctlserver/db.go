package ctlserver

import (
	"context"

	"sumika-native/internal/ctlproto"
)

// DBBackend 是 db 命令的可选扩展：库是正在运行的程序打开的，打开时记下的状态在它那里。
// 没接的宿主（演示、只看历史）不实现它。DBStatus 只读，不重新开库。
type DBBackend interface {
	DBStatus(ctx context.Context) (ctlproto.DBStatus, error)
}

const dbUsage = "用法：db status"

// db 校验 db 请求：只有 status，不带别的参数；演示数据模式一律不做。
func (s *Server) db(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) != 1 || req.Args[0] != ctlproto.DBStatusCmd {
		return invalid("%s", dbUsage)
	}
	if r, ok := s.demo("db status"); ok {
		return r, nil
	}
	b, ok := s.b.(DBBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "库状态接口不可用"), nil
	}
	out, err := b.DBStatus(s.ctx)
	if err != nil {
		return s.fromErr(err), nil
	}
	for i := range out.Databases {
		d := &out.Databases[i]
		d.Name = ctlproto.Clean(d.Name)
		d.Path = ctlproto.Clean(d.Path)
		d.Status = ctlproto.Clean(d.Status)
		d.Reason = ctlproto.Clean(d.Reason)
		d.Detail = ctlproto.Clean(d.Detail)
		d.Snapshot = ctlproto.Clean(d.Snapshot)
		for j := range d.Tracks {
			d.Tracks[j].Track = ctlproto.Clean(d.Tracks[j].Track)
		}
	}
	return success(out), nil
}
