package turnbudget

import (
	"context"
	"sumika-native/internal/kernel"
)

type rootControl interface {
	Track(context.Context, string) (context.Context, func())
	Exhaust(context.Context, string) error
}

func TrackRoot(ctx context.Context, b kernel.Budget, root string) (context.Context, func()) {
	if control, ok := b.(rootControl); ok {
		return control.Track(ctx, root)
	}
	return ctx, func() {}
}
func ExhaustRoot(ctx context.Context, b kernel.Budget, root string) error {
	if control, ok := b.(rootControl); ok {
		return control.Exhaust(ctx, root)
	}
	return nil
}
