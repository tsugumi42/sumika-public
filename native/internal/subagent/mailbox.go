package subagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/kernel"
)

// Take 只在原域仍有效的回合边界领取；脏通知本身从不启动前台。
func (m *Manager) Take(ctx context.Context, a ecosystem.Access, inv kernel.Invocation) ([]Result, error) {
	if !m.opts.Enabled || inv.Scope != a.Scope() || !m.opts.ParentValid(ctx, inv) {
		return nil, ecosystem.ErrDenied
	}
	if err := a.Check(ctx, "task:mailbox"); err != nil {
		return nil, err
	}
	records, err := m.opts.Repository.PendingTasks(ctx, a.Scope())
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(records))
	for _, r := range records {
		if r.RootID != inv.RootID {
			continue
		}
		var result Result
		if err = json.Unmarshal([]byte(r.Result), &result); err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	return out, nil
}

// Commit 在持久输入已接纳 task ID 后调用；读取失败、取消或保存失败不确认邮箱。
func (m *Manager) Commit(ctx context.Context, a ecosystem.Access, inv kernel.Invocation, ids []string) error {
	if !m.opts.Enabled || inv.Scope != a.Scope() || inv.RunID == "" || !m.opts.ParentValid(ctx, inv) {
		return ecosystem.ErrDenied
	}
	if err := a.Check(ctx, "task:mailbox"); err != nil {
		return err
	}
	return m.opts.Repository.CommitTasks(ctx, a.Scope(), inv.RunID, ids)
}

type Page struct {
	Result     Result
	NextCursor string
}
type resultCursor struct {
	TaskID, Owner, Ref string
	Offset             int64
}

func (m *Manager) ReadPage(ctx context.Context, a ecosystem.Access, parent kernel.Invocation, id, cursor string) (Page, error) {
	result, err := m.Read(ctx, a, parent, id)
	if err != nil {
		return Page{}, err
	}
	if result.Ref.ID == "" {
		return Page{Result: result}, nil
	}
	var cur resultCursor
	if cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(b) > 1024 {
			return Page{}, fmt.Errorf("cursor_invalidated")
		}
		if err = json.Unmarshal(b, &cur); err != nil || cur.TaskID != id || cur.Owner != a.Key() || cur.Ref != result.Ref.ID {
			return Page{}, fmt.Errorf("cursor_invalidated")
		}
	}
	if cur.Offset < 0 {
		return Page{}, fmt.Errorf("cursor_invalidated")
	}
	page, err := m.opts.Results.Read(ctx, result.Ref.Owner, result.Ref.ID, cur.Offset, 15000)
	if err != nil {
		return Page{}, err
	}
	result.Text = page.Text
	out := Page{Result: result}
	if !page.EOF {
		b, _ := json.Marshal(resultCursor{id, a.Key(), result.Ref.ID, page.Next})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, nil
}
