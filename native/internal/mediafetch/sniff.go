package mediafetch

import (
	"bytes"
	"encoding/binary"
)

// isBitmap 看文件头认不认得是位图（PNG、JPEG、GIF、WebP、BMP、AVIF、HEIC），不看扩展名也不信服务器给的类型
// （规格-QQ渠道第五节"看内容不看扩展名"）。SVG、HTML、压缩包一律不收
func isBitmap(h []byte) bool {
	switch {
	case bytes.HasPrefix(h, []byte("\x89PNG\r\n\x1a\n")):
		return true
	case len(h) >= 3 && h[0] == 0xFF && h[1] == 0xD8 && h[2] == 0xFF:
		return true
	case bytes.HasPrefix(h, []byte("GIF87a")), bytes.HasPrefix(h, []byte("GIF89a")):
		return true
	case len(h) >= 12 && bytes.Equal(h[0:4], []byte("RIFF")) && bytes.Equal(h[8:12], []byte("WEBP")):
		return true
	case len(h) >= 18 && h[0] == 'B' && h[1] == 'M':
		// BMP 只有两个字母太松：再核对后面信息头的大小是已知的几种
		switch binary.LittleEndian.Uint32(h[14:18]) {
		case 12, 40, 52, 56, 64, 108, 124:
			return true
		}
		return false
	case len(h) >= 12 && bytes.Equal(h[4:8], []byte("ftyp")):
		switch string(h[8:12]) {
		case "avif", "avis", "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1":
			return true
		}
	}
	return false
}
