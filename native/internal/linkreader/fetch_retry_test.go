package linkreader

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// 推特有 fx、vx 两条线路，失败会换线路；这里只数打到同一条线路（api.fxtwitter.com）的次数，才是在看重试本身。

// 连接被断开（EOF）在同一条线路上重试一次就拿到；两次都断，报 network 并带上不含网址的底层原文。
func TestFetchRetriesDroppedConnectionOnce(t *testing.T) {
	body := fixture(t, "x-plain")
	fx := 0
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.fxtwitter.com" {
			return response(500, `{}`), nil
		}
		fx++
		if fx == 1 {
			return nil, io.EOF
		}
		return response(200, body), nil
	})
	if _, err := f.Fetch(context.Background(), link(t, "https://x.com/i/status/1900000000000000014")); err != nil || fx != 2 {
		t.Fatalf("同一线路断一次应重试拿到：fx=%d err=%v", fx, err)
	}

	f = testFetcher(t, func(req *http.Request) (*http.Response, error) { return nil, io.EOF })
	_, err := f.Fetch(context.Background(), link(t, "https://x.com/i/status/1900000000000000014"))
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Kind != "network" || fe.Detail == "" || strings.Contains(fe.Detail, "http") {
		t.Fatalf("都断应报 network 并带不含网址的底层原文：%+v", err)
	}
}

// 不是断连的错（对方拒了这类）同一条线路上不重试。
func TestFetchDoesNotRetryOtherErrors(t *testing.T) {
	fx := 0
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.fxtwitter.com" {
			fx++
		}
		return nil, errors.New("编的：被拒绝")
	})
	_, _ = f.Fetch(context.Background(), link(t, "https://x.com/i/status/1900000000000000014"))
	if fx != 1 {
		t.Fatalf("普通错误同一线路只该发一次：%d", fx)
	}
	if retryableNetErr(errors.New("编的：被拒绝")) || retryableNetErr(context.Canceled) || !retryableNetErr(io.EOF) {
		t.Fatal("可重试的判定不对")
	}
}
