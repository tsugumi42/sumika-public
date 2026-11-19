package policy

import "strings"

// ───────────── Node 的 path.win32.join / normalize（照 Node 的 lib/path.js 搬） ─────────────
// judge 里"换过目录以后的相对路径接在新目录后面"用的是 win32.join：它保留结尾的分隔符、. 和 .. 的处理也和 Go 的 filepath 不一样

func isSep(c byte) bool { return c == '/' || c == '\\' }

func isDeviceRoot(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// normalizeString 照 Node 的 normalizeString(path, allowAboveRoot, '\\', isPathSeparator)
func normalizeString(path string, allowAboveRoot bool) string {
	res := ""
	lastSegmentLength, lastSlash, dots := 0, -1, 0
	var code byte
	for i := 0; i <= len(path); i++ {
		if i < len(path) {
			code = path[i]
		} else if isSep(code) {
			break
		} else {
			code = '/'
		}
		if isSep(code) {
			if lastSlash == i-1 || dots == 1 {
				// 空段、"."：什么都不做
			} else if dots == 2 {
				if len(res) < 2 || lastSegmentLength != 2 || res[len(res)-1] != '.' || res[len(res)-2] != '.' {
					if len(res) > 2 {
						if k := strings.LastIndexByte(res, '\\'); k == -1 {
							res, lastSegmentLength = "", 0
						} else {
							res = res[:k]
							lastSegmentLength = len(res) - 1 - strings.LastIndexByte(res, '\\')
						}
						lastSlash, dots = i, 0
						continue
					} else if len(res) != 0 {
						res, lastSegmentLength, lastSlash, dots = "", 0, i, 0
						continue
					}
				}
				if allowAboveRoot {
					if len(res) > 0 {
						res += `\..`
					} else {
						res = ".."
					}
					lastSegmentLength = 2
				}
			} else {
				if len(res) > 0 {
					res += `\` + path[lastSlash+1:i]
				} else {
					res = path[lastSlash+1 : i]
				}
				lastSegmentLength = i - lastSlash - 1
			}
			lastSlash, dots = i, 0
		} else if code == '.' && dots != -1 {
			dots++
		} else {
			dots = -1
		}
	}
	return res
}

// win32Normalize 照 Node 的 path.win32.normalize
func win32Normalize(path string) string {
	n := len(path)
	if n == 0 {
		return "."
	}
	rootEnd := 0
	device := ""
	hasDevice := false
	isAbsolute := false
	code := path[0]
	if n == 1 {
		if code == '/' {
			return `\`
		}
		return path
	}
	if isSep(code) {
		isAbsolute = true
		if isSep(path[1]) {
			j, last := 2, 2
			for j < n && !isSep(path[j]) {
				j++
			}
			if j < n && j != last {
				first := path[last:j]
				last = j
				for j < n && isSep(path[j]) {
					j++
				}
				if j < n && j != last {
					last = j
					for j < n && !isSep(path[j]) {
						j++
					}
					if j == n {
						return `\\` + first + `\` + path[last:] + `\`
					}
					if j != last {
						device, hasDevice, rootEnd = `\\`+first+`\`+path[last:j], true, j
					}
				}
			}
		} else {
			rootEnd = 1
		}
	} else if isDeviceRoot(code) && path[1] == ':' {
		device, hasDevice, rootEnd = path[:2], true, 2
		if n > 2 && isSep(path[2]) {
			isAbsolute, rootEnd = true, 3
		}
	}
	tail := ""
	if rootEnd < n {
		tail = normalizeString(path[rootEnd:], !isAbsolute)
	}
	if tail == "" && !isAbsolute {
		tail = "."
	}
	if tail != "" && isSep(path[n-1]) {
		tail += `\`
	}
	if !hasDevice {
		if isAbsolute {
			return `\` + tail
		}
		return tail
	}
	if isAbsolute {
		return device + `\` + tail
	}
	return device + tail
}

// win32Join 照 Node 的 path.win32.join
func win32Join(parts ...string) string {
	joined, first := "", ""
	has := false
	for _, p := range parts {
		if p == "" {
			continue
		}
		if !has {
			joined, first, has = p, p, true
		} else {
			joined += `\` + p
		}
	}
	if !has {
		return "."
	}
	needsReplace := true
	slashCount := 0
	if isSep(first[0]) {
		slashCount++
		if len(first) > 1 && isSep(first[1]) {
			slashCount++
			if len(first) > 2 {
				if isSep(first[2]) {
					slashCount++
				} else {
					needsReplace = false
				}
			}
		}
	}
	if needsReplace {
		for slashCount < len(joined) && isSep(joined[slashCount]) {
			slashCount++
		}
		if slashCount >= 2 {
			joined = `\` + joined[slashCount:]
		}
	}
	return win32Normalize(joined)
}
