//go:build windows

package sendlock

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"

	"sumika-native/internal/toolchain"
)

type bunProcess struct {
	Name        string
	CommandLine string
}

func bunStopped(ctx context.Context) (bool, error) {
	return bunStoppedWith(ctx, listBunProcesses)
}

func listBunProcesses(ctx context.Context) ([]bunProcess, error) {
	// CIM 能直接读命令行；只查 Bun，避免把 omp 等别的 Bun 程序当成旧后台。
	const script = `$ErrorActionPreference = 'Stop'; [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); ConvertTo-Json -Compress -InputObject @(Get-CimInstance Win32_Process -Filter "Name = 'bun.exe' OR Name = 'bun'" | Select-Object Name, CommandLine)`
	ps, ok := toolchain.Lookup("powershell")
	if !ok {
		return nil, errors.New("没找到 PowerShell，查不了 Bun 进程")
	}
	b, err := exec.CommandContext(ctx, ps.Path, "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return nil, err
	}
	var processes []bunProcess
	if err := json.Unmarshal(b, &processes); err != nil {
		return nil, err
	}
	return processes, nil
}

func bunStoppedWith(ctx context.Context, list func(context.Context) ([]bunProcess, error)) (bool, error) {
	processes, err := list(ctx)
	if err != nil {
		return false, err
	}
	for _, p := range processes {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !strings.EqualFold(p.Name, "bun.exe") && !strings.EqualFold(p.Name, "bun") {
			continue
		}
		cmd := strings.ToLower(strings.ReplaceAll(p.CommandLine, `\`, "/"))
		if strings.TrimSpace(cmd) == "" || strings.Contains(cmd, "daemon/main.ts") || strings.Contains(cmd, "scripts/supervise.ts") {
			return false, nil
		}
	}
	return true, ctx.Err()
}

func connectionRefused(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }
