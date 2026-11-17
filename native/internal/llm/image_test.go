package llm

import (
	"encoding/json"
	"testing"
)

func TestImageContentAndCapability(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"id":"unknown"}`, false},
		{`{"modalities":{"input":["text"]}}`, false},
		{`{"modalities":{"input":["text","image"]}}`, true},
	} {
		var m Model
		if err := json.Unmarshal([]byte(tc.raw), &m); err != nil {
			t.Fatal(err)
		}
		if m.SupportsImages() != tc.want {
			t.Fatalf("%s: capability %v", tc.raw, m.SupportsImages())
		}
	}
}
