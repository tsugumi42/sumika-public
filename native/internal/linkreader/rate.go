package linkreader

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RateOptions struct {
	RequestsPerMinute int
	// PerPlatform 按平台覆盖每分钟额度（键是 services 表里的平台名，例如 "bilibili"）；没写的平台用 RequestsPerMinute
	PerPlatform     map[string]int
	CooldownSeconds int
	// MaxBudgetWait 是这一分钟额度用满时最多排队等多久（主人 2026-11-13：自己定的额度不该把链接卡成失败，
	// 平台真拦了才停）。0 = 不等，额度满就报 rate_limited（测试和开发工具的旧行为）；冷却期间一律不等
	MaxBudgetWait time.Duration
	Now           func() time.Time
	// Sleep 等 d 或 ctx 结束；测试换成推进假时钟的版本。nil 用真计时器
	Sleep func(ctx context.Context, d time.Duration) error
}
type platformBudget struct {
	requests []time.Time
	until    time.Time
	// streak 是连续撞风控的次数，riskCool 用它让冷却时长翻倍；冷却过后成功取到一次才归零（riskReset）。
	streak int
	// riskUntil 是风控冷却的结束时刻（until 也可能是 429、频率码的冷却）；它没到之前，再报回来的风控是同一批在途请求，不再累加。
	riskUntil time.Time
}
type rateLimiter struct {
	mu        sync.Mutex
	options   RateOptions
	platforms map[string]*platformBudget
}

func newRateLimiter(o RateOptions) (*rateLimiter, error) {
	if o.RequestsPerMinute == 0 {
		o.RequestsPerMinute = 10
	}
	if o.CooldownSeconds == 0 {
		o.CooldownSeconds = 60
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	if o.RequestsPerMinute < 1 || o.CooldownSeconds < 1 || o.MaxBudgetWait < 0 {
		return nil, failure("invalid_options", "rate")
	}
	for _, n := range o.PerPlatform {
		if n < 1 {
			return nil, failure("invalid_options", "rate")
		}
	}
	return &rateLimiter{options: o, platforms: map[string]*platformBudget{}}, nil
}

// service 是限速的一行：哪些主机属于哪个平台，这个平台的 429 冷却怎么记。
// 预算（每分钟 N 次）永远按平台一份；冷却默认也按平台，splitCooldown 的平台改按主机冷却：
// 它名下的几个主机是互不相干的服务，一家回 429 不该挡住同时在取的另一家。
//   - X：fx、vx、官方嵌入接口各自限流，fx 回 429 以后“vx 成功就用 vx”（规格 C2）要能照常发（2026-11-12）。
//   - Steam：社区（steamcommunity.com）和商店、接口（steampowered.com）各自限流，
//     社区回 429 以后小黑盒游戏页还要去商店查游戏名，不能被一起挡住（样张复核 ST-07 → XH-08，2026-11-13）。
//
// 新平台、新的“一家回 429 连累另一家”事故只改这张表，不再往限速代码里加 if。
type service struct {
	platform      string
	suffixes      []string // 主机等于其中一项，或是它的子域
	exact         []string // 只认这几个主机本身，不含子域
	splitCooldown bool
	// riskBase 不为零的平台：遇到风控（HTTP 412、JSON 的 -352、-412）就整个平台冷却，
	// 连续撞一次翻倍，到 riskMax 为止（见 riskCool）；频率码（-509、-799）冷却 freqCool，不翻倍（见 freqCool）。
	// 这类风控按出口 IP 计，不按主机拆开。
	riskBase, riskMax, freqCool time.Duration
}

var services = []service{
	{platform: "bilibili", suffixes: []string{"b23.tv", "bili2233.cn", "bilibili.com"}, riskBase: 15 * time.Minute, riskMax: 2 * time.Hour, freqCool: 5 * time.Minute},
	{platform: "github", exact: []string{"api.github.com", "github.com", "gist.github.com"}},
	{platform: "youtube", suffixes: []string{"youtube.com", "youtu.be"}},
	{platform: "douyin", suffixes: []string{"douyin.com", "iesdouyin.com"}},
	{platform: "netease", suffixes: []string{"music.163.com"}, exact: []string{"163cn.tv"}},
	{platform: "booth", suffixes: []string{"booth.pm", "booth.pximg.net"}},
	{platform: "misskey", suffixes: []string{"misskey.io", "media.misskeyusercontent.jp"}},
	{platform: "mastodon", suffixes: []string{"mastodon.social", "files.mastodon.social"}},
	{platform: "tiktok", suffixes: []string{"tiktok.com"}},
	{platform: "pixiv", suffixes: []string{"pixiv.net"}},
	{platform: "bluesky", suffixes: []string{"bsky.app"}},
	{platform: "telegram", suffixes: []string{"t.me", "telegram.me", "cdn-telegram.org", "telesco.pe"}},
	{platform: "steam", suffixes: []string{"steampowered.com", "steamcommunity.com"}, splitCooldown: true},
	{platform: "afdian", suffixes: []string{"afdian.com", "afdian.net", "ifdian.net", "afdiancdn.com"}},
	{platform: "spotify", suffixes: []string{"spotify.com", "spotify.link", "scdn.co"}},
	{platform: "xiaoheihe", suffixes: []string{"xiaoheihe.cn"}},
	{platform: "xiaohongshu", suffixes: []string{"xiaohongshu.com", "xhslink.com", "xhslink.cn"}},
	{platform: "x", suffixes: []string{"x.com", "twitter.com", "t.co", "fxtwitter.com", "vxtwitter.com", "fixupx.com", "fixvx.com", "twimg.com"}, splitCooldown: true},
}

func (s service) has(host string) bool {
	for _, h := range s.exact {
		if host == h {
			return true
		}
	}
	return hostSuffix(host, s.suffixes...)
}

// webPrefix 是认不出平台的普通网页的键前缀。
const webPrefix = "web@"

// webKey：认不出平台的主机各记各的预算和冷却，不并入任何平台，一波普通网页耗不掉 X 的每分钟预算。
// 归法：键是小写主机名（不含端口），只去掉开头的 www.；子域各算各的（a.example.com 和 b.example.com 是两份），
// 因为它们常常是互不相干的服务；example.com 和 www.example.com 是同一份。
func webKey(host string) string {
	return webPrefix + strings.TrimPrefix(host, "www.")
}

// requestPlatform 认出请求属于哪个平台；认不出的是 webKey(主机)。
func requestPlatform(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return webKey("")
	}
	host := strings.ToLower(u.Hostname())
	for _, s := range services {
		if s.has(host) {
			return s.platform
		}
	}
	return webKey(host)
}

