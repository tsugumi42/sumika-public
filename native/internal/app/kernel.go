package app

import (
	"context"
	"sync/atomic"

	"sumika-native/internal/channel"
	"sumika-native/internal/turn"
)

// trackedKernel 只计真实委托次数；寿命和排空由 Hub 独占。
type trackedKernel struct {
	channel.Kernel
	calls atomic.Uint64
}

func (k *trackedKernel) Respond(in turn.Input) <-chan turn.Outcome {
	k.calls.Add(1)
	return k.Kernel.Respond(in)
}
func (k *trackedKernel) idle(ctx context.Context) error {
	return k.Kernel.(interface{ Drain(context.Context) error }).Drain(ctx)
}
