package jsstr

import (
	"errors"
	"net/url"
	"strings"
)

// FileURLToPath 照 Node 的 url.fileURLToPath（Windows 那一支，lib/internal/url.js getPathFromURLWin32）：
// 路径里编码过的 / 和 \（%2F、%5C）不行；有主机名的拼成 \\主机\路径（localhost 当没有）；没有主机名的要是"盘符:"开头。
// 近似：URL 用 Go 的 net/url 解析（WHATWG 的细节、国际化域名没照搬，规格第七节 T16）
func FileURLToPath(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "file" {
		return "", errors.New("Invalid URL")
	}
	pathname := u.EscapedPath()
	for i := 0; i+2 < len(pathname); i++ {
		if pathname[i] != '%' {
			continue
		}
		third := pathname[i+2] | 0x20
		if (pathname[i+1] == '2' && third == 'f') || (pathname[i+1] == '5' && third == 'c') {
			return "", errors.New(`File URL path must not include encoded \ or / characters`)
		}
	}
	pathname = strings.ReplaceAll(pathname, "/", `\`)
	decoded, err := url.PathUnescape(pathname)
	if err != nil {
		return "", errors.New("URI malformed")
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		host = ""
	}
	if host != "" {
		return `\\` + host + decoded, nil
	}
	if len(decoded) < 3 {
		return "", errors.New("File URL path must be absolute")
	}
	letter := decoded[1] | 0x20
	if letter < 'a' || letter > 'z' || decoded[2] != ':' {
		return "", errors.New("File URL path must be absolute")
	}
	return decoded[1:], nil
}
