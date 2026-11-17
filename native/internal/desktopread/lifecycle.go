package desktopread

import (
	"context"
	"errors"
)

// ErrClosed 是服务关了以后再来的调用：宿主要关库了，不再查。
var ErrClosed = errors.New("读取服务已关闭")

// enter 登记一次查询或订阅；Close 取消返回的 ctx，并等所有 done 调完才返回（规格 4.4 第 8 条、3.3：消费者先于关库退出）。
func (s *Service) enter(ctx context.Context) (context.Context, func(), error) {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.closed {
		return nil, nil, ErrClosed
	}
	s.inflight.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.base, cancel)
	return ctx, func() { stop(); cancel(); s.inflight.Done() }, nil
}

// Close 拒绝新调用，取消在途查询和订阅，等它们都不再碰库才返回；可以重复调用。
func (s *Service) Close() {
	s.lifeMu.Lock()
	s.closed = true
	s.lifeMu.Unlock()
	s.stop()
	s.inflight.Wait()
}
