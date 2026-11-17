package main

// 运行页资源采样器接进桌面程序：整个程序一个 resstat.Sampler，界面和 sumika-ctl 共用。
// 采样器没有协程和定时器，只在有人来要数据时采一次；这里只负责第一次用到时建起来。

import "sumika-native/internal/resstat"

func (s *shell) sampler() *resstat.Sampler {
	s.resOnce.Do(func() { s.res = resstat.New(resstat.Options{}) })
	return s.res
}
