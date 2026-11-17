// Package memview 是桌面记忆页的读服务（规格-记忆页 4.1）：实现 api.MemoryReader，只读。
// 生命周期、5 秒时限、一次一个查询、变化轮询、出处原话（连聊天库）、名字和打码都在这里；SQL 在 internal/memory 的 Browser 里。
// 页面读到的内容不进模型上下文、不进日志、不进别的库：这个包不写日志，错误只带原因，不带正文、搜索词、原话。
// 只交给桌面窗口；internal/archcheck 钉住 turn、channel、memplugin、tools、ctlserver 不许引用它。
package memview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/memory"
)

// ErrClosed 是服务关了以后再来的调用
var ErrClosed = errors.New("记忆读取服务已关闭")

// ErrParam：参数不对（角色不认识、会话或序号越界）。和 memory.ErrBrowseParam 一样，界面当程序错误
var ErrParam = errors.New("记忆页查询参数不对")

// Character 是装配给的一个角色
type Character struct {
	ID, Name string
	DB       string // memory.db 的路径；空 = 没有记忆库
	Browse   memory.BrowserOptions
	Events   memory.EventSource // 取出处原话；nil = 读不到聊天记录
}

// Names 是认人和会话名的查询（规格-记忆页 九·名字不另存一份）：kind 是 person 或 conv，查不到返回空串。
// feat/names-ui 的认人入口合进来以前，装配给 nil
type Names func(kind, id string) string

type Options struct {
	Names         Names
	ResolvePerson func(term string) []string // 搜索词能解析成人（名字或别名）时给出他们的 id；nil = 不解析
	Poll          time.Duration              // 变化轮询间隔；0 = 2 秒
	Timeout       time.Duration              // 单个查询时限；0 = 5 秒
	Retry         time.Duration              // 某个角色的概览没算成以后隔多久再算；0 = 10 秒
	Now           func() time.Time
}

type Service struct {
	chars  []Character
	byID   map[string]Character
	opt    Options
	gate   chan struct{} // 一次一个查询
	mu     sync.Mutex
	opened map[string]*memory.Browser

	lifeMu   sync.Mutex
	closed   bool
	down     atomic.Bool // Close 开始了：browser 不再打开新库（和 closed 同时置位，browser 在持 mu 时读它）
	inflight sync.WaitGroup
	base     context.Context
	stop     context.CancelFunc
}

var _ api.MemoryReader = (*Service)(nil)

func New(chars []Character, opt Options) *Service {
	s := &Service{chars: chars, byID: map[string]Character{}, opt: opt, gate: make(chan struct{}, 1), opened: map[string]*memory.Browser{}}
	for _, c := range chars {
		s.byID[c.ID] = c
	}
	s.base, s.stop = context.WithCancel(context.Background())
	return s
}

func (s *Service) poll() time.Duration {
	if s.opt.Poll > 0 {
		return s.opt.Poll
	}
	return 2 * time.Second
}

func (s *Service) retry() time.Duration {
	if s.opt.Retry > 0 {
		return s.opt.Retry
	}
	return 10 * time.Second
}

func (s *Service) timeout() time.Duration {
	if s.opt.Timeout > 0 {
		return s.opt.Timeout
	}
	return 5 * time.Second
}

func (s *Service) now() time.Time {
	if s.opt.Now != nil {
		return s.opt.Now()
	}
	return time.Now()
}

// enter 登记一次查询或订阅；Close 取消返回的 ctx，并等所有 done 调完才返回
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

// Close 拒绝新调用，取消在途查询和订阅，等它们都不再碰库，再关库；可以重复调用
func (s *Service) Close() {
	s.lifeMu.Lock()
	s.closed = true
	s.lifeMu.Unlock()
	s.down.Store(true)
	s.stop()
	s.inflight.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, b := range s.opened {
		b.Close()
		delete(s.opened, id)
	}
}

// browser 取（必要时打开）一个角色的库。没有记忆库返回 errNoDB；打不开的不缓存，下次再试
var errNoDB = errors.New("这个角色还没有记忆库")

func (s *Service) browser(id string) (*memory.Browser, error) {
	c, ok := s.byID[id]
	if !ok {
		return nil, ErrParam
	}
	if err := memory.ValidCharacterID(id); err != nil {
		return nil, ErrParam
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down.Load() {
		return nil, ErrClosed // 关了以后不再打开库，也不让"没有记忆库"盖住"已关闭"
	}
	if b := s.opened[id]; b != nil {
		return b, nil
	}
	if c.DB == "" {
		return nil, errNoDB
	}
	if _, err := os.Stat(c.DB); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errNoDB
		}
		return nil, fmt.Errorf("看记忆库：%w", err)
	}
	opt := c.Browse
	if opt.Now == nil {
		opt.Now = s.now
	}
	b, err := memory.OpenBrowser(c.DB, opt)
	if err != nil {
		return nil, fmt.Errorf("打开记忆库：%w", err)
	}
	s.opened[id] = b
	return b, nil
}

// run 是每个查询的外壳：登记、5 秒时限、等一次一个的闸、查、查完再看一眼 ctx（取消了就丢结果）
func (s *Service) run(ctx context.Context, fn func(context.Context) error) error {
	ctx, done, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.gate }()
	if err := fn(ctx); err != nil {
		return err
	}
	return ctx.Err()
}

func failed[T any](err error) (api.Result[T], error) {
	return api.Result[T]{State: api.Failed, Err: err}, err
}

func ready[T any](v T) (api.Result[T], error) {
	return api.Result[T]{State: api.Ready, Value: v}, nil
}
