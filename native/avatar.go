package main

// 头像（主人 2026-11-02：消息左边和会话列表显示头像）。数据：后台的 qq_avatars（key → 媒体库里的图），只读，经 api.AvatarReader 取；
// 后台没有这个可选接口（演示数据、测试里的假读取）、没有这个头像、图解不开，都用 display.Avatar 的首字母色块，和以前一样。
// key：QQ 群是 group:<群号>，QQ 私聊是 user:<对方的号>，群里别人的话是 user:<号>；本地会话、角色、主人没有 QQ 头像，只画首字母。
// 这次不刷新：定时去 QQ 取新头像要等 QQ 媒体下载那条线的下载层（待办）。
// 存储照 faceStore：要用时在视图构建里 want，后台线程取、解，回界面线程放进去；取不到的记成"没有"，换会话再试一次。

import (
	"context"
	"strings"
	"sync"
	"unicode"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

const (
	avatarTier     = 96 // 位图长边（像素）：会话列表里 40 DIP，按 125%～200% 的缩放也够
	avatarFetchers = 3  // 同时取的上限
)

const (
	avLoading = iota + 1
	avOK
	avMissing
)

type avatarEntry struct {
	state int
	bmp   *ui.Bitmap
}

type avatarStore struct {
	ctx  context.Context
	post func(func()) // 回界面线程；窗口关了之后不会再执行
	// fetch 取一个头像的字节；没有这个头像回 api.ErrNoAvatar
	fetch func(ctx context.Context, key string) ([]byte, error)

	m        map[string]*avatarEntry // 只在界面线程读写
	sem      chan struct{}
	wg       sync.WaitGroup // 测试用：等 goroutine 都退出
	revision uint64         // 成功头像到达时使不可变会话快照失效；缺图不改像素，不触发全量复制。
}

func newAvatarStore(ctx context.Context, post func(func())) *avatarStore {
	return &avatarStore{ctx: ctx, post: post, m: map[string]*avatarEntry{}, sem: make(chan struct{}, avatarFetchers)}
}

// want 要这个 key 的头像：没有记录就发起一次取。界面线程里调（视图构建时）
func (s *avatarStore) want(key string) {
	if key == "" || s.fetch == nil {
		return
	}
	if _, ok := s.m[key]; ok {
		return
	}
	s.m[key] = &avatarEntry{state: avLoading}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-s.ctx.Done():
			return
		}
		var bmp *ui.Bitmap
		data, err := s.fetch(s.ctx, key)
		if err == nil {
			bmp, _ = decodeStatic(data, avatarTier) // 解不开就当没有
		}
		if s.ctx.Err() != nil {
			return
		}
		s.post(func() {
			e := s.m[key]
			if s.ctx.Err() != nil || e == nil || e.state != avLoading {
				return
			}
			if bmp != nil {
				e.state, e.bmp = avOK, bmp
				s.revision++
			} else {
				e.state = avMissing
			}
		})
	}()
}

// peek 这个头像的位图；还在取、没有、没要过都返回 nil
func (s *avatarStore) peek(key string) *ui.Bitmap {
	if e := s.m[key]; e != nil && e.state == avOK {
		return e.bmp
	}
	return nil
}

// clearMissing 换会话时调：确认没有的头像下次用到再取一次（取头像的下载层上线以后，新的会在这时被看到）
func (s *avatarStore) clearMissing() {
	for k, e := range s.m {
		if e.state == avMissing {
			delete(s.m, k)
		}
	}
}

// avs 头像的存储，第一次用时才建，跟着当前数据源的寿命（换后台时 attach 丢掉它）；
// 数据源不是 api.AvatarReader 时 fetch 为空，want 什么也不做，全部用首字母
func (a *app) avs() *avatarStore {
	if a.avatars == nil {
		ctx := a.viewCtx
		if ctx == nil {
			ctx = a.base()
		}
		a.avatars = newAvatarStore(ctx, a.post)
		if ar, ok := a.reader.(api.AvatarReader); ok {
			a.avatars.fetch = func(ctx context.Context, key string) ([]byte, error) {
				res, err := ar.ReadAvatar(ctx, key)
				if err != nil {
					return nil, err
				}
				if len(res.Value.Bytes) == 0 {
					return nil, api.ErrNoAvatar
				}
				return res.Value.Bytes, nil
			}
		}
	}
	return a.avatars
}

// avatarKeyOfConv 会话的头像 key：QQ 群是群的头像，QQ 私聊是对方的；别的没有
func avatarKeyOfConv(k api.ConversationKey) string {
	if k.Platform != "qq" {
		return ""
	}
	parts := strings.Split(k.Conversation, ":")
	switch {
	case k.Scene == "group" && len(parts) == 3 && parts[1] == "group":
		return avatarKey("group", parts[2])
	case k.Scene == "private" && len(parts) == 4 && parts[1] == "private":
		return avatarKey("user", parts[3])
	}
	return ""
}

// avatarKeyOfActor 一个发言人的头像 key：QQ 里的人（ActorID 是 qq:<号>）；主人的 ActorID 是 "owner"，号在读取层给的 actorUID 里
// （主人自己的头像一次取不到，原因就在这里：读取层不给号时只能返回空）；角色、没有号的没有
func avatarKeyOfActor(platform, actorKind, actorID, actorUID string) string {
	if platform != "qq" || actorKind == "character" {
		return ""
	}
	if actorKind == "owner" {
		return avatarKey("user", actorUID)
	}
	id, ok := strings.CutPrefix(actorID, "qq:")
	if !ok {
		return ""
	}
	return avatarKey("user", id)
}

// avatarKey 号码只认 4～12 位数字（和后台接口同一个口径），不合的不拼 key
func avatarKey(kind, id string) string {
	if len(id) < 4 || len(id) > 12 {
		return ""
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return kind + ":" + id
}

func avatarLabel(name string) string {
	if strings.TrimSpace(name) == "" {
		return "头像"
	}
	return name + "的头像"
}

// initialOf 首字（名字里第一个非空白的字，英文大写）；没有名字写问号
func initialOf(name string) string {
	for _, r := range name {
		if !unicode.IsSpace(r) {
			return string(unicode.ToUpper(r))
		}
	}
	return "?"
}

// nameColor 名字对应的颜色，和 display.Avatar 同一张表同一个算法
func nameColor(c *ui.Context, name string) ui.Color {
	t := c.Theme()
	palette := [...]ui.Color{t.Accent, t.Success, t.Warning}
	var hash uint32
	for _, r := range name {
		hash = hash*31 + uint32(r)
	}
	return palette[hash%uint32(len(palette))]
}
