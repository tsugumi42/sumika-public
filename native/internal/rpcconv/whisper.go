package rpcconv

// 悄悄话和对话日志的转换（规格-群聊引用和路由）。约定同 conv.go：空切片不发、收回来是 nil（日志页的 Entries 例外：收回来给 []，和 ctlproto.CleanLogPage 一致）。

import (
	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
	"sumika-native/internal/rpcwire"
)

func WhisperReceiptOut(r api.WhisperReceipt) rpcwire.WhisperReceipt {
	return rpcwire.WhisperReceipt{Batch: r.Batch, Characters: strs(r.Characters)}
}

func WhisperReceiptIn(r rpcwire.WhisperReceipt) api.WhisperReceipt {
	return api.WhisperReceipt{Batch: r.Batch, Characters: strs(r.Characters)}
}

func logQuoteOut(q ctlproto.LogQuote) rpcwire.LogQuote {
	return rpcwire.LogQuote{Name: q.Name, At: q.At, Text: q.Text}
}

func logQuoteIn(q rpcwire.LogQuote) ctlproto.LogQuote {
	return ctlproto.LogQuote{Name: q.Name, At: q.At, Text: q.Text}
}

func logEntryOut(e ctlproto.LogEntry) rpcwire.LogEntry {
	return rpcwire.LogEntry{ID: e.ID, Batch: e.Batch, Kind: e.Kind, Character: e.Character, Parent: e.Parent, At: e.At, State: e.State, Position: e.Position,
		Summary: e.Summary, Runes: e.Runes, Targets: strs(e.Targets), Quotes: mapSlice(e.Quotes, logQuoteOut), Turn: e.Turn, Err: e.Err, Waiting: e.Waiting, Busy: e.Busy,
		Egress: e.Egress, EgressN: e.EgressN, EgressDanger: e.EgressDanger}
}

func logEntryIn(e rpcwire.LogEntry) ctlproto.LogEntry {
	return ctlproto.LogEntry{ID: e.ID, Batch: e.Batch, Kind: e.Kind, Character: e.Character, Parent: e.Parent, At: e.At, State: e.State, Position: e.Position,
		Summary: e.Summary, Runes: e.Runes, Targets: strs(e.Targets), Quotes: mapSlice(e.Quotes, logQuoteIn), Turn: e.Turn, Err: e.Err, Waiting: e.Waiting, Busy: e.Busy,
		Egress: e.Egress, EgressN: e.EgressN, EgressDanger: e.EgressDanger}
}

func LogPageOut(p ctlproto.LogPage) rpcwire.LogPage {
	return rpcwire.LogPage{Entries: mapSlice(p.Entries, logEntryOut), Next: p.Next, Running: p.Running}
}

func LogPageIn(p rpcwire.LogPage) ctlproto.LogPage {
	es := mapSlice(p.Entries, logEntryIn)
	if es == nil {
		es = []ctlproto.LogEntry{}
	}
	return ctlproto.LogPage{Entries: es, Next: p.Next, Running: p.Running}
}

func LogDetailOut(d ctlproto.LogDetail) rpcwire.LogDetail {
	return rpcwire.LogDetail{ID: d.ID, Kind: d.Kind, Title: d.Title, Text: d.Text, Args: d.Args, Hidden: d.Hidden}
}

func LogDetailIn(d rpcwire.LogDetail) ctlproto.LogDetail {
	return ctlproto.LogDetail{ID: d.ID, Kind: d.Kind, Title: d.Title, Text: d.Text, Args: d.Args, Hidden: d.Hidden}
}
