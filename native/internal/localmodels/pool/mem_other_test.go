//go:build !windows

package pool

func peakRSS() uint64 { return 0 }
