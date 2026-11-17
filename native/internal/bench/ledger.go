package bench

// 网关账本（规格-表二执行器 第二、三节）：跑完用 magpie usage --csv 取记录，按记账代理打的会话头对回到每一次。
// X-Magpie-Session 会盖过客户端自己的会话头（omp 的 x-session-affinity 退到 native_session，Magpie usage.go），三个壳口径一样

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GatewayCall 是网关记的一次请求（CSV 的一行）
type GatewayCall struct {
	Time       time.Time `json:"time"`
	Session    string    `json:"session"`
	RouteID    string    `json:"routeId"`
	Status     int       `json:"status"` // 499 是客户端中途取消（停止、超时）
	Provider   string    `json:"provider"`
	Key        string    `json:"key"`   // 走的哪个号（provider_key_name）
	Model      string    `json:"model"` // 实际答的模型（served_model）
	Input      int64     `json:"input"`
	Output     int64     `json:"output"`
	CacheRead  int64     `json:"cacheRead"`
	CacheWrite int64     `json:"cacheWrite"`
	Reasoning  int64     `json:"reasoning"`
	TTFTMs     int64     `json:"ttftMs"` // 取消前没到首字的为 0
	DurationMs int64     `json:"durationMs"`
	Tries      int       `json:"tries"` // 网关替它试了几次（空的算 1）
	Error      string    `json:"error,omitempty"`
	// Stop 是服务商给的结束原因（CSV 的 upstream_stop：stop、length、tool_calls……）；CSV 没这一列或这行为空时是空串
	Stop string `json:"stop,omitempty"`
}

var ledgerCols = []string{"time", "session", "route_id", "status", "provider", "provider_key_name", "served_model",
	"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens",
	"ttft_ms", "duration_ms", "tries", "error", "error_message", "source", "rejected"}

// ReadLedger 读 magpie usage --csv 的输出，只留会话以 prefix 开头、网关自己转发的（source 为空；别的是从各家日志导进来的）、
// 没被拒的；同一 route_id 的几行留时间最晚的（结局）；按会话分组，组内按时间排（CSV 大体倒序，时区有 Z 有 +08:00，解析后再比）
func ReadLedger(r io.Reader, prefix string) (map[string][]GatewayCall, error) {
	cr := csv.NewReader(r)
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("读表头：%w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[h] = i
	}
	var missing []string
	for _, c := range ledgerCols {
		if _, ok := col[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("magpie usage 的 CSV 少了列：%s", strings.Join(missing, "、"))
	}
	byRoute := map[string]int{}
	var calls []GatewayCall
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		get := func(c string) string { return rec[col[c]] }
		if !strings.HasPrefix(get("session"), prefix) || get("source") != "" || get("rejected") == "true" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, get("time"))
		if err != nil {
			return nil, fmt.Errorf("时间 %q：%w", get("time"), err)
		}
		num := func(c string) int64 { n, _ := strconv.ParseInt(get(c), 10, 64); return n }
		c := GatewayCall{Time: t, Session: get("session"), RouteID: get("route_id"), Status: int(num("status")),
			Provider: get("provider"), Key: get("provider_key_name"), Model: get("served_model"),
			Input: num("input_tokens"), Output: num("output_tokens"), CacheRead: num("cache_read_tokens"),
			CacheWrite: num("cache_write_tokens"), Reasoning: num("reasoning_tokens"),
			TTFTMs: num("ttft_ms"), DurationMs: num("duration_ms"), Tries: max(1, int(num("tries"))),
			Error: get("error_message")}
		if c.Error == "" {
			c.Error = get("error")
		}
		if i, ok := col["upstream_stop"]; ok {
			c.Stop = rec[i]
		}
		if i, ok := byRoute[c.RouteID]; ok && c.RouteID != "" {
			if !c.Time.Before(calls[i].Time) {
				calls[i] = c
			}
			continue
		}
		if c.RouteID != "" {
			byRoute[c.RouteID] = len(calls)
		}
		calls = append(calls, c)
	}
	sort.SliceStable(calls, func(i, j int) bool { return calls[i].Time.Before(calls[j].Time) })
	out := map[string][]GatewayCall{}
	for _, c := range calls {
		out[c.Session] = append(out[c.Session], c)
	}
	return out, nil
}

// FilterLedger 只留会话以 prefix 开头的行（连表头），存进结果目录备查、出了问题能重算：账本里还有别的程序的请求，不往外带
func FilterLedger(raw []byte, prefix string) ([]byte, error) {
	cr := csv.NewReader(bytes.NewReader(raw))
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("读表头：%w", err)
	}
	si := slices.Index(head, "session")
	if si < 0 {
		return nil, fmt.Errorf("magpie usage 的 CSV 少了列：session")
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	cw.Write(head)
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(rec[si], prefix) {
			cw.Write(rec)
		}
	}
	cw.Flush()
	return buf.Bytes(), cw.Error()
}

// FetchLedger 跑 magpie usage --csv --last <从 since 到现在，再多两分钟>（它没有 --since，只能取到现在为止的一段）
func FetchLedger(ctx context.Context, since time.Time) ([]byte, error) {
	mins := int(time.Since(since).Minutes()) + 2
	out, err := exec.CommandContext(ctx, "magpie", "usage", "--csv", "--last", fmt.Sprintf("%dm", mins)).Output()
	if err != nil {
		return nil, fmt.Errorf("magpie usage：%w", err)
	}
	return out, nil
}
