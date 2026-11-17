package ctlserver

import (
	"context"

	"sumika-native/internal/ctlproto"
)

// StatsBackend 是运行页统计的可选扩展（规格-运行页 第三节），沿用 ChatBackend 的做法：没接统计的宿主不实现它。
// 只读：不收路径、SQL、清理动作。用量和存储已接入；后端交回全量报告，裁成 --by / --full 要的样子由本层做
type StatsBackend interface {
	Usage(ctx context.Context, a ctlproto.StatsArgs) (ctlproto.UsageReport, error)
}

// QualityBackend 是质量标签的可选扩展：后端按 StatsArgs 的范围和 --task、--failures 交回报告。只读
type QualityBackend interface {
	Quality(ctx context.Context, a ctlproto.StatsArgs) (ctlproto.QualityReport, error)
}

// SpansBackend 是回合耗时时间线的可选扩展。
type SpansBackend interface {
	Spans(ctx context.Context, a ctlproto.StatsArgs) (ctlproto.SpansReport, error)
}

// RebuildBackend 是重建留痕的可选扩展：每天、每个场合重建几次、为什么、丢了几条、有没有摘要覆盖。
type RebuildBackend interface {
	Rebuild(ctx context.Context, a ctlproto.StatsArgs) (ctlproto.RebuildReport, error)
}

// StorageBackend 是存储标签的可选扩展：读快照加增量，不触发扫描。和 StatsBackend 分开，没接存储的宿主只实现用量也行
type StorageBackend interface {
	Storage(ctx context.Context) (ctlproto.StorageReport, error)
}

// ResourcesBackend 是资源标签的可选扩展：采一次进程表、主程序内部、后台队列。只读，不触发任何操作；
// 命令行没有前一次采样，实现要自己隔一小会儿采两次，CPU 不给空
type ResourcesBackend interface {
	Resources(ctx context.Context) (ctlproto.ResourcesReport, error)
}

// stats 校验 stats 请求：子命令只认 usage，范围互斥和上限在这里把关（"唯一的关口"），别的子命令回 invalid
func (s *Server) stats(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) == 0 {
		return invalid("stats 要写子命令：usage、storage、quality、spans、rebuild 或 resources")
	}
	switch req.Args[0] {
	case ctlproto.StatsUsage:
	case ctlproto.StatsStorage:
		return s.storage(req)
	case ctlproto.StatsQuality:
		return s.quality(req)
	case ctlproto.StatsSpans:
		return s.spans(req)
	case ctlproto.StatsRebuild:
		return s.rebuild(req)
	case ctlproto.StatsResources:
		return s.resources(req)
	default:
		return invalid("不认识的 stats 子命令，现在只有 usage、storage、quality、spans、rebuild、resources")
	}
	if len(req.Args) != 1 {
		return invalid("stats usage 不带位置参数，范围用 --days / --year / --all")
	}
	var a ctlproto.StatsArgs
	if req.Stats != nil {
		a = *req.Stats
	}
	if a.Task != "" || a.Failures != 0 {
		return invalid("--task、--failures 只用于 stats quality")
	}
	if a.Turn != "" {
		return invalid("--turn 只用于 stats spans")
	}
	if err := a.Validate(); err != nil {
		return invalid("%s", err.Error())
	}
	if r, ok := s.demo("stats"); ok {
		return r, nil
	}
	b, ok := s.b.(StatsBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "统计接口不可用"), nil
	}
	rep, err := b.Usage(s.ctx, a)
	if err != nil {
		return s.fromErr(err), nil
	}
	return success(rep.Shape(a.By, a.Full)), nil
}

// spans 处理 stats spans：--days / --all 汇总每种环节花了多久，--turn 列一轮的时间线。
func (s *Server) spans(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) != 1 {
		return invalid("stats spans 不带位置参数，范围用 --days / --all，看一轮用 --turn last 或 --turn <回合号>")
	}
	var a ctlproto.StatsArgs
	if req.Stats != nil {
		a = *req.Stats
	}
	if err := a.ValidateSpans(); err != nil {
		return invalid("%s", err.Error())
	}
	if r, ok := s.demo("stats"); ok {
		return r, nil
	}
	b, ok := s.b.(SpansBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "统计接口不可用"), nil
	}
	rep, err := b.Spans(s.ctx, a)
	if err != nil {
		return s.fromErr(err), nil
	}
	return success(rep), nil
}

// rebuild 处理 stats rebuild：--days / --all 汇总每天、每个场合的重建次数、原因、丢了几条、摘要覆盖。只读
func (s *Server) rebuild(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) != 1 {
		return invalid("stats rebuild 不带位置参数，范围用 --days / --all")
	}
	var a ctlproto.StatsArgs
	if req.Stats != nil {
		a = *req.Stats
	}
	if err := a.ValidateRebuild(); err != nil {
		return invalid("%s", err.Error())
	}
	if r, ok := s.demo("stats"); ok {
		return r, nil
	}
	b, ok := s.b.(RebuildBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "统计接口不可用"), nil
	}
	rep, err := b.Rebuild(s.ctx, a)
	if err != nil {
		return s.fromErr(err), nil
	}
	return success(rep), nil
}

// quality 处理 stats quality：只认 --days / --all、--task、--failures；校验在这里把关，不合的不碰后端。只读
func (s *Server) quality(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) != 1 {
		return invalid("stats quality 不带位置参数，范围用 --days / --all，只看一类用 --task，失败条数用 --failures")
	}
	var a ctlproto.StatsArgs
	if req.Stats != nil {
		a = *req.Stats
	}
	if err := a.ValidateQuality(); err != nil {
		return invalid("%s", err.Error())
	}
	if r, ok := s.demo("stats"); ok {
		return r, nil
	}
	b, ok := s.b.(QualityBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "统计接口不可用"), nil
	}
	rep, err := b.Quality(s.ctx, a)
	if err != nil {
		return s.fromErr(err), nil
	}
	return success(rep), nil
}

// resources 处理 stats resources：不带位置参数，也不带范围、分组选项（资源是现在这一刻的）；只读
func (s *Server) resources(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) != 1 {
		return invalid("stats resources 不带位置参数")
	}
	if req.Stats != nil {
		return invalid("stats resources 不接受 --days、--year、--all、--by、--full 这些选项")
	}
	if r, ok := s.demo("stats"); ok {
		return r, nil
	}
	b, ok := s.b.(ResourcesBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "统计接口不可用"), nil
	}
	rep, err := b.Resources(s.ctx)
	if err != nil {
		return s.fromErr(err), nil
	}
	return success(rep), nil
}

// storage 处理 stats storage：不带位置参数，也不带范围、分组选项（存储没有时间范围）；只读，不触发扫描
func (s *Server) storage(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) != 1 {
		return invalid("stats storage 不带位置参数")
	}
	if req.Stats != nil {
		return invalid("stats storage 不接受 --days、--year、--all、--by、--full")
	}
	if r, ok := s.demo("stats"); ok {
		return r, nil
	}
	b, ok := s.b.(StorageBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "统计接口不可用"), nil
	}
	rep, err := b.Storage(s.ctx)
	if err != nil {
		return s.fromErr(err), nil
	}
	return success(rep), nil
}
