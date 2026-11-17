package memory

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// WhisperSource 是悄悄话处理那一轮里 remember 写的事实的来源（规格-群聊引用和路由 8.3）：哪一批悄悄话、
// 主人是在哪个会话里引用的、被引的是哪几条事件。值由宿主按悄悄话服务里这一条的真实来历给，不来自模型的参数。
// 存在 facts.whisper_json（迁移 0003），记忆页和召回都据它写明“这是从某次悄悄话来的”。
type WhisperSource struct {
	Batch  string   `json:"batch"`
	Source string   `json:"source"`
	Quotes []string `json:"quotes,omitempty"`
}

const (
	maxWhisperQuotes = 20 // 一批最多引用二十块，和多选上限一致
	maxWhisperField  = 200
)

// Valid：批号和来源会话都有才算数；半截的来源不写（写了反而像是核对过）
func (w *WhisperSource) Valid() bool {
	return w != nil && strings.TrimSpace(w.Batch) != "" && strings.TrimSpace(w.Source) != ""
}

// clean 返回压成一行、截短的副本：来源会话和事件 id 最终要写进给她看的〔来源〕和记忆页，不能带换行、控制字符
func (w WhisperSource) clean() WhisperSource {
	out := WhisperSource{Batch: CleanLine(w.Batch, maxWhisperField), Source: CleanLine(w.Source, maxWhisperField)}
	for _, q := range w.Quotes {
		if q = CleanLine(q, maxWhisperField); q != "" && len(out.Quotes) < maxWhisperQuotes {
			out.Quotes = append(out.Quotes, q)
		}
	}
	return out
}

// whisperJSON 是写进库的样子；不合格返回 nil（写 NULL）
func whisperJSON(w *WhisperSource) any {
	if !w.Valid() {
		return nil
	}
	raw, err := json.Marshal(w.clean())
	if err != nil {
		return nil
	}
	return string(raw)
}

// parseWhisper 读库里的 whisper_json；空、坏了、缺批号或来源都当没有，不猜
func parseWhisper(raw string) *WhisperSource {
	raw = strings.TrimSpace(raw)
	if raw == "" || utf8.RuneCountInString(raw) > 8000 {
		return nil
	}
	var w WhisperSource
	if json.Unmarshal([]byte(raw), &w) != nil || !w.Valid() {
		return nil
	}
	c := w.clean()
	return &c
}

// Mark 是给她看的来源前缀和记忆页的写法：批号、来源会话、被引事件（都转义过）
func (w *WhisperSource) Mark() string {
	if w == nil {
		return ""
	}
	s := "悄悄话（批号=" + escapeUserText(w.Batch) + "；来源会话=" + escapeUserText(w.Source)
	if len(w.Quotes) > 0 {
		qs := make([]string, len(w.Quotes))
		for i, q := range w.Quotes {
			qs[i] = escapeUserText(q)
		}
		s += "；被引事件=" + strings.Join(qs, "、")
	}
	return s + "）"
}

// Text 是记忆页上的写法（已压成一行、截短，不做给模型看的转义）
func (w *WhisperSource) Text() string {
	if w == nil {
		return ""
	}
	s := "批号 " + w.Batch + "；来源会话 " + w.Source
	if len(w.Quotes) > 0 {
		s += "；被引事件 " + strings.Join(w.Quotes, "、")
	}
	return s
}

// WhisperOf 解出库里 whisper_json 的原文，给记忆页用（不合格返回 nil）
func WhisperOf(raw string) *WhisperSource { return parseWhisper(raw) }
