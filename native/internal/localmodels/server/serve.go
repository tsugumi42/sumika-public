package server

import (
	"log"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/netutil"
)

// Limits 是监听的限额（规格 2.5"监听"一行）。令牌检查在读完请求头之后，不设上限的话本机任何程序都能慢速占住连接。
type Limits struct {
	ReadHeaderTimeout time.Duration // 读请求头最多多久
	IdleTimeout       time.Duration // 保持连接空闲最多多久（占着同时连接数的名额）
	MaxConns          int           // 同时连接数；第 N+1 个排队，等前面有人走
}

// DefaultLimits 是生产值：请求头 10 秒、同时 32 个连接。
func DefaultLimits() Limits {
	return Limits{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxConns: 32}
}

// NewHTTPServer 建 http.Server。不设 ReadTimeout 和 WriteTimeout：它们是整个服务一个值，而一次请求的算多久由 --max-request-seconds 管
// （ReadTimeout 不能比它短，WriteTimeout 更会把长批量的响应截断）；读正文的期限由 readBody 按请求自己设，读请求头的期限是 ReadHeaderTimeout。
func NewHTTPServer(h http.Handler, l Limits, errLog *log.Logger) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: l.ReadHeaderTimeout,
		IdleTimeout:       l.IdleTimeout,
		ErrorLog:          errLog,
	}
}

// Serve 在 ln 上提供服务，同时连接数不超过 l.MaxConns；srv.Shutdown 或 Close 之后返回 http.ErrServerClosed。
func Serve(srv *http.Server, ln net.Listener, l Limits) error {
	if l.MaxConns > 0 {
		ln = netutil.LimitListener(ln, l.MaxConns)
	}
	return srv.Serve(ln)
}
