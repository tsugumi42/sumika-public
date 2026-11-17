package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 流里的怪块：error 是 null 不算出错；字段类型不对（content 是数组）只当没有，不断流；注释、event 行、CRLF 不影响
func TestStreamToleratesOddChunks(t *testing.T) {
	res, err := streamRaw(t, time.Minute, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keepalive\r\n\r\n")
		fmt.Fprint(w, "event: message\r\ndata: {\"error\":null,\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\r\n\r\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":[1,2],\"reasoning\":7}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"的\"},\"finish_reason\":\"stop\"}],\"usage\":null}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	if err != nil || res.Text != "好的" || res.Stop != "stop" {
		t.Fatalf("%v %+v", err, res)
	}
}

// 流内报错带着回复头：限流的 Retry-After 要读得到；报错原因进错误文字
func TestStreamErrorKeepsHeader(t *testing.T) {
	_, err := streamRaw(t, time.Minute, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Retry-After", "7")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"overloaded\",\"type\":\"api_error\"}}\n\n")
	})
	var se *StreamError
	if !errors.As(err, &se) || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("应该是流内报错：%v", err)
	}
	if ok, wait := Retryable(err); !ok || wait != 7*time.Second {
		t.Fatalf("api_error 要重试、等 7 秒：%v %v", ok, wait)
	}
}

// 回复体到一半不是 JSON：按读流出错报（带着已收到的字），不当成正常结束
func TestStreamBadJSONIsError(t *testing.T) {
	res, err := streamRaw(t, time.Minute, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"半\"}}]}\n\n")
		fmt.Fprint(w, "data: {oops\n\n")
	})
	if err == nil || res.Text != "半" {
		t.Fatalf("应该出错、留着半个字：%v %+v", err, res)
	}
	if ok, _ := Retryable(err); !ok {
		t.Fatalf("读不懂的块算一时的错误：%v", err)
	}
}
