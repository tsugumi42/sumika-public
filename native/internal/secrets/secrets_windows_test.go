//go:build windows

package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func protectForBun(t *testing.T, data []byte) string {
	t.Helper()
	input := windows.DataBlob{Size: uint32(len(data)), Data: unsafe.SliceData(data)}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		t.Fatal(err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return base64.StdEncoding.EncodeToString(unsafe.Slice(out.Data, int(out.Size)))
}

// 照 secrets.ts 的 seal/save：随机 32 字节数据密钥、12 字节 IV、独立 tag、用途作附加数据。
func makeBunFile(t *testing.T, plain []byte, use string) bunFile {
	t.Helper()
	key := make([]byte, 32)
	iv := make([]byte, 12)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	sealed := gcm.Seal(nil, iv, plain, []byte(use))
	b64 := base64.StdEncoding.EncodeToString
	return bunFile{V: 1, Wraps: map[string]any{"dpapi": protectForBun(t, key)}, Data: map[string]string{
		"iv": b64(iv), "tag": b64(sealed[len(sealed)-16:]), "ct": b64(sealed[:len(sealed)-16]),
	}}
}

func requireValues(t *testing.T, file string, want map[string]string) {
	t.Helper()
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	for name, expected := range want {
		value, ok := s.Get(name)
		if !ok || value != expected {
			t.Errorf("Get(%q) 应读到原值，存在=%t、值一致=%t", name, ok, value == expected)
		}
	}
	if value, ok := s.Get("qq.300.token"); ok || value != "" {
		t.Error("缺少的名字应返回空字符串、false")
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("Open/Get 改写了密钥文件")
	}
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "secrets.bin" {
			t.Error("只读打开不应产生额外文件")
		}
	}
}

func TestOpenBunFormat(t *testing.T) {
	values := map[string]string{"qq.100.token": "编的令牌一\n\"\\", "qq.200.token": "编的令牌二", "empty": ""}
	plain, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "secrets.bin")
	shape := makeBunFile(t, plain, "data")
	// 不认识的包裹不挡读取，写回时原样保留。
	shape.Wraps["future"] = map[string]string{"kdf": "不读取的编造内容"}
	writeBunFile(t, file, shape)
	requireValues(t, file, values)
}

func TestOpenLocked(t *testing.T) {
	shape := makeBunFile(t, []byte(`{"qq.100.token":"编的令牌"}`), "data")
	wrapped, err := base64.StdEncoding.DecodeString(shape.Wraps["dpapi"].(string))
	if err != nil {
		t.Fatal(err)
	}
	// 改了本机那份的一个字节：换了 Windows 用户 / 重装系统后就是这种解不开。
	wrapped[len(wrapped)-1] ^= 1
	shape.Wraps["dpapi"] = base64.StdEncoding.EncodeToString(wrapped)
	file := filepath.Join(t.TempDir(), "secrets.bin")
	writeBunFile(t, file, shape)
	err = requireError(t, file, ErrLocked)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatal("锁着的错误应带 LockedError，调用方能拿到 HasPassphrase")
	}
	if locked.HasPassphrase {
		t.Error("没有主密码包裹时 HasPassphrase 应为假")
	}
}

func TestOpenAuthentication(t *testing.T) {
	for _, name := range []string{"tag", "use"} {
		t.Run(name, func(t *testing.T) {
			use := "data"
			if name == "use" {
				use = "password"
			}
			shape := makeBunFile(t, []byte(`{"qq.100.token":"编的令牌"}`), use)
			if name == "tag" {
				tag, err := base64.StdEncoding.DecodeString(shape.Data["tag"])
				if err != nil {
					t.Fatal(err)
				}
				tag[0] ^= 1
				shape.Data["tag"] = base64.StdEncoding.EncodeToString(tag)
			}
			file := filepath.Join(t.TempDir(), "secrets.bin")
			writeBunFile(t, file, shape)
			requireError(t, file, ErrFormat)
		})
	}
}

func TestOpenInvalidValues(t *testing.T) {
	for _, plain := range []string{`null`, `[]`, `{"编的令牌":`, `{"qq.100.token":null}`, `{"qq.100.token":{"编的令牌":true}}`} {
		t.Run("values", func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "secrets.bin")
			writeBunFile(t, file, makeBunFile(t, []byte(plain), "data"))
			requireError(t, file, ErrFormat)
		})
	}
}
