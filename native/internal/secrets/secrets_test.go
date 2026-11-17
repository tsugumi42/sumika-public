package secrets

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 和 Bun 的文件结构独立造数据，不借用读取器里的结构或解密函数。
type bunFile struct {
	V     int               `json:"v"`
	Wraps map[string]any    `json:"wraps"`
	Data  map[string]string `json:"data"`
}

func writeBunFile(t *testing.T, file string, shape bunFile) {
	t.Helper()
	b, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireError(t *testing.T, file string, want error) error {
	t.Helper()
	s, err := Open(file)
	if s != nil || !errors.Is(err, want) {
		t.Errorf("Open 应返回空 Store 和 %v，得到 Store=%t、错误=%v", want, s != nil, err)
	}
	if err != nil && strings.Contains(err.Error(), "编的令牌") {
		t.Error("错误泄漏了密钥值")
	}
	return err
}

func TestOpenMissingFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "secrets.bin")
	requireError(t, file, ErrNotFound)
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Error("Open 不应创建不存在的文件")
	}
}

// scryptWrap 造一份参数合法的包裹，只用来试结构检查（不是真能解开的密文）。
func scryptWrap() map[string]any {
	b64 := base64.StdEncoding.EncodeToString
	return map[string]any{
		"kdf": "scrypt", "N": 1 << 14, "r": 8, "p": 1,
		"salt": b64(make([]byte, 16)), "iv": b64(make([]byte, 12)),
		"tag": b64(make([]byte, 16)), "ct": b64(make([]byte, 32)),
	}
}

func TestOpenBadFormat(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString
	valid := func() bunFile {
		return bunFile{V: 1, Wraps: map[string]any{"dpapi": b64([]byte("编的封装"))}, Data: map[string]string{
			"iv": b64(make([]byte, 12)), "tag": b64(make([]byte, 16)), "ct": b64([]byte("编的密文")),
		}}
	}
	cases := []struct {
		name   string
		change func(*bunFile)
	}{
		{"version", func(s *bunFile) { s.V = 2 }},
		{"wraps", func(s *bunFile) { s.Wraps = nil }},
		{"data", func(s *bunFile) { s.Data = nil }},
		{"iv-base64", func(s *bunFile) { s.Data["iv"] = "编的令牌" }},
		{"iv-size", func(s *bunFile) { s.Data["iv"] = b64(make([]byte, 11)) }},
		{"tag-base64", func(s *bunFile) { s.Data["tag"] = "编的令牌" }},
		{"tag-size", func(s *bunFile) { s.Data["tag"] = b64(make([]byte, 15)) }},
		{"ct-base64", func(s *bunFile) { s.Data["ct"] = "编的令牌" }},
		{"ct-empty", func(s *bunFile) { s.Data["ct"] = "" }},
		{"dpapi-base64", func(s *bunFile) { s.Wraps["dpapi"] = "编的令牌" }},
		{"no-known-wrap", func(s *bunFile) { s.Wraps = map[string]any{"future": "编的封装"} }},
		{"password-without-recovery", func(s *bunFile) { s.Wraps = map[string]any{"password": scryptWrap()} }},
		{"recovery-without-password", func(s *bunFile) { s.Wraps = map[string]any{"recovery": scryptWrap()} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shape := valid()
			tc.change(&shape)
			file := filepath.Join(t.TempDir(), "secrets.bin")
			writeBunFile(t, file, shape)
			requireError(t, file, ErrFormat)
		})
	}
	for _, bad := range []string{`{"编的令牌":`, `null`, `{"v":"编的令牌"}`, `{"v":1}`} {
		t.Run("json", func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "secrets.bin")
			if err := os.WriteFile(file, []byte(bad), 0o600); err != nil {
				t.Fatal(err)
			}
			requireError(t, file, ErrFormat)
		})
	}
}
