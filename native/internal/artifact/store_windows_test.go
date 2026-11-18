//go:build windows

package artifact

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestArtifactCommitFailuresWindows(t *testing.T) {
	s, inv := resultFixture(t)
	resultStep(t, s, inv, "read")
	ref := putText(t, s, inv, "真实文件共享权限错误")
	path, err := windows.UTF16PtrFromString(filepath.Join(s.Dir, ref.ID))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	_, err = s.Read(context.Background(), inv.Scope, ref.ID, 0, 1024)
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("真实权限错误没有透传：%v", err)
	}
}
