package desktopread

// 标已读的入口（api.ReadMarker）。写库的规矩在 store.MarkDisplayRead；这里只管：
//   - 谁能写：装配方用 WithReadMark(true) 打开。只看历史（archive）的库是只读打开的，约定也不写，所以默认关，
//     关着回 api.ErrReadMarkUnsupported，不去碰库；
//   - 哪些会话能标：只有带已读记录的平台（QQ）；本地会话回同一个错误；
//   - 会话身份：key 必须是后台自己认得的那个写法（和读路径同一个 keyOf），拿别人的 key 来要报范围错误。

import (
	"context"
	"errors"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/store"
)

var _ api.ReadMarker = (*Service)(nil)

// WithReadMark 打开或关上标已读；装配时调用一次，之后不再改。
func (s *Service) WithReadMark(on bool) *Service {
	s.readMark = on
	return s
}

// MarkRead 见 api.ReadMarker
func (s *Service) MarkRead(ctx context.Context, key api.ConversationKey, upTo int64) error {
	if !s.readMark {
		return api.ErrReadMarkUnsupported
	}
	k := keyOf(key.Conversation)
	if k != key {
		return store.ErrDisplayScope
	}
	if k.Platform != "qq" {
		return api.ErrReadMarkUnsupported
	}
	st, err := s.stores.ForConversation(k.Conversation)
	if err != nil {
		return err
	}
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = st.MarkDisplayRead(ctx, store.DisplayScope{Platform: k.Platform, Conversation: k.Conversation, Characters: s.ids}, upTo)
	if errors.Is(err, store.ErrNoReadTracking) {
		return api.ErrReadMarkUnsupported
	}
	return err
}
