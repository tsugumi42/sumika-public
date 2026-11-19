package main

import (
	"fmt"
	"strings"
)

type requestRecord struct {
	Session string
	Body    string
}
type counts struct{ Turns, Messages, Steps int }
type scalePoint struct {
	Sessions           int
	Memory, Goroutines float64
}

func isolation(records []requestRecord, secrets map[string]string) []string {
	var failures []string
	for i, r := range records {
		for session, secret := range secrets {
			if session != r.Session && strings.Contains(r.Body, secret) {
				failures = append(failures, fmt.Sprintf("请求 %d：%s 的暗号出现在 %s", i+1, session, r.Session))
			}
		}
	}
	return failures
}
func accounting(want, got map[string]counts) []string {
	var failures []string
	for session, expected := range want {
		if actual, ok := got[session]; !ok || actual != expected {
			failures = append(failures, fmt.Sprintf("%s：预期 %+v，实际 %+v", session, expected, actual))
		}
	}
	for session := range got {
		if _, ok := want[session]; !ok {
			failures = append(failures, "多出的会话："+session)
		}
	}
	return failures
}

// 暂定：扣除各档启动基线后，每会话的峰值开销不得超过最小档的两倍。
func linear(points []scalePoint) bool {
	if len(points) < 2 {
		return false
	}
	base := points[0]
	if base.Sessions <= 0 || base.Memory <= 0 || base.Goroutines <= 0 {
		return false
	}
	for _, p := range points[1:] {
		if p.Sessions <= 0 || p.Memory < 0 || p.Goroutines < 0 || p.Memory/float64(p.Sessions) > 2*base.Memory/float64(base.Sessions) || p.Goroutines/float64(p.Sessions) > 2*base.Goroutines/float64(base.Sessions) {
			return false
		}
	}
	return true
}
