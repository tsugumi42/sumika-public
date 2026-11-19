package ctlserver

import (
	"context"

	"sumika-native/internal/ctlproto"
)

// EnvBackend 是 doctor 的可选扩展：环境清单的快照在宿主里（后台没开、打开失败时也有基础部分），
// 没接的宿主（演示、别的命令行宿主）不实现它。只读：服务端不改快照，也不让 doctor 带参数。
type EnvBackend interface {
	EnvSnapshot(ctx context.Context) (ctlproto.DoctorSnapshot, error)
}

// doctor 校验 doctor 请求：不带参数；后端没接就失败。回复出去前把外来文字清一遍。
func (s *Server) doctor(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) != 0 {
		return invalid("doctor 不带参数")
	}
	b, ok := s.b.(EnvBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "环境清单不可用"), nil
	}
	out, err := b.EnvSnapshot(s.ctx)
	if err != nil {
		return s.fromErr(err), nil
	}
	cleanDoctor(&out)
	return success(out), nil
}

// cleanDoctor 把清单里的外来文字（路径、错误原因、配置值）过一遍 Clean：不能带控制字符和换行进终端和 Agent 的上下文。
func cleanDoctor(d *ctlproto.DoctorSnapshot) {
	if d.Items == nil {
		d.Items = []ctlproto.DoctorItem{}
	}
	for i := range d.Items {
		it := &d.Items[i]
		it.ID = ctlproto.Clean(it.ID)
		it.Group = ctlproto.Clean(it.Group)
		it.Name = ctlproto.Clean(it.Name)
		it.Detail = ctlproto.Clean(it.Detail)
		it.Next = ctlproto.Clean(it.Next)
		it.Affects = ctlproto.Clean(it.Affects)
		for j := range it.Sub {
			sub := &it.Sub[j]
			sub.Name = ctlproto.Clean(sub.Name)
			sub.Detail = ctlproto.Clean(sub.Detail)
			sub.Next = ctlproto.Clean(sub.Next)
		}
	}
}
