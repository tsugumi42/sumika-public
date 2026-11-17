package app

import (
	"context"

	"sumika-native/internal/api"
	"sumika-native/internal/checkpoint"
)

func checkpointProgressView(p checkpoint.Progress) api.CheckpointProgress {
	return api.CheckpointProgress{TargetID: p.TargetID, InverseID: p.InverseID, State: p.State, Done: p.Done, Total: p.Total, Completed: p.Completed, Remaining: p.Remaining, OutcomeSaved: p.OutcomeSaved, Error: p.Error}
}

func (r *DesktopReader) ReadCheckpointProgress(ctx context.Context, key api.ConversationKey, id string) (api.CheckpointProgress, error) {
	ctx, owner, done, err := r.enterCheckpoints(ctx, key)
	if err != nil {
		return api.CheckpointProgress{}, err
	}
	defer done()
	p, err := r.desk.app.checkpoints.Progress(ctx, owner, id)
	return checkpointProgressView(p), err
}

func (r *DesktopReader) CancelCheckpointRestore(ctx context.Context, key api.ConversationKey, id string) (api.CheckpointProgress, error) {
	if err := r.bunOwned(); err != nil {
		return api.CheckpointProgress{}, err
	}
	ctx, owner, done, err := r.enterCheckpoints(ctx, key)
	if err != nil {
		return api.CheckpointProgress{}, err
	}
	defer done()
	p, err := r.desk.app.checkpoints.Cancel(ctx, owner, id)
	return checkpointProgressView(p), err
}

type checkpointProgressSub struct {
	out    chan api.Snapshot[api.CheckpointProgress]
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *checkpointProgressSub) Updates() <-chan api.Snapshot[api.CheckpointProgress] { return s.out }
func (s *checkpointProgressSub) Close()                                               { s.cancel(); <-s.done }

func (r *DesktopReader) WatchCheckpointProgress(ctx context.Context, key api.ConversationKey, id string) (api.Subscription[api.CheckpointProgress], error) {
	ctx, owner, done, err := r.enterCheckpoints(ctx, key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	watch, err := r.desk.app.checkpoints.WatchProgress(ctx, owner, id)
	if err != nil {
		cancel()
		done()
		return nil, err
	}
	s := &checkpointProgressSub{out: make(chan api.Snapshot[api.CheckpointProgress], 1), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer done()
		defer cancel()
		defer close(s.done)
		defer close(s.out)
		version := uint64(0)
		err := watch.Run(func(p checkpoint.Progress) error {
			version++
			value := api.Snapshot[api.CheckpointProgress]{Version: version, Result: api.Result[api.CheckpointProgress]{State: api.Ready, Value: checkpointProgressView(p)}}
			// 界面只保留最新累计值，慢消费者不能阻塞文件恢复或漏掉终态。
			select {
			case <-s.out:
			default:
			}
			select {
			case s.out <- value:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil && ctx.Err() == nil {
			select {
			case <-s.out:
			default:
			}
			select {
			case s.out <- api.Snapshot[api.CheckpointProgress]{Version: version + 1, Result: api.Result[api.CheckpointProgress]{State: api.Failed, Err: err}}:
			case <-ctx.Done():
			}
		}
	}()
	return s, nil
}
