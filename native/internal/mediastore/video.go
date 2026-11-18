package mediastore

import (
	"bytes"
	"encoding/binary"
)

// VideoExtension 按容器头定后缀，不信平台文件名；AVIF/HEIC 也用 ftyp，必须区分品牌。
func VideoExtension(head []byte) string {
	if len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "AVI " {
		return ".avi"
	}
	if len(head) >= 16 && string(head[4:8]) == "ftyp" {
		size := int(binary.BigEndian.Uint32(head[:4]))
		if size < 16 || size%4 != 0 || size > len(head) {
			return ""
		}
		brand := string(head[8:12])
		if brand == "qt  " {
			return ".mov"
		}
		switch brand {
		case "isom", "iso2", "iso3", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "M4V ", "MSNV", "3gp4", "3gp5", "3gp6":
			return ".mp4"
		}
	}
	// EBML 的 DocType 是 0x4282；长度用 VINT，只接受完整、明确的 webm/matroska。
	if len(head) >= 5 && bytes.Equal(head[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		size, n := ebmlSize(head[4:])
		if n == 0 || size > uint64(len(head)-4-n) {
			return ""
		}
		body := head[4+n : 4+n+int(size)]
		for len(body) > 0 {
			idn := 1
			for idn <= 4 && body[0]&(0x80>>(idn-1)) == 0 {
				idn++
			}
			if idn > 4 || len(body) < idn+1 {
				return ""
			}
			id := body[:idn]
			size, n := ebmlSize(body[idn:])
			if n == 0 || size > uint64(len(body)-idn-n) {
				return ""
			}
			value := body[idn+n : idn+n+int(size)]
			if bytes.Equal(id, []byte{0x42, 0x82}) {
				switch string(value) {
				case "webm":
					return ".webm"
				case "matroska":
					return ".mkv"
				}
				return ""
			}
			body = body[idn+n+int(size):]
		}
	}
	return ""
}

func ebmlSize(b []byte) (uint64, int) {
	if len(b) == 0 {
		return 0, 0
	}
	n := 1
	for n <= 8 && b[0]&(0x80>>(n-1)) == 0 {
		n++
	}
	if n > 8 || len(b) < n {
		return 0, 0
	}
	v := uint64(b[0] & byte(0xff>>n))
	for _, x := range b[1:n] {
		v = v<<8 | uint64(x)
	}
	return v, n
}