func splitsCooldown(platform string) bool {
	for _, s := range services {
		if s.platform == platform {
			return s.splitCooldown
		}
	}
	return false
}

// sweepAt：表里的项超过这个数就清一遍已经没用的（窗口里没有请求、冷却也过了），
// 免得普通网页主机一多，表一直涨。
const sweepAt = 256

func (r *rateLimiter) sweepLocked(now time.Time) {
	for k, b := range r.platforms {
		if now.Before(b.until) {
			continue
		}
		live := false
		for _, at := range b.requests {
			if now.Before(at.Add(time.Minute)) {
				live = true
				break
			}
		}
		// 连续风控的计数也要留住，否则过了冷却就被清掉，下一次又从头算
		if !live && b.streak == 0 {
			delete(r.platforms, k)
		}
	}
}

func (r *rateLimiter) state(platform string) *platformBudget {
	b := r.platforms[platform]
	if b == nil {
		if len(r.platforms) >= sweepAt {
			r.sweepLocked(r.options.Now())
		}
		b = &platformBudget{}
		r.platforms[platform] = b
	}
	return b
}

// hostCoolKey 是 429 冷却按主机记的那一份：只有 service.splitCooldown 的平台有，别的平台返回空，按平台冷却。
func hostCoolKey(platform, host string) string {
	if !splitsCooldown(platform) || host == "" {
		return ""
	}
	return platform + "@" + strings.ToLower(host)
}

// allowHost 是带主机的 allow：先看这台主机自己的冷却，再照平台的冷却和预算。
func (r *rateLimiter) allowHost(platform, host string, consume bool) error {
	return r.check(platform, hostCoolKey(platform, host), consume)
}

