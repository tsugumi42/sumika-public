//go:build windows

package approval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// 只改 t.TempDir 内对象；事先持有 WRITE_DAC 句柄，拒绝后仍能恢复测试对象的 ACL。
func TestRealStrictDeniedTarget(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "blocked")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("编的"), 0o600); err != nil {
		t.Fatal(err)
	}
	denySD, err := windows.SecurityDescriptorFromString("D:P(D;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	deny, _, err := denySD.DACL()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, dir} {
		func() {
			sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			original, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			name, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(h)
			defer func() {
				if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, original, nil); err != nil {
					t.Errorf("恢复测试对象 ACL：%v", err)
				}
			}()
			if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, deny, nil); err != nil {
				t.Fatal(err)
			}
			for _, resolve := range []func(string) (string, error){RealStrict, RealForWrite} {
				got, err := resolve(path)
				if got != "" || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
					t.Errorf("%s 拒绝访问时不能回退成真实路径：%q %v", filepath.Base(path), got, err)
				}
			}
			if _, err := os.ReadFile(file); !os.IsPermission(err) {
				t.Errorf("测试对象必须确实不可读：%v", err)
			}
		}()
	}
}
