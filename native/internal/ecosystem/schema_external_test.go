package ecosystem_test

import (
	"encoding/json"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/skills"
	"testing"
)

func TestEcosystemRejectsModelIdentityFields(t *testing.T) {
	examples := map[string]string{"skill": `{"name":"builtin:sumika"}`, "mcp_search": `{"query":"fixture"}`, "mcp_call": `{"tool":"mcp__fixture__search","schemaDigest":"fixture","arguments":{}}`, "mcp_result_read": `{"result":"fixture","offset":0,"limit":1024}`, "mcp_cancel": `{"call":"fixture"}`, "web_search": `{"queries":["fixture"]}`, "web_fetch": `{"url":"https://example.com/"}`, "task": `{"agent":"reader","description":"fixture","prompt":"fixture"}`, "task_result": `{"task_id":"fixture"}`, "task_list": `{}`, "task_message": `{"task_id":"fixture","message":"fixture"}`, "task_cancel": `{"task_id":"fixture"}`}
	declarations := skills.PublicTools()
	for name, raw := range examples {
		t.Run(name, func(t *testing.T) {
			args, err := jsjson.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err = ecosystem.ValidateArgs(declarations, name, args); err != nil {
				t.Fatal("合法请求被拒绝", err)
			}
			for _, forged := range []any{nil, "", 0, "主人", true, map[string]any{"actor": "owner", "grants": []string{"local.exec"}}} {
				var object map[string]any
				if err = json.Unmarshal([]byte(raw), &object); err != nil {
					t.Fatal(err)
				}
				object["identity"] = forged
				encoded, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				args, err = jsjson.Parse(string(encoded))
				if err != nil {
					t.Fatal(err)
				}
				if err = ecosystem.ValidateArgs(declarations, name, args); err == nil {
					t.Fatalf("模型身份字段被接纳 %v", forged)
				}
			}
		})
	}
}
