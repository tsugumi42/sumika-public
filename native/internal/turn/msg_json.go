package turn

// 工作恢复的 JSON 里，2b-2 之前的 Msg 直接存 ThinkingField、ReasoningDetails。
// 现在思考回放收进不透明的 chat.Replay；旧行照读：两个旧字段转成编解码器第一版的封套，
// 新行带 Replay 时以它为准。旧行落库前思考已经清空（work.go Record），这里仍兼容手工造的旧行。

import (
	"encoding/json"
	"strings"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/llm/chat"
)

// UnmarshalJSON 读一条工作恢复的消息，兼容旧字段名 ThinkingField、ReasoningDetails
func (m *Msg) UnmarshalJSON(b []byte) error {
	type plain Msg
	var v struct {
		*plain
		ThinkingField    string            `json:"ThinkingField"`
		ReasoningDetails []json.RawMessage `json:"ReasoningDetails"`
	}
	v.plain = (*plain)(m)
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if m.Replay.Codec != "" || len(m.Replay.Data) > 0 {
		return nil
	}
	m.Replay = legacyReplay(v.ThinkingField, v.ReasoningDetails)
	return nil
}

// legacyReplay 把旧行的线上字段名和结构化思考装成 Replay。封套和 openaichat.ReplayFrom 一致：
// {"field":...,"details":[...]}；结构化思考既可能是原样 JSON，也可能是旧 Go 结构体写出的对象，两种都认
func legacyReplay(field string, raw []json.RawMessage) chat.Replay {
	var details []jsjson.Value
	for _, item := range raw {
		if v, err := jsjson.Parse(string(item)); err == nil {
			details = append(details, v)
			continue
		}
		var v jsjson.Value
		if json.Unmarshal(item, &v) == nil {
			details = append(details, v)
		}
	}
	if field == "" && len(details) == 0 {
		return chat.Replay{}
	}
	parts := make([]string, 0, 2)
	if field != "" {
		parts = append(parts, `"field":`+jsjson.Quote(field))
	}
	if len(details) > 0 {
		parts = append(parts, `"details":`+jsjson.Stringify(jsjson.Value{Kind: jsjson.Array, Arr: details}))
	}
	return chat.Replay{Data: []byte("{" + strings.Join(parts, ",") + "}")}
}
