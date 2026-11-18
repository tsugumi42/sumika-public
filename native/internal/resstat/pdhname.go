package resstat

import (
	"strconv"
	"strings"
)

// parsePID 解析性能计数器的实例名 pid_<n>_luid_…（显存、引擎两类计数器同一个写法）；没有 pid 的实例（比如整卡的）返回 false
func parsePID(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "pid_")
	if !ok {
		return 0, false
	}
	end := strings.IndexByte(rest, '_')
	if end < 0 {
		end = len(rest)
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// engineType 取引擎实例名里的引擎类型（..._engtype_3D → 3D）；没有就是空串
func engineType(name string) string {
	if i := strings.Index(name, "engtype_"); i >= 0 {
		return name[i+len("engtype_"):]
	}
	return ""
}
