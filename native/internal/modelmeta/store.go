package modelmeta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/fileatomic"
)

// 刷新的几个数是起步值，照别家同类做法，没有 Sumika 自己的流量数据：
// TTL 4 小时和失败退避 5 分钟取自 Hermes（agent/models_dev.py），带 ETag 的条件请求，没变就是 304、几乎不花流量；
// 响应上限 16 MB（全量约 6 MB）；请求超时 30 秒
const (
	DefaultURL     = "https://models.dev/api.json"
	DefaultTTL     = 4 * time.Hour
	failureBackoff = 5 * time.Minute
	maxBody        = 16 << 20
	requestTimeout = 30 * time.Second
	cacheVersion   = 1
)

// Store 管 models.dev 的本地缓存和刷新。热路径只读内存里的快照（Catalog），绝不联网；
// 启动时不阻塞，缓存读出来就能用，要不要联网由 Refresh 决定，取不到不影响启动。
type Store struct {
	Path    string // 缓存文件（<数据目录>/cache/models-dev.json）
	URL     string
	Proxy   string // 空则按环境变量
	Enabled bool
	TTL     time.Duration
	Now     func() time.Time
	HTTP    *http.Client // 测试里换成假服务的客户端
	Logf    func(string, ...any)

	mu        sync.Mutex
	cat       atomic.Pointer[Catalog]
	fetchedAt time.Time
	etag      string
	lastErr   string
	failedAt  time.Time
	fromCache bool
	// OnChange 在目录内容变了的时候调（不在持锁时）；正文没变（304、摘要一样）不调
	OnChange func()
}

// State 是给界面看的状态
type State struct {
	Enabled   bool
	Entries   int
	FetchedAt time.Time // 零值 = 没有缓存
	FromCache bool      // 现在用的是缓存（这次没联网成功）
	Error     string
}

type cacheFile struct {
	V         int     `json:"v"`
	FetchedAt int64   `json:"fetchedAt"`
	ETag      string  `json:"etag,omitempty"`
	Entries   []Entry `json:"entries"`
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func (s *Store) logf(f string, a ...any) {
	if s.Logf != nil {
		s.Logf(f, a...)
	}
}

// Catalog 是当前的快照；没读到任何数据时是 nil（Lookup 对 nil 安全，查不到）
func (s *Store) Catalog() *Catalog { return s.cat.Load() }

// State 现在的情况
func (s *Store) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Enabled: s.Enabled, FetchedAt: s.fetchedAt, FromCache: s.fromCache, Error: s.lastErr}
	if c := s.cat.Load(); c != nil {
		st.Entries = c.Count
	}
	return st
}

// Load 读本地缓存（任意年龄都用）。没有文件不算错；文件坏了改名隔离（不当空表用，也不覆盖，留着查原因），然后当没缓存
func (s *Store) Load() error {
	if s.Path == "" {
		return nil
	}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var f cacheFile
	if err == nil {
		err = json.Unmarshal(b, &f)
		if err == nil && (f.V != cacheVersion || len(f.Entries) == 0) {
			err = fmt.Errorf("缓存格式不认（版本 %d，%d 条）", f.V, len(f.Entries))
		}
	}
	if err != nil {
		bad := s.Path + ".bad-" + s.now().Format("20060102-150405")
		if rerr := os.Rename(s.Path, bad); rerr != nil {
			s.logf("模型资料缓存读不懂（%v），改名隔离也失败：%v", err, rerr)
		} else {
			s.logf("模型资料缓存读不懂（%v），已改名为 %s，当没有缓存", err, filepath.Base(bad))
		}
		return nil
	}
	s.mu.Lock()
	s.fetchedAt, s.etag, s.fromCache = time.UnixMilli(f.FetchedAt), f.ETag, true
	s.mu.Unlock()
	s.cat.Store(NewCatalog(f.Entries))
	return nil
}

// Due 说现在要不要联网刷新：关了不刷；没缓存或缓存过了 TTL 才刷；上次失败不到退避时间不刷
func (s *Store) Due() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.Enabled {
		return false
	}
	now := s.now()
	if !s.failedAt.IsZero() && now.Sub(s.failedAt) < failureBackoff {
		return false
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return s.fetchedAt.IsZero() || now.Sub(s.fetchedAt) >= ttl
}

