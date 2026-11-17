package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/llm"
	"sumika-native/internal/llm/llmtest"
)

func TestConfirmModelReplies(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		yes        bool
		reason     string
	}{
		{"围栏否", "```json\n{\"leaked\":\"no\"}\n```", false, ""},
		{"围栏是", "```json\n{\"leaked\":\"yes\"}\n```", true, ""},
		{"坏JSON", "```json\n坏了\n```", false, "model_bad_json"},
		{"坏答案", `{"leaked":"maybe"}`, false, "model_bad_answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bodies []string
			var mu sync.Mutex
			srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Reply: llmtest.Text(tc.text)}, &bodies, &mu))
			defer srv.Close()
			yes, err := confirm(context.Background(), options{data: t.TempDir(), model: "fake/guard"}, llm.New(llm.Magpie(srv.URL)), "", "编的待发文字", []Memory{{ID: "f:fake", Text: "编的私事"}})
			if yes != tc.yes || (tc.reason == "" && err != nil) || (tc.reason != "" && (err == nil || !strings.Contains(err.Error(), tc.reason))) {
				t.Fatalf("确认=%v 错误=%v；要 %v/%s", yes, err, tc.yes, tc.reason)
			}
		})
	}
}
