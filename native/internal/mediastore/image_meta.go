package mediastore

// 元数据只在下载和后台补扫时读；压缩像素用偏移跳过，不解码、不读全文校验。
import (
	"encoding/binary"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

type ImageMeta struct {
	Width, Height int
	Animated      bool
}

func ReadImageMeta(r io.ReaderAt, size int64) ImageMeta {
	cfg, format, err := image.DecodeConfig(io.NewSectionReader(r, 0, size))
	if err != nil {
		return ImageMeta{}
	}
	m := ImageMeta{Width: cfg.Width, Height: cfg.Height}
	switch format {
	case "jpeg":
		// 和界面 decodeStatic 一致：只有长边不超过 2048 的照片按 EXIF 转正。
		if max(cfg.Width, cfg.Height) <= 2048 && jpegOrientation(r) >= 5 {
			m.Width, m.Height = m.Height, m.Width
		}
	case "gif":
		m.Animated = GIFFrameCount(r) > 1
	case "webp":
		_, m.Animated = WebPFrameCount(r, size)
	}
	return m
}

// GIFFrameCount 只数图像描述符，跳过调色板、扩展和 LZW 子块；不分配帧像素。
func GIFFrameCount(r io.ReaderAt) int {
	var head [13]byte
	if _, err := r.ReadAt(head[:], 0); err != nil {
		return 0
	}
	p := int64(13)
	if head[10]&0x80 != 0 {
		p += 3 << (head[10]&7 + 1)
	}
	n := 0
	var b [10]byte
	for {
		if _, err := r.ReadAt(b[:1], p); err != nil {
			return n
		}
		switch b[0] {
		case 0x21:
			p += 2
		case 0x2c:
			if _, err := r.ReadAt(b[:], p); err != nil {
				return n
			}
			n++
			p += 10
			if b[9]&0x80 != 0 {
				p += 3 << (b[9]&7 + 1)
			}
			p++ // LZW 字长
		default:
			return n
		}
		for {
			if _, err := r.ReadAt(b[:1], p); err != nil {
				return n
			}
			p++
			if b[0] == 0 {
				break
			}
			p += int64(b[0])
		}
	}
}

// WebPFrameCount 共用界面的 RIFF 判定：有 ANIM 是动图，ANMF 每块一帧，块按偶数字节对齐。
func WebPFrameCount(r io.ReaderAt, size int64) (frames int, anim bool) {
	var h [8]byte
	for p := int64(12); p+8 <= size; {
		if _, err := r.ReadAt(h[:], p); err != nil {
			break
		}
		n := int64(binary.LittleEndian.Uint32(h[4:]))
		switch string(h[:4]) {
		case "ANIM":
			anim = true
		case "ANMF":
			frames++
		}
		if n > size {
			break
		}
		p += 8 + n + n&1
	}
	return
}

// jpegOrientation 沿用撤回版本的口径，只扫 SOS 前的 APP1，不读取压缩像素。
func jpegOrientation(r io.ReaderAt) int {
	var header [4]byte
	for offset := int64(2); ; {
		if _, err := r.ReadAt(header[:], offset); err != nil || header[0] != 0xff {
			return 0
		}
		marker, size := header[1], int64(binary.BigEndian.Uint16(header[2:]))
		if marker == 0xda || size < 2 {
			return 0
		}
		if marker == 0xe1 && size > 16 {
			seg := make([]byte, size-2)
			if _, err := r.ReadAt(seg, offset+4); err != nil {
				return 0
			}
			if string(seg[:6]) == "Exif\x00\x00" {
				return tiffOrientation(seg[6:])
			}
		}
		offset += 2 + size
	}
}

func tiffOrientation(t []byte) int {
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0
	}
	ifd := int(order.Uint32(t[4:]))
	if ifd < 8 || ifd+2 > len(t) {
		return 0
	}
	n := int(order.Uint16(t[ifd:]))
	for e := ifd + 2; n > 0 && e+12 <= len(t); e, n = e+12, n-1 {
		if order.Uint16(t[e:]) == 0x0112 && order.Uint16(t[e+2:]) == 3 {
			if o := int(order.Uint16(t[e+8:])); o >= 1 && o <= 8 {
				return o
			}
			return 0
		}
	}
	return 0
}
