package ctlserver

import (
	"context"
	"encoding/json"

	"sumika-native/internal/ctlproto"
)

// KernelBackend 是内核故障命令的可选扩展：回合内核在正在运行的程序里，没接的宿主不实现它。
// KernelFaults 只读；KernelRetry 的角色和会话都空 = 全部故障，没恢复要返回带着当时状态（ctlproto.KernelRetry）的 *ctlproto.Error。
type KernelBackend interface {
	KernelFaults(ctx context.Context) (ctlproto.KernelFaults, error)
	KernelRetry(ctx context.Context, character, conversation string) (ctlproto.KernelRetry, error)
}

const kernelUsage = "用法：kernel faults | kernel retry [<角色> <会话>]"

// kernel 校验 kernel 请求：只认 faults、retry；retry 的参数要么都不写（全部），要么角色和会话都写。参数形状不对不调后端。
// 重试是主人和运维的操作：这条命令只在本机管道上，不在模型能调的工具里；演示数据模式一律不做。
func (s *Server) kernel(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) == 0 {
		return invalid("%s", kernelUsage)
	}
	switch req.Args[0] {
	case ctlproto.KernelFaultsCmd:
		if len(req.Args) != 1 {
			return invalid("%s", kernelUsage)
		}
		if r, ok := s.demo("kernel faults"); ok {
			return r, nil
		}
		b, ok := s.b.(KernelBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "内核故障接口不可用"), nil
		}
		out, err := b.KernelFaults(s.ctx)
		if err != nil {
			return s.fromErr(err), nil
		}
		return success(cleanKernelFaults(out)), nil
	case ctlproto.KernelRetryCmd:
		if len(req.Args) != 1 && len(req.Args) != 3 {
			return invalid("%s", kernelUsage)
		}
		var character, conversation string
		if len(req.Args) == 3 {
			character, conversation = req.Args[1], req.Args[2]
			if character == "" || conversation == "" {
				return invalid("%s", kernelUsage)
			}
		}
		if r, ok := s.demo("kernel retry"); ok {
			return r, nil
		}
		b, ok := s.b.(KernelBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "内核故障接口不可用"), nil
		}
		out, err := b.KernelRetry(s.ctx, character, conversation)
		if err != nil {
			// 没恢复：失败，回复里带着还没恢复的那些；别的错误照常
			if (len(out.Remaining) > 0 || out.StrayEnds > 0) && s.ctx.Err() == nil {
				r := fail(ctlproto.CodeFailed, ctlproto.Clean(err.Error()))
				if b, merr := json.Marshal(cleanKernelRetry(out)); merr == nil {
					r.Data = b
				}
				return r, nil
			}
			return s.fromErr(err), nil
		}
		return success(cleanKernelRetry(out)), nil
	}
	return invalid("%s", kernelUsage)
}

func cleanKernelFault(f ctlproto.KernelFault) ctlproto.KernelFault {
	f.Character, f.Conversation = ctlproto.Clean(f.Character), ctlproto.Clean(f.Conversation)
	f.Kind, f.Reason, f.Detail = ctlproto.Clean(f.Kind), ctlproto.Clean(f.Reason), ctlproto.Clean(f.Detail)
	return f
}

func cleanKernelFaults(in ctlproto.KernelFaults) ctlproto.KernelFaults {
	out := ctlproto.KernelFaults{Faults: make([]ctlproto.KernelFault, 0, len(in.Faults)), StrayEnds: in.StrayEnds}
	for _, f := range in.Faults {
		out.Faults = append(out.Faults, cleanKernelFault(f))
	}
	return out
}

func cleanKernelRetry(in ctlproto.KernelRetry) ctlproto.KernelRetry {
	out := ctlproto.KernelRetry{Cleared: make([]ctlproto.KernelFault, 0, len(in.Cleared)), Remaining: make([]ctlproto.KernelFault, 0, len(in.Remaining)), StrayEnds: in.StrayEnds}
	for _, f := range in.Cleared {
		out.Cleared = append(out.Cleared, cleanKernelFault(f))
	}
	for _, f := range in.Remaining {
		out.Remaining = append(out.Remaining, cleanKernelFault(f))
	}
	return out
}