// cooldownHost 记一次 429：拆开冷却的平台只冷却那台主机，其他平台冷却整个平台。
func (r *rateLimiter) cooldownHost(platform, host, retry string) {
	if k := hostCoolKey(platform, host); k != "" {
		r.cooldown(k, retry)
		return
	}
	r.cooldown(platform, retry)
}

func (r *rateLimiter) allow(platform string, consume bool) error {
	return r.check(platform, "", consume)
}

func (r *rateLimiter) check(platform, hostKey string, consume bool) error {
	_, err := r.checkWait(platform, hostKey, consume)
	return err
}

// limit 是这个平台每分钟的额度：PerPlatform 里写了就用它，否则用 RequestsPerMinute
func (r *rateLimiter) limit(platform string) int {
	if n, ok := r.options.PerPlatform[platform]; ok {
		return n
	}
	return r.options.RequestsPerMinute
}

// checkWait 和 check 一样，另外在“只是这一分钟的额度用满了”（不是冷却）时给出要等多久才腾出一个空位；冷却和通过时 wait 为 0
func (r *rateLimiter) checkWait(platform, hostKey string, consume bool) (time.Duration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.options.Now()
	// 只读不建：x 名下兜底的普通网页主机很多，没冷却过的主机不留表项
	if hb, ok := r.platforms[hostKey]; hostKey != "" && ok && now.Before(hb.until) {
		return 0, failure("rate_limited", platform)
	}
	b := r.state(platform)
	if now.Before(b.until) {
		return 0, failure("rate_limited", platform)
	}
	n := 0
	for _, at := range b.requests {
		if now.Before(at.Add(time.Minute)) {
			b.requests[n] = at
			n++
		}
	}
	b.requests = b.requests[:n]
	if n >= r.limit(platform) {
		// 窗口里最早的那次满一分钟就腾出一个空位（requests 按时间先后追加）
		return b.requests[0].Add(time.Minute).Sub(now), failure("rate_limited", platform)
	}
	if consume {
		b.requests = append(b.requests, now)
	}
	return 0, nil
}

