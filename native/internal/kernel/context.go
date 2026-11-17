package kernel

import "context"

type invocationKey struct{}

// WithInvocation 只由宿主放入可信身份；工具参数和正文不参与构造。
func WithInvocation(ctx context.Context, in Invocation) context.Context {
	return context.WithValue(ctx, invocationKey{}, in)
}
func InvocationFromContext(ctx context.Context) Invocation {
	in, _ := ctx.Value(invocationKey{}).(Invocation)
	return in
}
