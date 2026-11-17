package ctlserver

import (
	"context"
	"slices"
	"strings"
	"time"

	"sumika-native/internal/ctlproto"
)

// WhisperCreateRequest 是交给后端的一次悄悄话（自检）：字段都是本层校验过的。没有会话 id：来源会话由后端按第一个角色拼成自检话题。
type WhisperCreateRequest struct {
	Characters            []string
	SourceText, QuoteName string
	Say                   string
	Timeout               time.Duration
}

// WhisperBackend 是悄悄话命令的可选扩展：没接可写桌面的宿主不实现它，回 failed，不落到别的路径。
type WhisperBackend interface {
	WhisperCreate(ctx context.Context, req WhisperCreateRequest) (ctlproto.WhisperResult, error)
	WhisperStatus(ctx context.Context, batch string) (ctlproto.WhisperResult, error)
	WhisperCancel(ctx context.Context, batch string) (ctlproto.WhisperResult, error)
}

// WhisperLogBackend 是 whisper log / list 的可选扩展（规格-群聊引用和路由 15.6）：后端只认自检来源，真实会话的日志读不到。
type WhisperLogBackend interface {
	WhisperLog(ctx context.Context, args ctlproto.WhisperLogArgs) (ctlproto.WhisperLogResult, error)
	WhisperList(ctx context.Context, selftest bool) (ctlproto.WhisperLogResult, error)
}

// whisper 校验 whisper 请求。create 只收 --selftest 的自检悄悄话；status、cancel 只收批号。
func (s *Server) whisper(req ctlproto.Request) (ctlproto.Reply, func()) {
	if len(req.Args) == 0 {
		return invalid("用法：whisper create --selftest --character <id[,id]> --source-text <文字> --say <文字> | whisper status <批号> | whisper cancel <批号> | whisper log --conversation <自检会话> | whisper list [--selftest]")
	}
	if req.Text != "" || req.New {
		return invalid("whisper 不收 --new 和要说的话，用 --say")
	}
	b, hasBackend := s.b.(WhisperBackend)
	switch req.Args[0] {
	case "create":
		if len(req.Args) != 1 {
			return invalid("whisper create 不收位置参数")
		}
		w := req.Whisper
		if w == nil || !w.Selftest {
			return invalid("whisper create 只能做自检：要写 --selftest（不读也不碰真实会话）")
		}
		if !ctlproto.ValidChatText(w.SourceText) || !ctlproto.ValidChatText(w.Say) {
			return invalid("--source-text 和 --say 都不能是空的，最多 %d 字，也不能带控制字符", ctlproto.MaxChatText)
		}
		if !ctlproto.ValidWhisperName(w.QuoteName) {
			return invalid("--quote-name 最多 %d 字，不能带控制字符和换行", ctlproto.MaxQuoteName)
		}
		var chars []string
		for _, c := range strings.Split(req.Character, ",") {
			if !ctlproto.ValidCharacter(c) {
				return invalid("--character 要写一个或几个角色 id（逗号隔开），不能带空白、冒号、斜杠")
			}
			if !slices.Contains(chars, c) {
				chars = append(chars, c)
			}
		}
		if len(chars) > ctlproto.MaxWhisperCharacters {
			return invalid("--character 最多 %d 个角色", ctlproto.MaxWhisperCharacters)
		}
		timeout := ctlproto.DefaultChatTTL
		if req.Wait != "" {
			d, err := time.ParseDuration(req.Wait)
			if err != nil || d <= 0 || d > ctlproto.MaxWait {
				return invalid("--timeout 要写成大于 0、不超过 %v 的时长，比如 120s", ctlproto.MaxWait)
			}
			timeout = d
		}
		if r, ok := s.demo("whisper create"); ok {
			return r, nil
		}
		if !hasBackend {
			return fail(ctlproto.CodeFailed, "悄悄话接口不可用"), nil
		}
		d, err := b.WhisperCreate(s.ctx, WhisperCreateRequest{Characters: chars, SourceText: w.SourceText, QuoteName: w.QuoteName, Say: w.Say, Timeout: timeout})
		return s.result(cleanWhisper(d), err), nil
	case "log":
		return s.whisperLog(req)
	case "list":
		if len(req.Args) != 1 || req.WhisperLog != nil || req.Character != "" || req.Wait != "" || (req.Whisper != nil && (req.Whisper.SourceText != "" || req.Whisper.QuoteName != "" || req.Whisper.Say != "")) {
			return invalid("用法：whisper list [--selftest]")
		}
		if r, ok := s.demo("whisper list"); ok {
			return r, nil
		}
		lb, ok := s.b.(WhisperLogBackend)
		if !ok {
			return fail(ctlproto.CodeFailed, "悄悄话接口不可用"), nil
		}
		d, err := lb.WhisperList(s.ctx, req.Whisper != nil && req.Whisper.Selftest)
		return s.result(cleanList(d), err), nil
	case "status", "cancel":
		if len(req.Args) != 2 || !ctlproto.ValidBatch(req.Args[1]) || req.Whisper != nil || req.Character != "" || req.Wait != "" {
			return invalid("用法：whisper %s <批号>（批号是字母和数字，最多 64 位）", req.Args[0])
		}
		if r, ok := s.demo("whisper " + req.Args[0]); ok {
			return r, nil
		}
		if !hasBackend {
			return fail(ctlproto.CodeFailed, "悄悄话接口不可用"), nil
		}
		var d ctlproto.WhisperResult
		var err error
		if req.Args[0] == "status" {
			d, err = b.WhisperStatus(s.ctx, req.Args[1])
		} else {
			d, err = b.WhisperCancel(s.ctx, req.Args[1])
		}
		return s.result(cleanWhisper(d), err), nil
	}
	return invalid("用法：whisper create | status | cancel | log | list")
}

