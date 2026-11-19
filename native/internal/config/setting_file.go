package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// SettingChange changes exactly one whitelisted setting. Account submissions
// contain only id, character and optional url; token is never accepted.
type SettingChange struct {
	Key   string
	Value any
}

var settingKinds = map[string]string{
	"magpie.baseUrl": "string", "qq.owner": "id", "qq.accounts": "accounts",
	"qq.groups": "ids", "qq.privateAllow": "ids", "qq.reply": "bool", "qq.goSend": "bool",
	"qq.qqDir": "string", "qq.qqDataDir": "string", "qq.protocolDir": "string", "qq.protocolLogDir": "string",
	"qq.media.download": "bool", "qq.media.ffmpeg": "string", "qq.media.ffprobe": "string",
	"qq.backfill.enabled": "bool", "qq.backfill.groups": "bool", "qq.forward.fetch": "bool",
	"notifications.goal": "bool", "notifications.loop": "bool", "notifications.approval": "bool", "notifications.question": "bool", "notifications.reminder": "bool",
	"qq.remoteApproval.enabled": "bool", "qq.remoteApproval.awayAfterSeconds": "number",
	"group.frequency": "number", "group.jitter": "number", "group.mode": "string",
	"plugins.sessions": "sessions", "reply.deny": "deny", "linkReader.proxy": "string", "linkReader.expand.startEmoji": "string", "location": "location",
	"tools.approvalMode": "string", "tools.lineEnding": "string",
	"embed.modelsRoot": "string",
	"web.searchOrder":  "weborder", "web.fetchOrder": "weborder",
	"agent.idleCompact.enabled": "bool", "agent.idleCompact.after": "number", "agent.idleCompact.minTokens": "int",
	"freeApi.probe.enabled": "bool", "freeApi.probe.intervalHours": "int",
	"freeApi.limits.weather.perActorDaily": "int", "freeApi.limits.weather.perConversationHourly": "int",
	"freeApi.limits.exchangeRate.perActorDaily": "int", "freeApi.limits.exchangeRate.perConversationHourly": "int",
	"freeApi.limits.calendar.perActorDaily": "int", "freeApi.limits.calendar.perConversationHourly": "int",
}

func WritableSetting(key string) bool { _, ok := settingKinds[key]; return ok }

func SaveSetting(ctx context.Context, file, history, base string, ch SettingChange, now time.Time) (Config, error) {
	return saveConfig(ctx, file, history, base, now, func(raw []byte) ([]byte, Config, error) { return PatchSetting(raw, file, ch) })
}

