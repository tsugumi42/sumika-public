package config

// MaskID 把号码打码成只留后两位；两位以内全部隐藏。配置错误和运行日志共用。
func MaskID(id string) string {
	r := []rune(id)
	if len(r) <= 2 {
		return "***"
	}
	return "***" + string(r[len(r)-2:])
}
