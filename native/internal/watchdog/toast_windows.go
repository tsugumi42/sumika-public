package watchdog

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unicode/utf16"

	"sumika-native/internal/toolchain"
)

// toastScript 弹一条系统通知：标题和正文从环境变量读（不拼进脚本，免得引号和换行出问题）。
// 守护进程不是带窗口的程序，没有自己的应用标识，借 Windows PowerShell 的标识弹；通知上显示的来源就是"Windows PowerShell"
const toastScript = `
$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$xml = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$text = $xml.GetElementsByTagName('text')
$text.Item(0).AppendChild($xml.CreateTextNode($env:SUMIKA_TOAST_TITLE)) > $null
$text.Item(1).AppendChild($xml.CreateTextNode($env:SUMIKA_TOAST_BODY)) > $null
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($toast)
`

// ToastNotifier 经 Windows PowerShell 弹桌面通知（Windows 10/11 自带的 powershell.exe，不依赖商店版 pwsh）
type ToastNotifier struct{}

// toastBodyMax：通知正文最多这么多个字，再长系统也只显示前几行
const toastBodyMax = 200

func (ToastNotifier) Notify(title, body string) error {
	if r := []rune(body); len(r) > toastBodyMax {
		body = string(r[:toastBodyMax]) + "…"
	}
	ps, ok := toolchain.Lookup("powershell51")
	if !ok {
		return errors.New("没找到 Windows PowerShell 5.1，弹不出系统通知")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps.Path, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encodePowerShell(toastScript))
	cmd.Env = append(os.Environ(), "SUMIKA_TOAST_TITLE="+title, "SUMIKA_TOAST_BODY="+body)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("powershell 弹通知失败：%w（%s）", err, firstLine(string(out)))
	}
	return nil
}

// encodePowerShell 是 -EncodedCommand 要的格式：UTF-16LE 再 base64
func encodePowerShell(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\r' || c == '\n' {
			return s[:i]
		}
	}
	return s
}
