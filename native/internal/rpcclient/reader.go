package rpcclient

import (
	"context"
	"errors"
	"os"

	"sumika-native/internal/api"
	"sumika-native/internal/rpcconv"
	"sumika-native/internal/rpcwire"
)

// Reader 是协议客户端的 api.Reader。只读：没有可选写接口
type Reader struct{ C *Client }

var _ api.Reader = (*Reader)(nil)

func unsupported[T any](method string) api.Result[T] {
	return api.Result[T]{State: api.Unsupported, Err: errors.New("这个后台没有 " + method)}
}

// read 调一个回 Result<W> 的方法并转成 api 的 Result
func read[W, T any](ctx context.Context, c *Client, method string, params any, conv func(W) T) (api.Result[T], error) {
	if !c.Has(method) {
		return unsupported[T](method), nil
	}
	var res rpcwire.Result[W]
	if err := c.Call(ctx, method, params, &res); err != nil {
		return api.Result[T]{State: api.Failed, Err: err}, err
	}
	return rpcconv.ResultIn(res, conv), nil
}

func (r *Reader) WatchOverview(ctx context.Context, req api.OverviewRequest) (api.Subscription[api.Overview], error) {
	p := rpcconv.OverviewRequestOut(req)
	if r.C.Has(rpcwire.MethodOverviewSubscribe) {
		return watch(ctx, r.C, rpcwire.MethodOverviewSubscribe, rpcwire.MethodOverviewUnsubscribe, p,
			func(w rpcwire.Result[rpcwire.Overview]) api.Result[api.Overview] {
				return rpcconv.ResultIn(w, rpcconv.OverviewIn)
			})
	}
	return onceSub(ctx, func(ctx context.Context) (api.Result[api.Overview], error) {
		return read(ctx, r.C, rpcwire.MethodOverviewRead, p, rpcconv.OverviewIn)
	})
}

func (r *Reader) ReadMessages(ctx context.Context, req api.ConversationRequest, cursor api.Cursor) (api.Result[api.MessagePage], error) {
	return read(ctx, r.C, rpcwire.MethodMessagesRead, rpcwire.MessagesReadParams{Request: rpcconv.ConversationRequestOut(req), Cursor: string(cursor)}, rpcconv.PageIn)
}

func (r *Reader) WatchConversation(ctx context.Context, req api.ConversationRequest) (api.Subscription[api.ConversationSnapshot], error) {
	if r.C.Has(rpcwire.MethodConversationSubscribe) {
		return watch(ctx, r.C, rpcwire.MethodConversationSubscribe, rpcwire.MethodConversationUnsubscribe, rpcconv.ConversationRequestOut(req),
			func(w rpcwire.Result[rpcwire.ConversationSnapshot]) api.Result[api.ConversationSnapshot] {
				return rpcconv.ResultIn(w, rpcconv.SnapshotIn)
			})
	}
	// 后台没有会话订阅：读最新一页当成一个快照（Bun 第 1 批）
	return onceSub(ctx, func(ctx context.Context) (api.Result[api.ConversationSnapshot], error) {
		page, err := r.ReadMessages(ctx, req, "")
		out := api.Result[api.ConversationSnapshot]{State: page.State, Err: page.Err, Stale: page.Stale}
		out.Value = api.ConversationSnapshot{Conversation: api.ConversationView{Key: req.Key}, Page: page.Value}
		return out, err
	})
}

func (r *Reader) ReadMedia(ctx context.Context, ref api.MediaRef, tier api.MediaTier) (api.Result[api.MediaView], error) {
	return read(ctx, r.C, rpcwire.MethodMediaRead, rpcwire.MediaReadParams{Ref: rpcconv.MediaRefOut(ref), Tier: int(tier)}, rpcconv.MediaIn)
}

func (r *Reader) ReadFaceIndex(ctx context.Context) (api.Result[api.List[api.FaceView]], error) {
	return read(ctx, r.C, rpcwire.MethodFacesIndex, rpcwire.Empty{}, rpcconv.FacesIn)
}

func (r *Reader) ReadFacePNG(ctx context.Context, id string) (api.Result[api.MediaView], error) {
	return read(ctx, r.C, rpcwire.MethodFacesPNG, rpcwire.FacePNGParams{ID: id}, rpcconv.MediaIn)
}

func (r *Reader) ReadSettings(ctx context.Context) (api.Result[api.Settings], error) {
	return read(ctx, r.C, rpcwire.MethodSettingsRead, rpcwire.Empty{}, rpcconv.SettingsIn)
}

func (r *Reader) WatchRuntime(ctx context.Context) (api.Subscription[api.RuntimeView], error) {
	if r.C.Has(rpcwire.MethodRuntimeSubscribe) {
		return watch(ctx, r.C, rpcwire.MethodRuntimeSubscribe, rpcwire.MethodRuntimeUnsubscribe, rpcwire.Empty{},
			func(w rpcwire.Result[rpcwire.RuntimeView]) api.Result[api.RuntimeView] {
				return rpcconv.ResultIn(w, rpcconv.RuntimeIn)
			})
	}
	return onceSub(ctx, func(ctx context.Context) (api.Result[api.RuntimeView], error) {
		return read(ctx, r.C, rpcwire.MethodRuntimeRead, rpcwire.Empty{}, rpcconv.RuntimeIn)
	})
}

// processAlive：Windows 上 FindProcess 会去开进程句柄，进程不在就失败；别的系统 FindProcess 总成功，只当活着
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}
