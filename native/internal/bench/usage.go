package bench

// Usage 是一个壳（连同全部子孙进程）跑一次用掉的本机资源（规格-表二执行器 第四节）
type Usage struct {
	CPUMs          int64  `json:"cpuMs"`          // 用户 + 内核时间，含已退出的进程
	PeakWorkingSet uint64 `json:"peakWorkingSet"` // 每次采样把各进程的工作集加起来，取最大（字节）
	PeakPrivate    uint64 `json:"peakPrivate"`    // 同上，私有字节
	PeakJobCommit  uint64 `json:"peakJobCommit"`  // Job 自己记的提交内存峰值（口径和工作集不同）
	Processes      uint32 `json:"processes"`      // 一共量到的进程数（进过 Job 的 + 跑出 Job、按父进程追到的）
	Escaped        uint32 `json:"escaped"`        // 其中跑出 Job 的（商店版 pwsh 和它起的进程）；它们不在 Job 的提交内存峰值里
}
