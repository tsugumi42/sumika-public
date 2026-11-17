package ctlserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"sumika-native/internal/config"
	"sumika-native/internal/ctlproto"
)

// BackfillRef 仅在宿主和服务端之间流转；不进入线上 JSON。
type BackfillRef struct {
	Platform, Account, Conversation string
	ID, FromMS, ToMS                int64
}
type BackfillRow struct {
	Ref  BackfillRef
	View ctlproto.BackfillGap
}
type BackfillPage struct {
	Items      []BackfillRow
	NextCursor string
	HasMore    bool
	AsOfMS     int64
	Scope      string
}
type BackfillBackend interface {
	BackfillStatus(context.Context) (ctlproto.BackfillStatus, error)
	BackfillGaps(context.Context, string, int) (BackfillPage, error)
	BackfillRetry(context.Context, BackfillRef) (ctlproto.BackfillAccepted, error)
	BackfillRun(context.Context) (ctlproto.BackfillAccepted, error)
}

func GapHandleOf(key []byte, r BackfillRef) string {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "gap\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d", r.Platform, r.Account, r.Conversation, r.ID, r.FromMS, r.ToMS)
	return hex.EncodeToString(m.Sum(nil))[:ctlproto.HandleLen]
}

var computeGapHandle = GapHandleOf

func maskGapConversation(r BackfillRef) (string, bool) {
	if r.Platform != "qq" || r.Account == "" || r.ID <= 0 || r.FromMS < 0 || r.ToMS < r.FromMS {
		return "", false
	}
	if groupConv.MatchString(r.Conversation) {
		return "qq:group:" + config.MaskID(strings.TrimPrefix(r.Conversation, "qq:group:")), true
	}
	parts := strings.Split(r.Conversation, ":")
	if len(parts) == 4 && parts[0] == "qq" && parts[1] == "private" && parts[2] == r.Account && digitsOnly(parts[2]) && digitsOnly(parts[3]) {
		return "qq:private:" + config.MaskID(parts[2]) + ":" + config.MaskID(parts[3]), true
	}
	return "", false
}
func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return strings.Trim(s, "0") != ""
}
func gapReason(s string) string {
	switch s {
	case "page-cap", "no-anchor", "stalled", "broken-page", "unverified-head", "unverified-history", "call-failed", "budget", "unsupported", "outage":
		return s
	}
	return "unknown"
}

