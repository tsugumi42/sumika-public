package llm

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFailoverCooldownChainAndCancellation(t *testing.T) {
	now := time.Unix(100, 0)
	f := &Failover{Now: func() time.Time { return now }}
	failure := io.ErrUnexpectedEOF
	p := f.Plan("primary", []string{"primary", "backup", "third", "backup"})
	if p.Model != "primary" || !p.Next(context.Background(), failure) || p.Model != "backup" || p.Info.PreviousModel != "primary" || p.Info.Reason != ClassNetwork {
		t.Fatalf("first switch: %+v", p)
	}
	if !p.Next(context.Background(), failure) || p.Model != "third" || p.Next(context.Background(), failure) {
		t.Fatalf("full chain: %+v", p)
	}
	if err := p.Err(failure); !errors.Is(err, failure) || !strings.Contains(err.Error(), "primary、backup、third") || !strings.Contains(err.Error(), "模型故障切换") {
		t.Fatalf("chain error: %v", err)
	}
	all := f.Plan("primary", []string{"backup", "third"})
	if all.Model != "" || all.Err(nil) == nil || !strings.Contains(all.Err(nil).Error(), "primary、backup、third") || !strings.Contains(all.Err(nil).Error(), "都在冷却") {
		t.Fatalf("cooling: %+v %v", all, all.Err(nil))
	}
	now = now.Add(time.Minute)
	again := f.Plan("primary", []string{"backup"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if again.Model != "primary" || again.Next(ctx, failure) {
		t.Fatalf("cancel: %+v", again)
	}
	if f.Plan("primary", nil).Err(failure) != failure {
		t.Fatal("empty chain changed error")
	}
	bad := &StatusError{Code: 400, Body: "bad"}
	q := f.Plan("primary", []string{"backup"})
	if q.Next(context.Background(), bad) || q.Exhausted(bad) != bad {
		t.Fatalf("bad request switched or wrapped: %v", q.Exhausted(bad))
	}
}

func TestFailoverConcurrentCooldown(t *testing.T) {
	f := &Failover{Now: func() time.Time { return time.Unix(1, 0) }}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := f.Plan("primary", []string{"backup"})
			if p.Model != "" {
				p.Next(context.Background(), io.EOF)
			}
		}()
	}
	wg.Wait()
	if p := f.Plan("primary", []string{"backup"}); p.Model != "" {
		t.Fatalf("models not cooling: %+v", p)
	}
}

func TestFailoverClassification(t *testing.T) {
	for _, tc := range []struct {
		data     string
		class    string
		eligible bool
	}{
		{`{"error":{"type":"overloaded_error"}}`, ClassServerError, true},
		{`{"error":{"type":"invalid_request_error"}}`, ClassBadRequest, false},
		{`{"error":{"code":"model_unavailable"}}`, ClassModelUnknown, true},
		{`{"error":{"type":"mystery"}}`, ClassStreamError, false},
		{`{"error":{"type":"api_error"}}`, ClassStreamError, true},
	} {
		e := &StreamError{Data: []byte(tc.data), Message: tc.data}
		if c := ClassifyError(e); c != tc.class || FailoverEligible(context.Background(), e) != tc.eligible {
			t.Errorf("%s: %s eligible=%v", tc.data, c, FailoverEligible(context.Background(), e))
		}
	}
	if FailoverEligible(context.Background(), errors.Join(io.EOF, LocalError(errors.New("record failed")))) {
		t.Fatal("local error allowed switch")
	}
	if FailoverEligible(context.Background(), &StopError{Reason: "unexpected"}) {
		t.Fatal("stop allowed switch")
	}
}

// OpenAI 兼容服务的模型不存在、密钥不对也带 type=invalid_request_error：要按具体原因认，才会切换。
func TestClassifySpecificBeforeBadRequest(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
		want string
		swap bool
	}{
		{404, `{"error":{"type":"invalid_request_error","code":"model_not_found"}}`, ClassModelUnknown, true},
		{401, `{"error":{"type":"invalid_request_error","code":"invalid_api_key"}}`, ClassAuth, true},
		{400, `{"error":{"type":"invalid_request_error","code":"bad_param"}}`, ClassBadRequest, false},
	} {
		err := &StatusError{Code: tc.code, Body: tc.body, RawBody: tc.body}
		if got := ClassifyError(err); got != tc.want {
			t.Errorf("%s：归成 %q，想要 %q", tc.body, got, tc.want)
		}
		if got := FailoverEligible(context.Background(), err); got != tc.swap {
			t.Errorf("%s：可换=%v，想要 %v", tc.body, got, tc.swap)
		}
	}
}

// 切换过一次后，备选的请求本身错误不包成“故障切换”，只有真的走完链才说试过谁。
func TestExhaustedKeepsRequestErrors(t *testing.T) {
	f := &Failover{}
	p := f.Plan("主", []string{"备"})
	if !p.Next(context.Background(), &StatusError{Code: 500, Body: "x"}) {
		t.Fatal("500 该换")
	}
	bad := &StatusError{Code: 400, Body: `{"error":{"type":"invalid_request_error"}}`}
	if got := p.Exhausted(bad); got != error(bad) {
		t.Fatalf("请求本身的错要原样：%v", got)
	}
	server := &StatusError{Code: 500, Body: "x"}
	if p.Next(context.Background(), server) {
		t.Fatal("链走完了")
	}
	if got := p.Exhausted(server); got == error(server) || !errors.Is(got, server) {
		t.Fatalf("走完链要说明并保留原错误：%v", got)
	}
}
