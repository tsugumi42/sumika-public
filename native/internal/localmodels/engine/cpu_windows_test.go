//go:build windows

package engine

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// cpuName 读注册表里的处理器名，参考数据的 ref-meta.json 里记的是同一个字段（ort 包的测试同一个写法）。
func cpuName() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("ProcessorNameString")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}
