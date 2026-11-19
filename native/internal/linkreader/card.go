package linkreader

import (
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
)

// CardInfo 即使没有可识别链接也提供可展示的文字；非法卡片返回零值。
type CardInfo struct {
	Title       string
	Description string
	Prompt      string
}

const maxCardBytes = 1 << 20
const maxCardDepth = 64

// 字段名来自 N 2.7.1 matchers/rule.py:24-44、94-99，和
// A v1.6.1 core/utils.py:317-336；video.jumpUrl 是任务要求的补充形状。
var cardPaths = []struct {
	group  string
	fields []string
}{
	{"detail_1", []string{"qqdocurl", "jumpUrl", "url", "sourceUrl", "shareUrl", "targetUrl"}},
	{"news", []string{"jumpUrl", "url", "sourceUrl", "shareUrl"}},
	{"music", []string{"musicUrl", "jumpUrl", "url"}},
	{"video", []string{"jumpUrl"}},
	{"miniapp", []string{"legacyUrl", "pcJumpUrl", "jumpUrl", "sourceUrl", "url"}},
}

// 卡片载荷解析不了的几种原因（ParseCard 返回；不带载荷原文）
var (
	ErrCardTooBig   = errors.New("卡片载荷太大或嵌套太深")
	ErrCardNotJSON  = errors.New("卡片载荷不是 JSON")
	ErrCardNotPlain = errors.New("卡片载荷不是对象")
)

// cardRoot 解包卡片载荷，完整校验后再读兄弟字段；不递归抓任意字符串。
func cardRoot(raw string) map[string]any {
	root, _ := parseCardRoot(raw)
	return root
}

func parseCardRoot(raw string) (map[string]any, error) {
	var root map[string]any
	for range 4 {
		if !cardBounded(raw) {
			return nil, ErrCardTooBig
		}
		var v any
		if json.Unmarshal([]byte(raw), &v) != nil {
			return nil, ErrCardNotJSON
		}
		if s, ok := v.(string); ok {
			raw = s
			continue
		}
		var ok bool
		root, ok = v.(map[string]any)
		if !ok {
			return nil, ErrCardNotPlain
		}
		if _, exists := root["meta"]; exists {
			break
		}
		switch data := root["data"].(type) {
		case string:
			raw = data
			root = nil
			continue
		case map[string]any:
			if s, ok := data["data"].(string); ok {
				raw = s
			} else {
				b, _ := json.Marshal(data)
				raw = string(b)
			}
			root = nil
			continue
		}
		break
	}
	if root == nil {
		return nil, ErrCardNotPlain // 套了四层字符串还没解出对象
	}
	return root, nil
}

// FromCard 从明确的跳转字段识别已支持平台；通用桌面入口另用 CardURLs。解析不了的返回零值（要原因用 ParseCard）
func FromCard(raw string) ([]Link, CardInfo) {
	links, info, _ := ParseCard(raw)
	return links, info
}

// ParseCard 同 FromCard，载荷解析不了时返回原因（ErrCardTooBig、ErrCardNotJSON、ErrCardNotPlain）
func ParseCard(raw string) ([]Link, CardInfo, error) {
	root, err := parseCardRoot(raw)
	if err != nil {
		return nil, CardInfo{}, err
	}
	info := CardInfo{Prompt: cardString(root, "prompt"), Title: cardString(root, "title"), Description: cardString(root, "desc")}
	meta, _ := root["meta"].(map[string]any)
	var links []Link
	seen := make(map[string]struct{})
	add := func(value string) {
		// JSON 解码已还原普通的转义斜杠；额外兼容 A 的双重转义分享字段。
		value = strings.ReplaceAll(value, `\/`, `/`)
		lower := strings.ToLower(value)
		if strings.HasPrefix(lower, "https%3a%2f%2f") || strings.HasPrefix(lower, "http%3a%2f%2f") {
			if decoded, err := url.PathUnescape(value); err == nil {
				value = decoded
			}
		}
		for _, l := range Extract(value) {
			if _, ok := seen[l.Clean]; !ok {
				seen[l.Clean] = struct{}{}
				links = append(links, l)
			}
		}
	}
	for _, p := range cardPaths {
		node, _ := meta[p.group].(map[string]any)
		for _, field := range p.fields {
			add(cardString(node, field))
		}
	}
	// 不递归捡封面 URL；只收明确的跳转字段，避免把素材认作卡片目标。
	keys := make([]string, 0, len(meta))
	for key := range meta {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		node, _ := meta[key].(map[string]any)
		if info.Title == "" {
			info.Title = cardString(node, "title")
		}
		if info.Description == "" {
			info.Description = cardString(node, "desc")
		}
	}
	if info.Title == "" {
		info.Title = info.Prompt
	}
	return links, info, nil
}

func cardString(node map[string]any, key string) string {
	value, _ := node[key].(string)
	return value
}

func CardURLs(raw string) []string {
	root := cardRoot(raw)
	meta, _ := root["meta"].(map[string]any)
	var out []string
	seen := map[string]bool{}
	for _, p := range cardPaths {
		node, _ := meta[p.group].(map[string]any)
		for _, field := range p.fields {
			for _, found := range FindURLs(cardString(node, field)) {
				if !seen[found.Clean] && len(out) < 20 {
					seen[found.Clean] = true
					out = append(out, found.Open)
				}
			}
		}
	}
	return out
}

func cardBounded(raw string) bool {
	if len(raw) > maxCardBytes {
		return false
	}
	// 标准库负责 JSON 语法；这里只在解码分配前拦深层输入，不递归走外部树。
	depth, quoted, escaped := 0, false, false
	for i := range len(raw) {
		c := raw[i]
		if quoted {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > maxCardDepth {
				return false
			}
		case '}', ']':
			depth--
		}
	}
	return true
}
