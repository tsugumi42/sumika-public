// Package bunseg 是 Bun 旧消息段的格式：一个 Bun 段换成中立段（ConvertSeg），以及从导入时随事件留下的旧段
// （import_meta.legacy_segments）里取对应的那一段（LegacySegment）。
//
// 常驻的包：导进来的历史一直带着旧段，桌面读历史、媒体核对都要按它取。
// 一次性导入工具已退役（ADR-308），这里继续维护既有历史的读取格式。
package bunseg

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// ImportMetaVersion 是 import_meta 的版本：以后读旧段的消费者按它选口径
const ImportMetaVersion = 1

// ConvertSeg 是一个 Bun 段 → 中立段（规格-从Bun导入历史 第五节的表）。认不出的当 other，带原类型和数据的 JSON 摘要，
// 不把未知段当文字执行、也不丢整个事件
func ConvertSeg(raw any) map[string]any {
	s, ok := raw.(map[string]any)
	if !ok {
		return map[string]any{"kind": types.SegOther, "raw": clipRunes(jsonSummary(raw), types.RawMax)}
	}
	typ, _ := s["type"].(string)
	seg := map[string]any{}
	set := func(k, v string) {
		if v != "" {
			seg[k] = v
		}
	}
	switch typ {
	case "text":
		seg["kind"] = types.SegText
		set("text", StrValue(s["text"]))
	case "at":
		seg["kind"] = types.SegAt
		set("target", StrValue(s["qq"]))
	case "reply":
		seg["kind"] = types.SegReply
		set("target", StrValue(s["id"]))
	case "image":
		if Truthy(s["sticker"]) || Truthy(s["market"]) {
			seg["kind"] = types.SegEmoji
		} else {
			seg["kind"] = types.SegImage
		}
		// 有 hash 就保留（可校验的）；没有就空着，不把 file/URL 当 hash（规格 第五节）
		set("hash", StrValue(s["hash"]))
	case "face":
		if Truthy(s["super"]) {
			seg["kind"] = types.SegSuperface
		} else {
			seg["kind"] = types.SegFace
		}
		set("ref", StrValue(s["id"]))
	case "mface":
		seg["kind"] = types.SegEmoji
		set("ref", StrValue(s["emojiId"]))
	case "record":
		seg["kind"] = types.SegVoice
	case "video":
		seg["kind"] = types.SegVideo
	case "file":
		seg["kind"] = types.SegFile
	case "forward":
		seg["kind"] = types.SegForward
		set("ref", StrValue(s["id"]))
	case "card":
		seg["kind"] = types.SegCard
		set("raw", clipRunes(StrValue(s["raw"]), types.RawMax))
	case "other":
		seg["kind"] = types.SegOther
		set("raw", clipRunes(jsonSummary(s), types.RawMax))
	default:
		seg["kind"] = types.SegOther
		set("raw", clipRunes(jsonSummary(s), types.RawMax))
	}
	return seg
}

// jsonSummary 是一段原始 JSON 的紧凑摘要（原类型和数据都在里面），写进 other 的 raw
func jsonSummary(v any) string {
	s, err := store.JSString(v)
	if err != nil {
		return ""
	}
	return s
}

// clipRunes 按 Unicode 码点截到 n 个字（规格 第五节：card 和 other 的 raw 最多 200 码点）
func clipRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

// Truthy 是 JSON 值的"真"：Bun 的段里这几个开关可能是 true、1、非空串（照 Bun truthy 的用法）
func Truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return strings.TrimSpace(t) != "" && t != "false" && t != "0" && t != `""`
	case json.Number:
		n, err := t.Float64()
		return err == nil && n != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// StrValue 取一个 JSON 值的字符串写法（Bun 的段字段都是字符串，偶尔是数字）
func StrValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return ""
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// LegacySegment 取第 i 个中立段对应的导入旧段（import_meta.legacy_segments），只给展示元数据和补缺失的 hash 用。
// 版本、下标、类型、身份或已有 hash 对不上时报错，调用方保留中立段，不拿旧段覆盖目标、引用或拼路径。
func LegacySegment(version int, legacy []map[string]any, i int, seg types.Seg) (map[string]any, error) {
	if version != ImportMetaVersion || i < 0 || i >= len(legacy) {
		return nil, errors.New("旧媒体段版本或下标不匹配")
	}
	old := legacy[i]
	neutral := ConvertSeg(old)
	oldHash := StrValue(old["hash"])
	if StrValue(neutral["kind"]) != seg.Kind || StrValue(neutral["ref"]) != seg.Ref || StrValue(neutral["target"]) != seg.Target || (seg.Hash != "" && oldHash != "" && seg.Hash != oldHash) {
		return nil, errors.New("旧媒体段类型、身份或 hash 不匹配")
	}
	return old, nil
}
