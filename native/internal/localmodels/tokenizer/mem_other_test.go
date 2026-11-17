//go:build !windows

package tokenizer

import "time"

// 不是 Windows 就不量工作集（只有 HeapAlloc）和 CPU 时间
func workingSetBytes() int64 { return 0 }

func processCPU() time.Duration { return 0 }
