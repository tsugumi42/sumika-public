package turn

import (
	"encoding/json"
	"strconv"
	"strings"

	"sumika-native/internal/approval"
	"sumika-native/internal/egress"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/policy"
	"sumika-native/internal/store"
)

// WireApprovalSteps 在审批自己的流程里记步骤，不借通知回调。等待写成才发卡，批准写成才放行；
// 库操作不设短时限，失败交回审批流程报错。迟到等待不能盖掉结局，批准不能复活已结束的工具。
// 批准之后目标变了又问一次（Retry）时，等待可以盖掉上一次的批准：上一次已经作废了。
func WireApprovalSteps(a *approval.Approvals, st *store.Store) {
	a.Persist = func(req approval.Request, d *approval.Decision) error {
		if req.Tool == "project_trust" { // 项目说明的信任问题不是哪一步工具调用，没有步骤可记；结局留在规则存储里
			return nil
		}
		if req.Identity.ToolID != "" {
			req.ToolCallID = req.Identity.ToolID
		}
		if req.Identity.RunID != "" {
			if err := st.RecordStepIdentity(req.ToolCallID, req.Identity, -1); err != nil {
				return err
			}
		}
		if d == nil {
			_, err := st.Trace.Exec(`INSERT INTO steps (id, character_id, kind, status, approval_json)
				VALUES (?1, ?2, 'tool', 'waiting', ?3)
				ON CONFLICT(id) DO UPDATE SET status = 'waiting', approval_json = ?3
				WHERE steps.ended_at IS NULL AND (?4 > 0 OR json_extract(steps.approval_json, '$.decision') IS NULL)`,
				req.ToolCallID, req.CharacterID, `{"title":`+jsjson.Quote(req.Title)+egressJSON(req.Egress)+`}`, req.Retry)
			return err
		}
		status := d.Kind
		if status == "approved" {
			status = "running"
		}
		_, err := st.Trace.Exec(`INSERT INTO steps (id, character_id, kind, status, approval_json)
			VALUES (?1, ?2, 'tool', ?3, ?4)
			ON CONFLICT(id) DO UPDATE SET approval_json = ?4,
			status = CASE WHEN ?3 = 'running' AND steps.ended_at IS NOT NULL THEN steps.status ELSE ?3 END`,
			req.ToolCallID, req.CharacterID, status, decisionJSON(req.Title, d, req.Egress))
		return err
	}
}

// decisionJSON 是审批结局写进步骤的样子：批准的多写按哪一档、记成哪一条规则。
func decisionJSON(title string, d *approval.Decision, eg *egress.Report) string {
	s := `{"title":` + jsjson.Quote(title) + egressJSON(eg) + `,"decision":` + jsjson.Quote(d.Kind)
	if d.Via != "" {
		s += `,"via":` + jsjson.Quote(d.Via)
	}
	if d.Kind == "approved" {
		by := d.By
		if by == "" {
			by = policy.ByOnce
		}
		s += `,"by":` + jsjson.Quote(by)
		if d.Rule != "" {
			s += `,"rule":` + jsjson.Quote(d.Rule) + `,"label":` + jsjson.Quote(d.Label)
		}
	}
	return s + `}`
}

// egressJSON 是外发检查写进步骤记录的样子：只有编号、几处、几处危险、危险原因码，不写地址和任何正文
// （对话日志据此显示“外发 E-XXXXXX”，规格-群聊引用和路由 8.2）。没有就是空串。
func egressJSON(r *egress.Report) string {
	if r == nil || (len(r.Targets) == 0 && !r.Unchecked) {
		return ""
	}
	why := "[]"
	if rs := r.Reasons(); len(rs) > 0 {
		q := make([]string, len(rs))
		for i, x := range rs {
			q[i] = jsjson.Quote(x)
		}
		why = "[" + strings.Join(q, ",") + "]"
	}
	return `,"egress":{"id":` + jsjson.Quote(r.ID) + `,"n":` + strconv.Itoa(len(r.Targets)) + `,"danger":` + strconv.Itoa(r.Danger()) + `,"why":` + why + `}`
}

// WireGateSteps 让"没问主人就放行"的也留痕：哪一档放的、靠哪条规则，写进这一步的 approval_json。
// 写不进去只记日志、不拦：默认放行本来就是为了不打断，库坏了别的地方也会报。
func WireGateSteps(g *approval.Gate, st *store.Store, logf func(string, ...any)) {
	g.OnAuto = func(req approval.Request, c approval.Clearance) {
		id := req.ToolCallID
		if req.Identity.ToolID != "" {
			id = req.Identity.ToolID
		}
		s := `{"auto":` + jsjson.Quote(c.Reason) + `,"by":` + jsjson.Quote(c.By) + egressJSON(req.Egress)
		if c.Rule != "" {
			s += `,"rule":` + jsjson.Quote(c.Rule) + `,"label":` + jsjson.Quote(c.Label)
		}
		if err := st.StepApproval(id, req.CharacterID, "running", s+`}`); err != nil && logf != nil {
			logf("放行记录没写进步骤 %s：%v", id, err)
		}
	}
}

// StepRule 把步骤里存的 approval_json 换成给主人看的那一句：这次是哪条规则放行的。没放行、没记录的返回空。
func StepRule(raw string) string {
	if raw == "" {
		return ""
	}
	var a struct{ Auto, Decision, By, Label string }
	if json.Unmarshal([]byte(raw), &a) != nil {
		return ""
	}
	switch {
	case a.Decision == "approved":
		if a.By == "" {
			a.By = policy.ByOnce // 以前的记录没写档位，当时只有允许一次
		}
		return approval.RuleText(a.By, a.Label, "")
	case a.Auto != "":
		if a.By == "" {
			a.By = policy.ByMode
		}
		return approval.RuleText(a.By, a.Label, a.Auto)
	}
	return ""
}
