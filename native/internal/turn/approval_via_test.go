package turn

import (
	"strings"
	"testing"

	"sumika-native/internal/approval"
)

// 从主人 QQ 私聊批的，步骤记录里写明来源（approval_json 的 via）；本机批的、到期的没有这个字段。
func TestApprovalStepRecordsVia(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[]`})
	a := &approval.Approvals{}
	WireApprovalSteps(a, h.st)
	for _, tc := range []struct {
		id, via string
		want    string
		none    bool
	}{
		{"via-qq", "qq", `"via":"qq"`, false},
		{"via-local", "", "", true},
	} {
		req := approval.Request{ID: tc.id, ToolCallID: tc.id, CharacterID: h.c.CharacterID, Title: "写文件"}
		if err := a.Persist(req, nil); err != nil {
			t.Fatal(err)
		}
		if err := a.Persist(req, &approval.Decision{Kind: "approved", Via: tc.via}); err != nil {
			t.Fatal(err)
		}
		got := h.stepState(tc.id).approval
		if !strings.Contains(got, `"decision":"approved"`) {
			t.Fatalf("%s 的结局没记：%s", tc.id, got)
		}
		if tc.none && strings.Contains(got, `"via"`) {
			t.Fatalf("本机批的不该有 via：%s", got)
		}
		if !tc.none && !strings.Contains(got, tc.want) {
			t.Fatalf("%s 要记来源 %s：%s", tc.id, tc.want, got)
		}
	}
}
