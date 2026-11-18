package turn

// 工作恢复的旧行（2b-2 之前直接存 ThinkingField、ReasoningDetails）照读：
// 思考回放转成 chat.Replay，回放出的字段和结构化思考和改动前一致。

import (
	"encoding/json"
	"testing"

	"sumika-native/internal/llm/openaichat"
)

func TestMsgJSONReadsLegacyThinking(t *testing.T) {
	raw := `{"Role":"assistant","Text":"答","HasText":true,"HasThinking":true,` +
		`"ThinkingField":"reasoning_content","ReasoningDetails":[{"type":"reasoning.text","text":"想","signature":"sig-1"}]}`
	var m Msg
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	field, details := openaichat.ReplayParts(m.Replay)
	if field != "reasoning_content" || len(details) != 1 || details[0].Obj["signature"].Str != "sig-1" {
		t.Fatalf("旧字段没转成回放：%q %v", field, details)
	}
	if m.Text != "答" || !m.HasThinking || m.Thinking != "" {
		t.Fatalf("其余字段读得不对：%+v", m)
	}

	// 新行照写照读，Replay 原样带回
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var again Msg
	if err := json.Unmarshal(b, &again); err != nil {
		t.Fatal(err)
	}
	if string(again.Replay.Data) != string(m.Replay.Data) || again.Replay.Codec != m.Replay.Codec {
		t.Fatalf("新行回放不一致：%+v %+v", again.Replay, m.Replay)
	}

	// 真正落库的旧行思考已清空：旧字段名在、值是空的，照常读出零值回放
	var empty Msg
	if err := json.Unmarshal([]byte(`{"Role":"user","Text":"你好","ThinkingField":"","ReasoningDetails":null}`), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Text != "你好" || len(empty.Replay.Data) != 0 || empty.Replay.Codec != "" {
		t.Fatalf("清空思考的旧行读得不对：%+v", empty)
	}
}
