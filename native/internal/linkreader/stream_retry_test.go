package linkreader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
)

// 媒体下载还没收到响应头就被断开（EOF、连接重置）：同一地址重试一次就拿到图；
// 两次都断报 network，Detail 带上不含网址的底层原文（2026-11-11 日志里四次“下载中断”查不到原因）。
func TestStreamRetriesDroppedConnectionOnce(t *testing.T) {
	for _, drop := range []error{io.EOF, syscall.ECONNRESET} {
		hits := 0
		f, e := NewFetcher(FetchOptions{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits++
			if hits == 1 {
				return nil, drop
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: 3, Body: io.NopCloser(strings.NewReader("png"))}, nil
		})})
		if e != nil {
			t.Fatal(e)
		}
		var dst bytes.Buffer
		if e = f.Stream(context.Background(), "x", "https://pbs.twimg.com/media/fiction.png", &dst, &ByteBudget{Limit: 32}); e != nil || hits != 2 || dst.String() != "png" {
			t.Fatalf("%v：断一次应重试拿到：hits=%d wrote=%q err=%v", drop, hits, dst.String(), e)
		}
	}

	f, _ := NewFetcher(FetchOptions{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, io.EOF })})
	e := f.Stream(context.Background(), "x", "https://pbs.twimg.com/media/fiction.png?token=编的", io.Discard, &ByteBudget{Limit: 32})
	var fe *FetchError
	if !errors.As(e, &fe) || fe.Kind != "network" || fe.Detail == "" || strings.Contains(fe.Detail, "http") || strings.Contains(fe.Detail, "token") {
		t.Fatalf("都断应报 network 并带不含网址的底层原文：%+v", e)
	}
}

// 不是断连的错不重试，免得被对方拒绝时打两次。
func TestStreamDoesNotRetryOtherErrors(t *testing.T) {
	hits := 0
	f, _ := NewFetcher(FetchOptions{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits++
		return nil, errors.New("编的：被拒绝")
	})})
	_ = f.Stream(context.Background(), "x", "https://pbs.twimg.com/media/fiction.png", io.Discard, &ByteBudget{Limit: 32})
	if hits != 1 {
		t.Fatalf("普通错误只该发一次：%d", hits)
	}
}
