package linkreader

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// 主人 2026-11-13：自己定的每分钟额度不该把链接卡成失败，平台真拦了才停。
// 额度满了排队等空位（MaxBudgetWait 以内），冷却期间、等不到的照旧立刻报 rate_limited。

type fakeRateClock struct {
	now   time.Time
	slept []time.Duration
}

func (c *fakeRateClock) Now() time.Time { return c.now }
func (c *fakeRateClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	return nil
}

func waitFetcher(t *testing.T, clock *fakeRateClock, rate RateOptions, status int) (*Fetcher, *int) {
	t.Helper()
	calls := 0
	rate.Now, rate.Sleep = clock.Now, clock.Sleep
	f, err := NewFetcher(FetchOptions{Rate: rate, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(status, `{}`), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return f, &calls
}

func TestBudgetFullWaitsForSlot(t *testing.T) {
	clock := &fakeRateClock{now: time.Date(2026, 11, 13, 14, 27, 0, 0, time.UTC)}
	f, calls := waitFetcher(t, clock, RateOptions{RequestsPerMinute: 2, MaxBudgetWait: 45 * time.Second}, 200)
	get := func() error {
		_, _, _, e := f.request(context.Background(), "https://www.pixiv.net/ajax/illust/1", "pixiv")
		return e
	}
	if err := get(); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(10 * time.Second)
	if err := get(); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(10 * time.Second) // 第三个在 20 秒时来：最早那次 60 秒时腾出空位，要等 40 秒
	if err := get(); err != nil {
		t.Fatal("额度满了应该排队等空位，不该失败：", err)
	}
	if *calls != 3 || len(clock.slept) != 1 || clock.slept[0] != 40*time.Second {
		t.Fatal("等待不对", *calls, clock.slept)
	}
}

func TestBudgetWaitGivesUpBeyondMax(t *testing.T) {
	clock := &fakeRateClock{now: time.Date(2026, 11, 13, 14, 27, 0, 0, time.UTC)}
	f, calls := waitFetcher(t, clock, RateOptions{RequestsPerMinute: 1, MaxBudgetWait: 10 * time.Second}, 200)
	get := func() error {
		_, _, _, e := f.request(context.Background(), "https://www.pixiv.net/ajax/illust/1", "pixiv")
		return e
	}
	if err := get(); err != nil {
		t.Fatal(err)
	}
	wantError(t, get(), "rate_limited") // 要等 60 秒，超过 10 秒上限：不等
	if *calls != 1 || len(clock.slept) != 0 {
		t.Fatal("超过上限不该等也不该发", *calls, clock.slept)
	}
}

func TestBudgetWaitRespectsContextDeadline(t *testing.T) {
	clock := &fakeRateClock{now: time.Now()}
	f, calls := waitFetcher(t, clock, RateOptions{RequestsPerMinute: 1, MaxBudgetWait: 2 * time.Minute}, 200)
	if _, _, _, err := f.request(context.Background(), "https://www.pixiv.net/ajax/illust/1", "pixiv"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), clock.now.Add(5*time.Second))
	defer cancel()
	_, _, _, err := f.request(ctx, "https://www.pixiv.net/ajax/illust/1", "pixiv")
	wantError(t, err, "rate_limited") // 回合时限 5 秒内等不到空位：不白等
	if *calls != 1 || len(clock.slept) != 0 {
		t.Fatal("时限内等不到不该等", *calls, clock.slept)
	}
}

func TestCooldownDoesNotWait(t *testing.T) {
	clock := &fakeRateClock{now: time.Date(2026, 11, 13, 14, 27, 0, 0, time.UTC)}
	f, calls := waitFetcher(t, clock, RateOptions{RequestsPerMinute: 10, MaxBudgetWait: 45 * time.Second}, http.StatusTooManyRequests)
	get := func() error {
		_, _, _, e := f.request(context.Background(), "https://www.pixiv.net/ajax/illust/1", "pixiv")
		return e
	}
	_ = get() // 回 429，平台进冷却
	wantError(t, get(), "rate_limited")
	if *calls != 1 || len(clock.slept) != 0 {
		t.Fatal("平台真拦了要立刻停，不排队", *calls, clock.slept)
	}
}

func TestPerPlatformLimit(t *testing.T) {
	clock := &fakeRateClock{now: time.Date(2026, 11, 13, 14, 27, 0, 0, time.UTC)}
	f, calls := waitFetcher(t, clock, RateOptions{RequestsPerMinute: 3, PerPlatform: map[string]int{"bilibili": 1}}, 200)
	get := func(raw string) error { _, _, _, e := f.request(context.Background(), raw, "offline"); return e }
	if err := get("https://b23.tv/one"); err != nil {
		t.Fatal(err)
	}
	wantError(t, get("https://b23.tv/two"), "rate_limited") // bilibili 单独 1 次
	for i := 0; i < 3; i++ {
		if err := get("https://www.pixiv.net/ajax/illust/1"); err != nil {
			t.Fatal("pixiv 用默认额度 3：", i, err)
		}
	}
	wantError(t, get("https://www.pixiv.net/ajax/illust/1"), "rate_limited")
	if *calls != 4 {
		t.Fatal(*calls)
	}
}

func TestRateOptionsRejectBadPerPlatform(t *testing.T) {
	if _, err := newRateLimiter(RateOptions{PerPlatform: map[string]int{"pixiv": 0}}); err == nil {
		t.Fatal("按平台额度 0 应该拒绝")
	}
	if _, err := newRateLimiter(RateOptions{MaxBudgetWait: -time.Second}); err == nil {
		t.Fatal("负的等待上限应该拒绝")
	}
}
