//go:build windows

package sendlock

import (
	"context"
	"errors"
	"testing"
)

func TestBunStoppedWindows(t *testing.T) {
	for _, tc := range []struct {
		name      string
		processes []bunProcess
		err       error
		want      bool
	}{
		{"没有Bun", nil, nil, true},
		{"omp不拦", []bunProcess{{Name: "bun.exe", CommandLine: `bun C:\tools\omp\main.js`}}, nil, true},
		{"别的进程不拦", []bunProcess{{Name: "other.exe"}}, nil, true},
		{"后台拦", []bunProcess{{Name: "bun.exe", CommandLine: `bun D:\sumika\daemon\main.ts`}}, nil, false},
		{"看门拦", []bunProcess{{Name: "BUN.EXE", CommandLine: `bun D:/sumika/scripts/supervise.ts`}}, nil, false},
		{"omp和后台并存", []bunProcess{{Name: "bun.exe", CommandLine: "bun omp.js"}, {Name: "bun.exe", CommandLine: "bun daemon/main.ts"}}, nil, false},
		{"命令行读不到", []bunProcess{{Name: "bun.exe"}}, nil, false},
		{"列表读不到", nil, errors.New("进程列表读不到"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bunStoppedWith(context.Background(), func(context.Context) ([]bunProcess, error) { return tc.processes, tc.err })
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("后台是否退出：got=%v err=%v，要 %v err=%v", got, err, tc.want, tc.err)
			}
		})
	}
}
