//go:build windows

package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 测试自己造 Bun 格式的密钥，DPAPI 只包这次生成的随机密钥，不读真实密钥文件。
func writeTestSecrets(t *testing.T, file string, values map[string]string) {
	t.Helper()
	key := make([]byte, 32)
	iv := make([]byte, 12)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	input := windows.DataBlob{Size: uint32(len(key)), Data: unsafe.SliceData(key)}
	var wrapped windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &wrapped); err != nil {
		t.Fatal(err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(wrapped.Data)))
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	sealed := gcm.Seal(nil, iv, plain, []byte("data"))
	b64 := base64.StdEncoding.EncodeToString
	shape := map[string]any{"v": 1, "wraps": map[string]string{"dpapi": b64(unsafe.Slice(wrapped.Data, int(wrapped.Size)))}, "data": map[string]string{"iv": b64(iv), "tag": b64(sealed[len(sealed)-16:]), "ct": b64(sealed[:len(sealed)-16])}}
	raw, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestTokenMissingKeyAndEmptyValue(t *testing.T) {
	t.Parallel()
	for _, values := range []map[string]string{{"qq.200.token": "编的别的令牌"}, {"qq.100.token": ""}} {
		t.Run("密钥缺少可用值", func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.writeConfig("")
			writeTestSecrets(t, filepath.Join(e.data, "config", "secrets.bin"), values)
			a, err := Open(context.Background(), e.options())
			if a != nil {
				a.cleanup()
			}
			if err == nil {
				t.Fatal("密钥没有可用值也启动了")
			}
			if strings.Contains(err.Error(), "qq."+testAccount+".token") {
				t.Fatalf("缺键错误泄露账号：%v", err)
			}
			if !strings.Contains(err.Error(), "访问令牌") {
				t.Fatalf("错误没说明缺访问令牌：%v", err)
			}
		})
	}
}

// 循环链接让 Stat 返回解析路径失败；区别于真正缺文件，也不依赖运行用户的 ACL 权限。
func TestMemoryStatFailureStopsOpen(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	file := filepath.Join(e.data, "go", "characters", testChar, "memory.db")
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, file); err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(file)
	if statErr == nil || errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("夹具没有产生非不存在的 Stat 错误：%v", statErr)
	}
	a, err := Open(context.Background(), e.options())
	if a != nil {
		a.cleanup()
	}
	if err == nil {
		t.Fatalf("记忆库 Stat 失败被当成没有记忆库：%v", statErr)
	}
	if !strings.Contains(err.Error(), "记忆") {
		t.Fatalf("错误没说是哪份记忆库：%v", err)
	}
}
