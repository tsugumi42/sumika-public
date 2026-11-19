package api_test

import (
	"context"
	"errors"

	"sumika-native/internal/api"
)

// compileReader 只放在测试里，确认外部调用方不依赖 app、SQL 或界面，
// 也能实现完整接口。
type compileReader struct{}

var _ api.Reader = compileReader{}
var errFake = errors.New("compile-only fake")

func (compileReader) WatchOverview(context.Context, api.OverviewRequest) (api.Subscription[api.Overview], error) {
	return nil, errFake
}
func (compileReader) ReadMessages(context.Context, api.ConversationRequest, api.Cursor) (api.Result[api.MessagePage], error) {
	return api.Result[api.MessagePage]{State: api.Failed, Err: errFake}, errFake
}
func (compileReader) WatchConversation(context.Context, api.ConversationRequest) (api.Subscription[api.ConversationSnapshot], error) {
	return nil, errFake
}
func (compileReader) ReadMedia(context.Context, api.MediaRef, api.MediaTier) (api.Result[api.MediaView], error) {
	return api.Result[api.MediaView]{State: api.Failed, Err: errFake}, errFake
}
func (compileReader) ReadFaceIndex(context.Context) (api.Result[api.List[api.FaceView]], error) {
	return api.Result[api.List[api.FaceView]]{State: api.Failed, Err: errFake}, errFake
}
func (compileReader) ReadFacePNG(context.Context, string) (api.Result[api.MediaView], error) {
	return api.Result[api.MediaView]{State: api.Failed, Err: errFake}, errFake
}
func (compileReader) ReadSettings(context.Context) (api.Result[api.Settings], error) {
	return api.Result[api.Settings]{State: api.Failed, Err: errFake}, errFake
}
func (compileReader) WatchRuntime(context.Context) (api.Subscription[api.RuntimeView], error) {
	return nil, errFake
}