// PatchSetting validates the submitted shape before invoking the shared YAML
// patcher and Parse. Errors intentionally omit input values and YAML excerpts.
func PatchSetting(raw []byte, file string, ch SettingChange) ([]byte, Config, error) {
	kind, ok := settingKinds[ch.Key]
	if !ok {
		return nil, Config{}, errors.New("设置键不可写")
	}
	invalid := errors.New("设置值无效或配置无法安全修改")
	encoded, err := json.Marshal(ch.Value)
	if err != nil {
		return nil, Config{}, invalid
	}
	if (kind == "id" || kind == "ids" || kind == "accounts") && containsFloat(reflect.ValueOf(ch.Value), 0) {
		return nil, Config{}, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, Config{}, invalid
	}
	switch kind {
	case "string":
		s, ok := value.(string)
		if !ok || strings.ContainsRune(s, 0) {
			return nil, Config{}, invalid
		}
		if ch.Key == "tools.approvalMode" && s != "manual" && s != "auto" && s != "yolo" {
			return nil, Config{}, invalid
		}
		if ch.Key == "tools.lineEnding" && !lineEndings[s] {
			return nil, Config{}, invalid
		}
		if ch.Key == "magpie.baseUrl" || ch.Key == "linkReader.proxy" {
			old, e := Parse(raw, file)
			if e != nil {
				return nil, Config{}, invalid
			}
			previous := old.Magpie.BaseURL
			if ch.Key == "linkReader.proxy" {
				previous = old.LinkReader.Proxy
			}
			if visible, ok := settingVisibleURL(previous); ok && s == visible {
				value = previous
			} else if s != "" || ch.Key == "magpie.baseUrl" {
				u, e := url.Parse(s)
				if e != nil || u.Host == "" || (ch.Key == "magpie.baseUrl" && u.Scheme != "http" && u.Scheme != "https") || (ch.Key == "linkReader.proxy" && (u.Scheme != "http" || u.User != nil)) {
					return nil, Config{}, invalid
				}
			}
		}
	case "bool":
		if _, ok := value.(bool); !ok {
			return nil, Config{}, invalid
		}
	case "weborder":
		// 整张顺序表一起写：字符串列表，每项是已知服务名、不重复；空表不收（要恢复默认就写默认顺序）
		list, ok := value.([]any)
		if !ok || len(list) == 0 {
			return nil, Config{}, invalid
		}
		names := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, Config{}, invalid
			}
			names = append(names, s)
		}
		if ValidWebOrder(ch.Key, names) != nil {
			return nil, Config{}, invalid
		}
	case "location":
		// 整个对象一起写；空对象是清空。字段外的键、错误类型一律拒绝，范围由 Parse 的 checkLife 查
		if _, ok := value.(map[string]any); !ok {
			return nil, Config{}, invalid
		}
		dec := json.NewDecoder(bytes.NewReader(encoded))
		dec.DisallowUnknownFields()
		var loc Location
		if dec.Decode(&loc) != nil {
			return nil, Config{}, invalid
		}
		// 转回普通的 map：数字变成 float64，空字段去掉；json.Number 过不了 patchSettings 的自检
		normal, e := json.Marshal(loc)
		if e != nil {
			return nil, Config{}, invalid
		}
		clean := map[string]any{}
		if json.Unmarshal(normal, &clean) != nil {
			return nil, Config{}, invalid
		}
		value = clean
	case "int":
		n, ok := value.(json.Number)
		if !ok {
			return nil, Config{}, invalid
		}
		i, e := strconv.ParseInt(string(n), 10, 32)
		if e != nil {
			return nil, Config{}, invalid
		}
		value = int(i)
	case "number":
		n, ok := value.(json.Number)
		if !ok {
			return nil, Config{}, invalid
		}
		f, e := strconv.ParseFloat(string(n), 64)
		if e != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, Config{}, invalid
		}
		value = f
	case "id":
		id, ok := settingID(value)
		if !ok {
			return nil, Config{}, invalid
		}
		value = id
	case "ids":
		list, ok := value.([]any)
		if !ok {
			return nil, Config{}, invalid
		}
		for i, item := range list {
			id, ok := settingID(item)
			if !ok {
				return nil, Config{}, invalid
			}
			list[i] = id
		}
	case "accounts":
		list, ok := value.([]any)
		if !ok {
			return nil, Config{}, invalid
		}
		old, e := Parse(raw, file)
		if e != nil {
			return nil, Config{}, invalid
		}
		var doc map[string]any
		if yaml.Unmarshal(raw, &doc) != nil {
			return nil, Config{}, invalid
		}
		oldItems := []any(nil)
		if qq, ok := doc["qq"].(map[string]any); ok {
			if accounts, exists := qq["accounts"]; exists {
				oldItems, ok = accounts.([]any)
				if !ok || len(oldItems) != len(old.QQ.Accounts) {
					return nil, Config{}, invalid
				}
			}
		}
		oldByID := map[string]map[string]any{}
		for i, account := range old.QQ.Accounts {
			item, ok := oldItems[i].(map[string]any)
			if !ok {
				return nil, Config{}, invalid
			}
			identity := strings.TrimLeft(string(account.ID), "0")
			if identity == "" || oldByID[identity] != nil {
				return nil, Config{}, invalid
			}
			oldByID[identity] = item
		}
		seen := map[string]bool{}
		for i, entry := range list {
			item, ok := entry.(map[string]any)
			if !ok {
				return nil, Config{}, invalid
			}
			for k := range item {
				if k != "id" && k != "character" && k != "url" {
					return nil, Config{}, invalid
				}
			}
			id, ok := settingID(item["id"])
			if !ok {
				return nil, Config{}, invalid
			}
			identity := strings.TrimLeft(id, "0")
			if seen[identity] {
				return nil, Config{}, invalid
			}
			seen[identity] = true
			character, ok := item["character"].(string)
			if !ok || strings.TrimSpace(character) == "" || strings.ContainsRune(character, 0) {
				return nil, Config{}, invalid
			}
			if v, exists := item["url"]; exists {
				s, ok := v.(string)
				if !ok || strings.ContainsRune(s, 0) {
					return nil, Config{}, invalid
				}
				if previous, ok := oldByID[identity]["url"].(string); ok {
					if visible, ok := settingVisibleURL(previous); ok && s == visible {
						item["url"] = previous
					}
				}
			}
			merged := map[string]any{}
			for k, v := range oldByID[identity] {
				merged[k] = v
			}
			delete(merged, "url")
			for k, v := range item {
				merged[k] = v
			}
			merged["id"] = id
			list[i] = merged
		}
	case "sessions":
		m, ok := value.(map[string]any)
		if !ok {
			return nil, Config{}, invalid
		}
		for _, v := range m {
			switches, ok := v.(map[string]any)
			if !ok {
				return nil, Config{}, invalid
			}
			for _, on := range switches {
				if _, ok := on.(bool); !ok {
					return nil, Config{}, invalid
				}
			}
		}
	case "deny":
		m, ok := value.(map[string]any)
		if !ok {
			return nil, Config{}, invalid
		}
		for _, v := range m {
			if _, ok := v.(string); !ok {
				return nil, Config{}, invalid
			}
		}
	}
	out, c, err := patchSettings(raw, file, []configEdit{{path: strings.Split(ch.Key, "."), value: value}})
	if err != nil {
		return nil, Config{}, invalid
	}
	return out, c, nil
}

// Keep this projection aligned with settingsreg's URL display.
func settingVisibleURL(s string) (string, bool) {
	if s == "" {
		return "", true
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	return u.String(), true
}

func settingID(v any) (string, bool) {
	var s string
	switch n := v.(type) {
	case string:
		s = n
	case json.Number:
		if _, err := strconv.ParseUint(string(n), 10, 64); err != nil {
			return "", false
		}
		s = string(n)
	default:
		return "", false
	}
	return s, positiveDigits(s)
}

func containsFloat(v reflect.Value, depth int) bool {
	if !v.IsValid() {
		return false
	}
	if depth > 100 {
		return true
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		return !v.IsNil() && containsFloat(v.Elem(), depth+1)
	case reflect.Float32, reflect.Float64:
		return true
	case reflect.Map:
		for _, key := range v.MapKeys() {
			if containsFloat(v.MapIndex(key), depth+1) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if containsFloat(v.Index(i), depth+1) {
				return true
			}
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if containsFloat(v.Field(i), depth+1) {
				return true
			}
		}
	}
	return false
}