// Refresh 联网取一次（条件请求）。force 是用户手动点刷新：不管 TTL 和退避。
// 失败时保留旧缓存、记错误、进入退避；返回错误只是给调用方记日志，不影响任何调用方
func (s *Store) Refresh(ctx context.Context, force bool) error {
	if !s.Enabled {
		return errors.New("模型资料库已关闭（modelMeta.modelsDev.enabled）")
	}
	if !force && !s.Due() {
		return nil
	}
	err := s.fetch(ctx)
	s.mu.Lock()
	if err != nil {
		s.lastErr, s.failedAt = err.Error(), s.now()
	} else {
		s.lastErr, s.failedAt = "", time.Time{}
	}
	s.mu.Unlock()
	return err
}

func (s *Store) client() (*http.Client, error) {
	if s.HTTP != nil {
		return s.HTTP, nil
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if s.Proxy != "" {
		u, err := url.Parse(s.Proxy)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("代理地址读不懂：%q", s.Proxy)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr, Timeout: requestTimeout}, nil
}

func (s *Store) fetch(ctx context.Context) error {
	target := s.URL
	if target == "" {
		target = DefaultURL
	}
	cl, err := s.client()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sumika-modelmeta")
	s.mu.Lock()
	etag := s.etag
	have := s.cat.Load() != nil
	s.mu.Unlock()
	if etag != "" && have {
		req.Header.Set("If-None-Match", etag)
	}
	res, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("连不上 models.dev：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified && have {
		s.mu.Lock()
		s.fetchedAt, s.fromCache = s.now(), false
		s.mu.Unlock()
		return s.save(nil) // 只更新时间
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("models.dev 返回 %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("读 models.dev 回复出错：%w", err)
	}
	if len(body) > maxBody {
		return fmt.Errorf("models.dev 回复超过 %d MB，当读不懂，保留旧缓存", maxBody>>20)
	}
	entries, err := decodeCatalog(body)
	if err != nil {
		return fmt.Errorf("models.dev 回复读不懂：%w，保留旧缓存", err)
	}
	if len(entries) == 0 {
		return errors.New("models.dev 回复里一个模型都没有，保留旧缓存")
	}
	cat := NewCatalog(entries)
	if cat.Count == 0 {
		return errors.New("models.dev 回复里没有可用的聊天模型，保留旧缓存")
	}
	s.mu.Lock()
	changed := !sameEntries(s.cat.Load(), cat)
	s.etag, s.fetchedAt, s.fromCache = res.Header.Get("ETag"), s.now(), false
	s.mu.Unlock()
	s.cat.Store(cat)
	if err := s.save(entries); err != nil {
		s.logf("模型资料缓存写不进去（下次启动要重新联网取）：%v", err)
	}
	if changed && s.OnChange != nil {
		s.OnChange()
	}
	return nil
}

func sameEntries(a, b *Catalog) bool {
	if a == nil || b == nil || a.Count != b.Count {
		return false
	}
	for k, av := range a.byName {
		bv := b.byName[k]
		if len(av) != len(bv) {
			return false
		}
		seen := map[Entry]int{}
		for _, e := range av {
			seen[e]++
		}
		for _, e := range bv {
			seen[e]--
		}
		for _, n := range seen {
			if n != 0 {
				return false
			}
		}
	}
	return true
}

// save 原子写缓存。entries 为 nil 时沿用现有内容、只更新时间和 ETag（304 的情况），那时要从内存快照重建
func (s *Store) save(entries []Entry) error {
	if s.Path == "" {
		return nil
	}
	if entries == nil {
		c := s.cat.Load()
		if c == nil {
			return nil
		}
		for _, list := range c.byName {
			entries = append(entries, list...)
		}
	}
	s.mu.Lock()
	f := cacheFile{V: cacheVersion, FetchedAt: s.fetchedAt.UnixMilli(), ETag: s.etag, Entries: entries}
	s.mu.Unlock()
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return fileatomic.Replace(tmp, s.Path, 2*time.Second)
}
