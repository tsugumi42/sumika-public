//go:build !windows

package turn

import "time"

// 影子对比的计时和内存只在 Windows 上量（主人的机器）；别的系统给零值
func processStart() time.Time { return time.Time{} }

func processMemoryMB() (rss, peak float64) { return 0, 0 }
