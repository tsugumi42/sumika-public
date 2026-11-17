package ctlserver

import (
	"context"

	"sumika-native/internal/ctlproto"
)

// ProactiveBackend 是主动联系日志的可选扩展：读导演的状态和最近 n 行日志。只读，没接的宿主不实现它
type ProactiveBackend interface {
	ProactiveLog(ctx context.Context, n int) (ctlproto.ProactiveLog, error)
}

// proactive 处理 proactive log：只认 log，行数用 -n（默认 50，最多 500）。只读。
// 日志里的外来文字（备注）出去前清掉控制字符，其余原样；客户端打印前还会再清一遍
func (s *Server) proactive(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) != 1 || req.Args[0] != "log" {
		return invalid("用法：proactive log [-n 行数]")
	}
	n := req.N
	if n == 0 {
		n = ctlproto.DefaultLogLines
	}
	if n < 0 || n > ctlproto.MaxLogLines {
		return invalid("-n 要在 1 到 %d 之间", ctlproto.MaxLogLines)
	}
	if r, ok := s.demo("proactive log"); ok {
		return r, nil
	}
	b, ok := s.b.(ProactiveBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "主动联系日志接口不可用"), nil
	}
	l, err := b.ProactiveLog(s.ctx, n)
	if err != nil {
		return s.fromErr(err), nil
	}
	for i := range l.Rows {
		l.Rows[i].Character = ctlproto.Clean(l.Rows[i].Character)
		l.Rows[i].Thread = ctlproto.Clean(l.Rows[i].Thread)
		l.Rows[i].Note = ctlproto.Clean(l.Rows[i].Note)
		l.Rows[i].Reason = ctlproto.Clean(l.Rows[i].Reason)
		l.Rows[i].Kind = ctlproto.Clean(l.Rows[i].Kind)
	}
	for i := range l.States {
		l.States[i].Character = ctlproto.Clean(l.States[i].Character)
	}
	return success(l), nil
}
