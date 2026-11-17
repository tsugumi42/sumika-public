package bunseg

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
)

func TestConvertSeg(t *testing.T) {
	long := strings.Repeat("界", types.RawMax)
	tests := []struct {
		name string
		raw  any
		want map[string]any
	}{
		{"text", map[string]any{"type": "text", "text": "你好"}, map[string]any{"kind": "text", "text": "你好"}},
		{"empty text omitted", map[string]any{"type": "text", "text": false}, map[string]any{"kind": "text"}},
		{"at number", map[string]any{"type": "at", "qq": json.Number("123")}, map[string]any{"kind": "at", "target": "123"}},
		{"reply", map[string]any{"type": "reply", "id": "msg"}, map[string]any{"kind": "reply", "target": "msg"}},
		{"image", map[string]any{"type": "image", "hash": "abc", "file": "fake", "url": "https://example.invalid/x"}, map[string]any{"kind": "image", "hash": "abc"}},
		{"image without hash", map[string]any{"type": "image", "file": "fake", "url": "https://example.invalid/x"}, map[string]any{"kind": "image"}},
		{"image sticker", map[string]any{"type": "image", "sticker": true, "hash": "abc"}, map[string]any{"kind": "emoji", "hash": "abc"}},
		{"image market", map[string]any{"type": "image", "market": "1"}, map[string]any{"kind": "emoji"}},
		{"image false switches", map[string]any{"type": "image", "sticker": "false", "market": json.Number("0")}, map[string]any{"kind": "image"}},
		{"image float zero switch", map[string]any{"type": "image", "sticker": float64(0)}, map[string]any{"kind": "emoji"}},
		{"face", map[string]any{"type": "face", "id": "7"}, map[string]any{"kind": "face", "ref": "7"}},
		{"superface", map[string]any{"type": "face", "id": 7, "super": true}, map[string]any{"kind": "superface", "ref": "7"}},
		{"mface", map[string]any{"type": "mface", "emojiId": "emoji-1"}, map[string]any{"kind": "emoji", "ref": "emoji-1"}},
		{"record", map[string]any{"type": "record", "file": "x"}, map[string]any{"kind": "voice"}},
		{"video", map[string]any{"type": "video", "hash": "x"}, map[string]any{"kind": "video"}},
		{"file", map[string]any{"type": "file", "name": "x"}, map[string]any{"kind": "file"}},
		{"forward", map[string]any{"type": "forward", "id": "fwd"}, map[string]any{"kind": "forward", "ref": "fwd"}},
		{"card empty", map[string]any{"type": "card"}, map[string]any{"kind": "card"}},
		{"card under limit", map[string]any{"type": "card", "raw": long[:len(long)-3]}, map[string]any{"kind": "card", "raw": long[:len(long)-3]}},
		{"card at limit", map[string]any{"type": "card", "raw": long}, map[string]any{"kind": "card", "raw": long}},
		{"card over limit", map[string]any{"type": "card", "raw": long + "🙂"}, map[string]any{"kind": "card", "raw": long}},
		{"other", map[string]any{"type": "other", "data": "<&"}, map[string]any{"kind": "other", "raw": `{"data":"<&","type":"other"}`}},
		{"unknown", map[string]any{"type": "mystery", "data": 2}, map[string]any{"kind": "other", "raw": `{"data":2,"type":"mystery"}`}},
		{"missing type", map[string]any{"text": "x"}, map[string]any{"kind": "other", "raw": `{"text":"x"}`}},
		{"non-map null", nil, map[string]any{"kind": "other", "raw": "null"}},
		{"non-map string", "hello", map[string]any{"kind": "other", "raw": `"hello"`}},
		{"non-map over limit", strings.Repeat("界", types.RawMax), map[string]any{"kind": "other", "raw": `"` + strings.Repeat("界", types.RawMax-1)}},
		{"unserializable map", map[string]any{"type": "other", "data": make(chan int)}, map[string]any{"kind": "other"}},
		{"unserializable non-map", make(chan int), map[string]any{"kind": "other", "raw": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConvertSeg(tt.raw); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ConvertSeg(%#v) = %#v, want %#v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestLegacySegment(t *testing.T) {
	image := map[string]any{"type": "image", "hash": "abc", "file": "synthetic"}
	noHash := map[string]any{"type": "image", "file": "synthetic"}
	at := map[string]any{"type": "at", "qq": "person"}
	face := map[string]any{"type": "face", "id": "7"}
	legacy := []map[string]any{image, noHash, at, face}
	tests := []struct {
		name           string
		version, index int
		seg            types.Seg
		want           map[string]any
		wantErr        bool
	}{
		{"matching image", ImportMetaVersion, 0, types.Seg{Kind: "image", Hash: "abc"}, image, false},
		{"missing neutral hash", ImportMetaVersion, 0, types.Seg{Kind: "image"}, image, false},
		{"missing old hash", ImportMetaVersion, 1, types.Seg{Kind: "image", Hash: "abc"}, noHash, false},
		{"both hashes absent", ImportMetaVersion, 1, types.Seg{Kind: "image"}, noHash, false},
		{"hash mismatch", ImportMetaVersion, 0, types.Seg{Kind: "image", Hash: "different"}, nil, true},
		{"old version", ImportMetaVersion - 1, 0, types.Seg{Kind: "image"}, nil, true},
		{"future version", ImportMetaVersion + 1, 0, types.Seg{Kind: "image"}, nil, true},
		{"negative index", ImportMetaVersion, -1, types.Seg{Kind: "image"}, nil, true},
		{"index at length", ImportMetaVersion, len(legacy), types.Seg{Kind: "image"}, nil, true},
		{"kind mismatch", ImportMetaVersion, 0, types.Seg{Kind: "emoji"}, nil, true},
		{"matching target", ImportMetaVersion, 2, types.Seg{Kind: "at", Target: "person"}, at, false},
		{"target mismatch", ImportMetaVersion, 2, types.Seg{Kind: "at", Target: "other"}, nil, true},
		{"missing target", ImportMetaVersion, 2, types.Seg{Kind: "at"}, nil, true},
		{"matching ref", ImportMetaVersion, 3, types.Seg{Kind: "face", Ref: "7"}, face, false},
		{"ref mismatch", ImportMetaVersion, 3, types.Seg{Kind: "face", Ref: "8"}, nil, true},
		{"missing ref", ImportMetaVersion, 3, types.Seg{Kind: "face"}, nil, true},
		{"extra target", ImportMetaVersion, 0, types.Seg{Kind: "image", Target: "person"}, nil, true},
		{"extra ref", ImportMetaVersion, 0, types.Seg{Kind: "image", Ref: "7"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LegacySegment(tt.version, legacy, tt.index, tt.seg)
			if (err != nil) != tt.wantErr || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LegacySegment() = %#v, %v; want %#v, error=%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
	if got, err := LegacySegment(ImportMetaVersion, nil, 0, types.Seg{}); err == nil || got != nil {
		t.Errorf("empty legacy = %#v, %v; want nil and error", got, err)
	}
}

func TestTruthy(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{"nil", nil, false}, {"false", false, false}, {"true", true, true},
		{"empty", "", false}, {"whitespace", " \t", false}, {"false string", "false", false},
		{"zero string", "0", false}, {"quoted empty", `""`, false},
		{"padded false", " false ", true}, {"uppercase false", "False", true}, {"text", "yes", true},
		{"number zero", json.Number("0"), false}, {"number fractional zero", json.Number("0.0"), false},
		{"number negative", json.Number("-1"), true}, {"invalid number", json.Number("bad"), false},
		{"empty slice", []any{}, false}, {"nonempty slice", []any{nil}, true},
		{"empty map", map[string]any{}, false}, {"nonempty map", map[string]any{"x": nil}, true},
		{"float zero", float64(0), true}, {"int zero", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Truthy(tt.value); got != tt.want {
				t.Errorf("Truthy(%#v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestStrValue(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"nil", nil, ""}, {"empty", "", ""}, {"string", "  text  ", "  text  "},
		{"number preserves spelling", json.Number("1.00"), "1.00"}, {"invalid number", json.Number("bad"), "bad"},
		{"true", true, "true"}, {"false", false, ""}, {"integer zero", 0, "0"},
		{"float", float64(1.5), "1.5"}, {"slice", []any{"a", 2}, "[a 2]"},
		{"map", map[string]any{"x": 1}, "map[x:1]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StrValue(tt.value); got != tt.want {
				t.Errorf("StrValue(%#v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