// whisperLog 校验 whisper log：只认自检来源（会话或批号至少给一个）；--entry 读一条的全文；--with-thinking 才带思考原文。
func (s *Server) whisperLog(req ctlproto.Request) (ctlproto.Reply, func()) {
	a := req.WhisperLog
	if len(req.Args) != 1 || req.Whisper != nil || req.Character != "" || req.Wait != "" || a == nil || (a.Conversation == "" && a.Batch == "") {
		return invalid("用法：whisper log --conversation <自检会话> [--batch <批号>] [--entry <条目id>] [--limit N] [--cursor <游标>] [--with-thinking]")
	}
	if a.Conversation != "" && !ctlproto.ValidLogConversation(a.Conversation) {
		return invalid("--conversation 要写自检会话（console:<角色>:topic:<话题>）；真实会话的日志读不到")
	}
	if a.Batch != "" && !ctlproto.ValidBatch(a.Batch) {
		return invalid("--batch 是字母和数字，最多 64 位")
	}
	if a.Entry != "" && !ctlproto.ValidLogEntry(a.Entry) {
		return invalid("--entry 不是日志条目的 id")
	}
	if a.Limit < 0 || a.Limit > ctlproto.LogPageMax || len(a.Cursor) > 300 || strings.ContainsAny(a.Cursor, "\r\n") {
		return invalid("--limit 要在 1 到 %d 之间，--cursor 原样用上一页给的", ctlproto.LogPageMax)
	}
	if a.Entry != "" && (a.Cursor != "" || a.Limit != 0) {
		return invalid("--entry 读一条的全文，不和 --cursor、--limit 一起用")
	}
	if r, ok := s.demo("whisper log"); ok {
		return r, nil
	}
	lb, ok := s.b.(WhisperLogBackend)
	if !ok {
		return fail(ctlproto.CodeFailed, "悄悄话接口不可用"), nil
	}
	d, err := lb.WhisperLog(s.ctx, *a)
	return s.result(cleanLog(d), err), nil
}

func cleanLog(d ctlproto.WhisperLogResult) ctlproto.WhisperLogResult {
	if d.Page != nil {
		p := ctlproto.CleanLogPage(*d.Page)
		d.Page = &p
	}
	if d.Detail != nil {
		x := ctlproto.CleanLogDetail(*d.Detail)
		d.Detail = &x
	}
	return d
}

func cleanList(d ctlproto.WhisperLogResult) ctlproto.WhisperLogResult {
	for i, it := range d.Items {
		it.Batch, it.Character, it.State = ctlproto.Clean(it.Batch), ctlproto.Clean(it.Character), ctlproto.Clean(it.State)
		d.Items[i] = it
	}
	return d
}

// cleanWhisper 外面来的文字（模型写的处理记录、错误说明）出去前过一遍
func cleanWhisper(w ctlproto.WhisperResult) ctlproto.WhisperResult {
	w.Batch, w.Status = ctlproto.Clean(w.Batch), ctlproto.Clean(w.Status)
	entries := make([]ctlproto.WhisperEntry, len(w.Entries)) // 空也给 [] 不给 null
	for i, e := range w.Entries {
		e.Character, e.State, e.Detail = ctlproto.Clean(e.Character), ctlproto.Clean(e.State), ctlproto.Clean(e.Detail)
		e.Note = ctlproto.CleanText(ctlproto.Truncate(e.Note, ctlproto.MaxWhisperNote))
		entries[i] = e
	}
	w.Entries = entries
	return w
}
