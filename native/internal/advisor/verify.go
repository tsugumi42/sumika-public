package advisor

import (
	"context"
	"sumika-native/internal/kernel"
	"time"
)

// Check 与异步队列分离；失败显式 partial，并保留宿主已写下的工作材料。
func (s *Service) Check(ctx context.Context, r kernel.ReviewRequest) (kernel.Verdict, error) {
	if !s.cfg.Verify.Enabled {
		return kernel.Verdict{CheckpointID: r.CheckpointID, Status: "partial"}, nil
	}
	r = freeze(r)
	inv := reviewInvocation(r, "verify")
	ctx, a, err := s.deps.Lifetime.Begin(ctx, inv)
	if err != nil {
		return kernel.Verdict{CheckpointID: r.CheckpointID, Status: "partial"}, err
	}
	var reviewErr error
	defer func() { a.End(reviewErr) }()
	timed, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.Verify.TimeoutMs)*time.Millisecond)
	defer cancel()
	v, err := s.review(timed, r, true, inv)
	// 已停止或授权/工作版本改变的核验只能留记录，不能让迟到 done 被采用。
	if err == nil {
		_, err = s.build(timed, r)
	}
	if err != nil {
		v.Status = "partial"
	}
	if err == nil && v.Status == "continue" && s.cfg.Verify.MaxContinuations == 0 {
		v.Status = "partial"
	}
	reviewErr = err
	return v, err
}