// waitAllow 是会排队的 allowHost：额度用满时等空位再试，冷却、等不到（超过 MaxBudgetWait 或 ctx 先到期）就照旧报 rate_limited
func (r *rateLimiter) waitAllow(ctx context.Context, platform, host string, consume bool) error {
	key := hostCoolKey(platform, host)
	wait, err := r.checkWait(platform, key, consume)
	if err == nil || wait <= 0 || r.options.MaxBudgetWait <= 0 {
		return err
	}
	deadline := r.options.Now().Add(r.options.MaxBudgetWait)
	for err != nil && wait > 0 {
		now := r.options.Now()
		if now.Add(wait).After(deadline) {
			return err
		}
		if dl, ok := ctx.Deadline(); ok && now.Add(wait).After(dl) {
			return err
		}
		if r.options.Sleep(ctx, wait) != nil {
			return err
		}
		wait, err = r.checkWait(platform, key, consume)
	}
	return err
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// spare 看这一分钟的预算还剩不剩得多：剩余请求数多于 keep 才为真，不扣预算。
// 评论、统计这类可有可无的请求用它先问一句，预算紧时让给后面链接的主请求。
func (r *rateLimiter) spare(platform string, keep int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.options.Now()
	b := r.state(platform)
	if now.Before(b.until) {
		return false
	}
	n := 0
	for _, at := range b.requests {
		if now.Before(at.Add(time.Minute)) {
			n++
		}
	}
	return r.limit(platform)-n > keep
}

// MaxRetryAfter 是对端 Retry-After 最多能让一个键冷却多久。对端回一个很大的值（或坏掉的值）时，
// 没有上限会让这个平台一直冷却到重启；超过上限的按上限算，到点再试一次，还限流就再冷却一轮。
const MaxRetryAfter = 10 * time.Minute

// githubResetCap 是 GitHub 匿名额度“耗尽后等 reset”的上限：额度窗口本身是一小时，reset 时间戳最远就是一小时后，
// 不能按 MaxRetryAfter 截短，否则每 10 分钟白撞一次 403。
const githubResetCap = time.Hour

// retryAfterUntil 把 Retry-After（秒数或 HTTP 日期）换成冷却到的时刻。
// 超过 limit 的按 limit 算（包括写不进 int64 的超长数字）；负数、小数、空、乱值、过去的日期返回 false，调用方按默认冷却。
func retryAfterUntil(now time.Time, retry string, limit time.Duration) (time.Time, bool) {
	retry = strings.TrimSpace(retry)
	if retry == "" {
		return time.Time{}, false
	}
	capAt := now.Add(limit)
	if seconds, err := strconv.ParseInt(retry, 10, 64); err == nil {
		if seconds < 0 {
			return time.Time{}, false
		}
		if seconds >= int64(limit/time.Second) {
			return capAt, true
		}
		return now.Add(time.Duration(seconds) * time.Second), true
	} else if errors.Is(err, strconv.ErrRange) && retry[0] != '-' {
		return capAt, true
	}
	if at, err := http.ParseTime(retry); err == nil && at.After(now) {
		if at.After(capAt) {
			return capAt, true
		}
		return at, true
	}
	return time.Time{}, false
}

func (r *rateLimiter) cooldown(platform, retry string) {
	r.cooldownCapped(platform, retry, MaxRetryAfter)
}

// riskCool 记一次风控：整个平台冷却，时长从 riskBase 起，每连撞一次翻倍，到 riskMax 为止。
// 风控冷却期内再报回来的（同一批在途请求会陆续返回）不累加，不算新的一轮。对端给了 Retry-After 且比退避时长长就取它（上限 riskMax）。
// 没有 riskBase 的平台什么也不做，照原来的 429 冷却走。
func (r *rateLimiter) riskCool(platform, retry string) {
	s, ok := serviceOf(platform)
	if !ok || s.riskBase == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.options.Now()
	b := r.state(platform)
	if now.Before(b.riskUntil) {
		return
	}
	if b.streak < 16 { // 翻倍到封顶后不必再数，防止溢出
		b.streak++
	}
	d := s.riskBase
	for i := 1; i < b.streak && d < s.riskMax; i++ {
		d *= 2
	}
	if d > s.riskMax {
		d = s.riskMax
	}
	until := now.Add(d)
	if at, ok := retryAfterUntil(now, retry, s.riskMax); ok && at.After(until) {
		until = at
	}
	b.riskUntil = until
	if until.After(b.until) {
		b.until = until
	}
}

// freqCool 记一次频率码（-509、-799）：平台冷却 freqCool，不翻倍、不算风控的连续次数。
// 它是“这一阵请求太密”，不是 IP 被标记，所以比风控短；已经在更长的冷却里就不缩短。
func (r *rateLimiter) freqCool(platform, retry string) {
	s, ok := serviceOf(platform)
	if !ok || s.freqCool == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.options.Now()
	until := now.Add(s.freqCool)
	if at, ok := retryAfterUntil(now, retry, s.riskMax); ok && at.After(until) {
		until = at
	}
	if b := r.state(platform); until.After(b.until) {
		b.until = until
	}
}

// riskReset 平台在风控冷却结束后成功取到一次数据：连续风控的计数归零，下一次风控从 riskBase 重新起算。
// 冷却期内才返回的成功（冷却前发出的在途请求）不算，免得一次迟到的成功把退避清掉。
func (r *rateLimiter) riskReset(platform string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.platforms[platform]
	if b != nil && !r.options.Now().Before(b.riskUntil) {
		b.streak = 0
	}
}

func serviceOf(platform string) (service, bool) {
	for _, s := range services {
		if s.platform == platform {
			return s, true
		}
	}
	return service{}, false
}

func (r *rateLimiter) cooldownCapped(platform, retry string, limit time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.options.Now()
	until := now.Add(time.Duration(r.options.CooldownSeconds) * time.Second)
	if at, ok := retryAfterUntil(now, retry, limit); ok && at.After(until) {
		until = at
	}
	b := r.state(platform)
	if until.After(b.until) {
		b.until = until
	}
}