// backfillKey 不把设置文件路径或外部错误正文带进补录控制回复。
func (s *Server) backfillKey() ([]byte, error) {
	key, err := s.handleKey()
	if err != nil {
		return nil, &ctlproto.Error{Code: ctlproto.CodeFailed, Msg: "读取缺口句柄失败", Data: ctlproto.BackfillFailure{ReasonCode: "io"}}
	}
	return key, nil
}
func (s *Server) exposeBackfill(p BackfillPage) (ctlproto.BackfillGaps, error) {
	key, err := s.backfillKey()
	if err != nil {
		return ctlproto.BackfillGaps{}, err
	}
	out := ctlproto.BackfillGaps{Items: []ctlproto.BackfillGap{}, NextCursor: p.NextCursor, HasMore: p.HasMore, AsOfMS: p.AsOfMS, Scope: ctlproto.Clean(p.Scope)}
	for _, row := range p.Items {
		masked, ok := maskGapConversation(row.Ref)
		if !ok {
			continue
		}
		v := row.View
		v.MaskedConversation = masked
		v.GapHandle = computeGapHandle(key, row.Ref)
		v.FromMS, v.ToMS = row.Ref.FromMS, row.Ref.ToMS
		v.Reason = gapReason(v.Reason)
		v.State = ctlproto.Clean(v.State)
		v.JobID = ctlproto.Clean(v.JobID)
		out.Items = append(out.Items, v)
	}
	return out, nil
}
func (s *Server) resolveGap(b BackfillBackend, handle string) (BackfillRef, error) {
	key, err := s.backfillKey()
	if err != nil {
		return BackfillRef{}, err
	}
	var hit BackfillRef
	n := 0
	cursor := ""
	for {
		p, err := b.BackfillGaps(s.ctx, cursor, ctlproto.MaxGapCount)
		if err != nil {
			return hit, err
		}
		for _, r := range p.Items {
			if _, ok := maskGapConversation(r.Ref); ok && computeGapHandle(key, r.Ref) == handle {
				hit = r.Ref
				n++
			}
		}
		if !p.HasMore {
			break
		}
		if p.NextCursor == "" || p.NextCursor == cursor {
			return hit, ctlproto.Errorf(ctlproto.CodeFailed, "缺口分页未继续")
		}
		cursor = p.NextCursor
	}
	if n != 1 {
		return BackfillRef{}, ctlproto.Errorf(ctlproto.CodeInvalid, "缺口句柄失效或冲突，请重新运行 backfill gaps")
	}
	return hit, nil
}
func (s *Server) backfill(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) == 0 {
		return invalid("用法：backfill status|gaps|retry <缺口句柄>|run")
	}
	sub := req.Args[0]
	if (sub == "retry" && len(req.Args) != 2) || (sub != "retry" && len(req.Args) != 1) {
		return invalid("backfill 参数不正确")
	}
	if sub != "status" && sub != "gaps" && sub != "retry" && sub != "run" {
		return invalid("backfill 子命令只能是 status、gaps、retry、run")
	}
	if sub == "retry" && !ctlproto.IsHandle(req.Args[1]) {
		return invalid("retry 只接受 backfill gaps 给出的八位小写缺口句柄")
	}
	if sub == "gaps" && (req.N < 0 || req.N > ctlproto.MaxGapCount || (req.Cursor != nil && *req.Cursor == "")) {
		return invalid("缺口分页参数不正确")
	}
	if s.opt.Demo {
		if sub == "retry" || sub == "run" {
			return fail(ctlproto.CodeDemo, "演示模式不能补录"), nil
		}
		if sub == "gaps" {
			return success(ctlproto.BackfillGaps{Items: []ctlproto.BackfillGap{}, Scope: "demo"}), nil
		}
		return success(ctlproto.BackfillStatus{Scope: "demo", ReasonCode: "demo", Discovery: "unsupported", Media: ctlproto.BackfillMedia{Scope: "demo", State: "skipped"}}), nil
	}
	b, ok := s.b.(BackfillBackend)
	if !ok {
		return s.fromErr(&ctlproto.Error{Code: ctlproto.CodeFailed, Msg: "补录接口不可用", Data: ctlproto.BackfillFailure{ReasonCode: "unavailable"}}), nil
	}
	switch sub {
	case "status":
		d, err := b.BackfillStatus(s.ctx)
		return s.result(cleanBackfillStatus(d), err), nil
	case "gaps":
		n := req.N
		if n == 0 {
			n = ctlproto.DefaultGapCount
		}
		if n < 1 || n > ctlproto.MaxGapCount {
			return invalid("-n 要在 1 到 200 之间")
		}
		cursor := ""
		if req.Cursor != nil {
			cursor = *req.Cursor
			if cursor == "" {
				return invalid("--cursor 不能为空")
			}
		}
		p, err := b.BackfillGaps(s.ctx, cursor, n)
		if err != nil {
			return s.fromErr(err), nil
		}
		d, err := s.exposeBackfill(p)
		return s.result(d, err), nil
	case "retry":
		ref, err := s.resolveGap(b, req.Args[1])
		if err != nil {
			return s.fromErr(err), nil
		}
		d, err := b.BackfillRetry(s.ctx, ref)
		if err == nil {
			s.logf("控制接口：backfill retry → 成功（任务 %s）", d.JobID)
		}
		return s.result(d, err), nil
	default:
		d, err := b.BackfillRun(s.ctx)
		if err == nil {
			s.logf("控制接口：backfill run → 成功（任务 %s）", d.JobID)
		}
		return s.result(d, err), nil
	}
}
func cleanBackfillStatus(v ctlproto.BackfillStatus) ctlproto.BackfillStatus {
	v.Scope = ctlproto.Clean(v.Scope)
	v.ReasonCode = ctlproto.Clean(v.ReasonCode)
	v.Discovery = ctlproto.Clean(v.Discovery)
	v.Media = cleanBackfillMedia(v.Media)
	clean := func(j *ctlproto.BackfillJob) *ctlproto.BackfillJob {
		if j == nil {
			return nil
		}
		v := *j
		v.ID = ctlproto.Clean(v.ID)
		v.Scope = ctlproto.Clean(v.Scope)
		v.Trigger = ctlproto.Clean(v.Trigger)
		v.State = ctlproto.Clean(v.State)
		v.MaskedConversation = ctlproto.Clean(v.MaskedConversation)
		v.Messages = ctlproto.Clean(v.Messages)
		v.ReasonCode = ctlproto.Clean(v.ReasonCode)
		v.Media = cleanBackfillMedia(v.Media)
		return &v
	}
	v.LastAttempt, v.CurrentJob = clean(v.LastAttempt), clean(v.CurrentJob)
	return v
}
func cleanBackfillMedia(v ctlproto.BackfillMedia) ctlproto.BackfillMedia {
	v.Scope = ctlproto.Clean(v.Scope)
	v.State = ctlproto.Clean(v.State)
	return v
}
