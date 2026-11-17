package desktopread

// 头像的只读入口（主人 2026-11-02：消息左边和会话列表显示头像）。
// 数据是 go/chat/qq.db 的 qq_avatars（key → 内容哈希），图在媒体库里（和聊天图共用数据组的 mediastore.Reader，平台 qq）。
// 和 ReadMedia 的区别：头像不属于任何一条事件，凭据就是 key；key 只认 user:<号> 和 group:<号>（4～12 位数字，和 Bun 时代同一个口径），
// 别的（卡片封面 url:…、路径、带点的）一律拒掉。没有登记或哈希为空是正常情况（没取过），回 ErrNoAvatar，界面退回首字母。
// 谁能看：头像是 QQ 上公开的，且这个接口只给本机的桌面界面（不经角色、不经 QQ），所以不按会话的观察范围卡；只放这两种 key。
// 这次不刷新：定时去 QQ 取新头像要等 QQ 媒体下载那条线的下载层（待办）。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"sumika-native/internal/api"
)

const avatarMax = 5 << 20 // 和 Bun 存头像时的上限一致

var avatarKey = regexp.MustCompile(`^(user|group):[0-9]{4,12}$`)

var _ api.AvatarReader = (*Service)(nil)

// ReadAvatar 见文件头
func (s *Service) ReadAvatar(ctx context.Context, key string) (api.Result[api.MediaView], error) {
	if !avatarKey.MatchString(key) {
		return failed[api.MediaView](api.Failed, errors.New("头像的 key 不对"))
	}
	if s.data == "" {
		return failed[api.MediaView](api.Unsupported, ErrNoData)
	}
	st, found := s.stores.Platform("qq")
	if !found {
		return failed[api.MediaView](api.Unsupported, errors.New("QQ 的库没打开"))
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case s.queryGate <- struct{}{}:
	case <-ctx.Done():
		return failed[api.MediaView](api.Failed, ctx.Err())
	}
	hash, _, ok, err := st.ReadAvatarHash(ctx, key)
	<-s.queryGate
	if err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	if !ok || !validHash(hash) {
		return failed[api.MediaView](api.Failed, api.ErrNoAvatar)
	}
	f, err := s.openMedia(s.data, "qq", hash)
	if err != nil {
		return failed[api.MediaView](api.Failed, api.ErrNoAvatar)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, avatarMax+1))
	if err != nil {
		return failed[api.MediaView](api.Failed, err)
	}
	if len(b) > avatarMax {
		return failed[api.MediaView](api.Failed, ErrMediaTooLarge)
	}
	return api.Result[api.MediaView]{State: api.Ready, Value: api.MediaView{Bytes: b, MIME: http.DetectContentType(b), Size: int64(len(b))}}, nil
}
