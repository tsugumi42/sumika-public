// Package freeapi 是免费联网接口的注册表、健康状态、排序、回退、探测和次数计数（规格-免费接口）。
//
// 它不知道天气、节假日、汇率长什么样：每类问题的请求和解析由调用方（lifetools）写成函数交进来，
// 这里只管“哪几家、先试谁、挂了换谁、记下结果、每个人和每个群用了多少次”。
// 日志只写类别、提供方 id、结局、毫秒，不写地点、网址和用户号。
package freeapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Kind 是一次尝试的结局类别
type Kind string

const (
	KOK       Kind = "ok"
	KTimeout  Kind = "timeout"
	KNet      Kind = "net"      // 连接失败、DNS、读到一半断了
	K5xx      Kind = "5xx"      // 对方服务器错
	KRate     Kind = "429"      // 对方说太频繁
	KAuth     Kind = "auth"     // 401、403：要人处理
	KBlocked  Kind = "cf"       // 被挑战页拦了
	KSchema   Kind = "schema"   // 200 但内容不对，或别的意外状态码
	KTooLarge Kind = "size"     // 响应体超限
	KNotFound Kind = "notfound" // 业务上查无结果，不是提供方的错
	KCanceled Kind = "canceled" // 调用方自己取消，不记
)

// ErrNotFound 是提供方回了“查无此地 / 查无这一年 / 没有这个币种”这类业务结果。拿 errors.Is 判
var ErrNotFound = errors.New("查无结果")

// ErrTooLarge 是响应体超过上限
var ErrTooLarge = errors.New("响应太大")

// StatusError 是服务器回了不是 200 的状态
type StatusError struct {
	Code       int
	RetryAfter time.Duration // 429 时对方给的等待
	Server     string
	Challenge  bool // 看起来是 Cloudflare 一类的挑战页
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Code) }

// SchemaError 是 200 但解析不出要的字段
type SchemaError struct{ Msg string }

func (e *SchemaError) Error() string { return "返回内容不对：" + e.Msg }

// Schema 造一个 SchemaError
func Schema(format string, a ...any) error { return &SchemaError{Msg: fmt.Sprintf(format, a...)} }

// Classify 给错误归类，并带回对方要求的等待（只有 429 有）
func Classify(err error) (Kind, time.Duration) {
	var se *StatusError
	var sc *SchemaError
	var ne net.Error
	switch {
	case err == nil:
		return KOK, 0
	case errors.Is(err, ErrNotFound):
		return KNotFound, 0
	case errors.Is(err, ErrTooLarge):
		return KTooLarge, 0
	case errors.Is(err, context.Canceled):
		return KCanceled, 0
	case errors.Is(err, context.DeadlineExceeded):
		return KTimeout, 0
	case errors.As(err, &se):
		switch {
		case se.Code == 429:
			return KRate, se.RetryAfter
		case se.Challenge && (se.Code == 403 || se.Code == 503):
			return KBlocked, 0
		case se.Code == 401 || se.Code == 403:
			return KAuth, 0
		case se.Code >= 500:
			return K5xx, 0
		}
		return KSchema, 0 // 别的 4xx、3xx：接口变了
	case errors.As(err, &sc):
		return KSchema, 0
	case errors.As(err, &ne) && ne.Timeout():
		return KTimeout, 0
	}
	return KNet, 0
}

// Get 取一个地址，最多读 max 字节。非 200 返回 *StatusError，超过上限返回 ErrTooLarge。
// 不跟随跳转由调用方的客户端保证。headers 里的 User-Agent 缺省时补一个通用的
func Get(ctx context.Context, cl *http.Client, rawURL string, headers map[string]string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (sumika)")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		se := &StatusError{Code: resp.StatusCode, Server: resp.Header.Get("Server"), RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		low := strings.ToLower(string(snippet))
		se.Challenge = strings.Contains(strings.ToLower(se.Server), "cloudflare") && (resp.StatusCode == 403 || resp.StatusCode == 503) ||
			strings.Contains(low, "just a moment") || strings.Contains(low, "cf-chl") || strings.Contains(low, "attention required")
		return nil, se
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrTooLarge
	}
	return b, nil
}

// parseRetryAfter 认秒数和日期两种写法；封顶 24 小时，认不出是 0
func parseRetryAfter(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(s); err == nil {
		d = time.Until(t)
	}
	if d < 0 {
		return 0
	}
	if d > 24*time.Hour {
		d = 24 * time.Hour
	}
	return d
}
