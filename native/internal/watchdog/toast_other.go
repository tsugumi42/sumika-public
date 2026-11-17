//go:build !windows

package watchdog

import "errors"

// ToastNotifier 只有 Windows 上能弹
type ToastNotifier struct{}

func (ToastNotifier) Notify(title, body string) error {
	return errors.New("只有 Windows 能弹桌面通知")
}
