package ctlserver

import (
	"context"
	"regexp"

	"sumika-native/internal/ctlproto"
)

// 可选扩展沿用控制服务模式；没有接可写桌面的宿主必须拒绝，不落到裸文件服务。
type CheckpointBackend interface {
	CheckpointList(context.Context, string) (ctlproto.CheckpointList, error)
	CheckpointPreview(context.Context, string, string, string) (ctlproto.CheckpointPlan, error)
	CheckpointRestore(context.Context, string, string, string, string) (ctlproto.CheckpointRestore, error)
	CheckpointProgress(context.Context, string, string) (ctlproto.CheckpointProgress, error)
	CheckpointCancel(context.Context, string, string) (ctlproto.CheckpointProgress, error)
}

var checkpointConversation = regexp.MustCompile(`^console:[^\x00-\x20:/\\]{1,128}(?::topic:[^\x00-\x20:/\\]{1,128})?$`)
var checkpointID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var checkpointVersion = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Server) checkpoint(args []string) (ctlproto.Reply, func()) {
	if len(args) < 2 || !checkpointConversation.MatchString(args[1]) {
		return invalid("checkpoint 要写 list|preview|restore|status|cancel 和完整本地私聊会话 console:<角色>[:topic:<话题>]")
	}
	sub := args[0]
	mode := "step"
	switch sub {
	case "list":
		if len(args) != 2 {
			return invalid("checkpoint list <本地会话>")
		}
	case "status", "cancel":
		if len(args) != 3 || !checkpointID.MatchString(args[2]) {
			return invalid("checkpoint status|cancel <本地会话> <32 位检查点 ID>")
		}
	case "preview", "restore":
		n := 3
		if sub == "restore" {
			n = 4
		}
		if len(args) != n && len(args) != n+1 {
			return invalid("checkpoint preview <本地会话> <ID> [step|turn|recovery|resume]；restore 另需 ID 后的 version")
		}
		if !checkpointID.MatchString(args[2]) {
			return invalid("检查点 ID 必须是 list 返回的 32 位小写十六进制值")
		}
		if sub == "restore" && !checkpointVersion.MatchString(args[3]) {
			return invalid("version 必须是 preview 返回的 64 位小写十六进制值")
		}
		if len(args) == n+1 {
			mode = args[n]
		}
		if mode != "step" && mode != "turn" && mode != "recovery" && mode != "resume" {
			return invalid("模式只能是 step、turn、recovery、resume")
		}
	default:
		return invalid("checkpoint 子命令只能是 list、preview、restore、status、cancel")
	}
	if s.opt.Demo {
		return fail(ctlproto.CodeDemo, "演示模式没有文件检查点"), nil
	}
	b, ok := s.b.(CheckpointBackend)
	if !ok {
		return fail(ctlproto.CodeReadOnly, "可写检查点接口不可用"), nil
	}
	switch sub {
	case "list":
		d, err := b.CheckpointList(s.ctx, args[1])
		return s.result(d, err), nil
	case "preview":
		d, err := b.CheckpointPreview(s.ctx, args[1], args[2], mode)
		return s.result(d, err), nil
	case "status":
		d, err := b.CheckpointProgress(s.ctx, args[1], args[2])
		return s.result(d, err), nil
	case "cancel":
		d, err := b.CheckpointCancel(s.ctx, args[1], args[2])
		return s.result(d, err), nil
	default:
		d, err := b.CheckpointRestore(s.ctx, args[1], args[2], mode, args[3])
		return s.result(d, err), nil
	}
}
