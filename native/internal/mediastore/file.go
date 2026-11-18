package mediastore

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"path"
	"strings"
)

// FileCandidate 只决定是否值得读头；最终自动准入仍必须经过 FileKind。
func FileCandidate(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".md", ".csv", ".log", ".json", ".xml", ".yaml", ".yml", ".toml", ".ini", ".srt", ".ass":
		return "text"
	case ".pdf", ".rtf", ".doc", ".xls", ".ppt", ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp":
		return "document"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".avif", ".heic", ".heif":
		return "image"
	case ".mp4", ".m4v", ".mov", ".webm", ".mkv", ".avi":
		return "video"
	case ".mp3", ".wav", ".flac", ".ogg", ".opus", ".m4a", ".aac", ".amr":
		return "audio"
	}
	return ""
}

// FileKind 只接受扩展名与头部格式一致的常见文件。不打开、不执行、不解压。
// OOXML/ODF 复用 ZIP 本地条目结构核对包种类；头部不够证明时归未知，等待手动。
func FileKind(name string, h []byte) string {
	ext := strings.ToLower(path.Ext(name))
	mime, _, _ := strings.Cut(http.DetectContentType(h), ";")
	kind := FileCandidate(name)
	if bytes.HasPrefix(h, []byte("MZ")) || bytes.HasPrefix(h, []byte("\x7fELF")) {
		return ""
	}
	ok := false
	switch ext {
	case ".png":
		ok = mime == "image/png"
	case ".jpg", ".jpeg":
		ok = mime == "image/jpeg"
	case ".gif":
		ok = mime == "image/gif"
	case ".webp":
		ok = mime == "image/webp"
	case ".bmp":
		ok = mime == "image/bmp"
	case ".avif":
		ok = brand(h, "avif", "avis")
	case ".heic", ".heif":
		ok = brand(h, "heic", "heix", "hevc", "hevx", "mif1", "msf1")
	case ".mp4", ".m4v":
		ok = VideoExtension(h) == ".mp4"
	case ".mov":
		ok = VideoExtension(h) == ".mov"
	case ".webm":
		ok = VideoExtension(h) == ".webm"
	case ".mkv":
		ok = VideoExtension(h) == ".mkv"
	case ".avi":
		ok = mime == "video/avi"
	case ".mp3":
		ok = mime == "audio/mpeg" || len(h) >= 2 && h[0] == 0xff && h[1]&0xe6 == 0xe2
	case ".wav":
		ok = mime == "audio/wave"
	case ".flac":
		ok = bytes.HasPrefix(h, []byte("fLaC"))
	case ".ogg", ".opus":
		ok = mime == "application/ogg" && (bytes.Contains(h, []byte("OpusHead")) || bytes.Contains(h, []byte("\x01vorbis")))
	case ".m4a":
		ok = brand(h, "M4A ", "M4B ")
	case ".aac":
		ok = len(h) >= 2 && h[0] == 0xff && h[1]&0xf6 == 0xf0
	case ".amr":
		ok = bytes.HasPrefix(h, []byte("#!AMR\n")) || bytes.HasPrefix(h, []byte("#!AMR-WB\n"))
	case ".pdf":
		ok = bytes.HasPrefix(h, []byte("%PDF-"))
	case ".rtf":
		ok = bytes.HasPrefix(h, []byte("{\\rtf"))
	case ".xml":
		ok = mime == "text/xml"
	case ".doc", ".xls", ".ppt":
		// 复合文档共享 CFB 容器；不同 Office 扩展也需匹配对应流名。
		stream := "W\x00o\x00r\x00d\x00D\x00o\x00c\x00u\x00m\x00e\x00n\x00t\x00"
		if ext == ".xls" {
			stream = "W\x00o\x00r\x00k\x00b\x00o\x00o\x00k\x00"
		}
		if ext == ".ppt" {
			stream = "P\x00o\x00w\x00e\x00r\x00P\x00o\x00i\x00n\x00t\x00 \x00D\x00o\x00c\x00u\x00m\x00e\x00n\x00t\x00"
		}
		ok = bytes.HasPrefix(h, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) && bytes.Contains(h, []byte(stream))
	case ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp":
		ok = documentZIP(ext, h)
	default:
		ok = kind == "text" && mime == "text/plain" && !bytes.HasPrefix(h, []byte("#!"))
	}
	if ok {
		return kind
	}
	return ""
}

// 手动下载允许扩展名不匹配；播放能力只按原件的媒体头，不拿扩展名猜。
func FileMediaKind(h []byte) string {
	if VideoExtension(h) != "" {
		return "video"
	}
	mime, _, _ := strings.Cut(http.DetectContentType(h), ";")
	if strings.HasPrefix(mime, "image/") || brand(h, "avif", "avis", "heic", "heix", "hevc", "hevx", "mif1", "msf1") {
		return "image"
	}
	if strings.HasPrefix(mime, "audio/") || mime == "application/ogg" && (bytes.Contains(h, []byte("OpusHead")) || bytes.Contains(h, []byte("\x01vorbis"))) || bytes.HasPrefix(h, []byte("fLaC")) || brand(h, "M4A ", "M4B ") {
		return "audio"
	}
	return ""
}

func brand(h []byte, brands ...string) bool {
	if len(h) < 12 || string(h[4:8]) != "ftyp" {
		return false
	}
	for _, b := range brands {
		if string(h[8:12]) == b {
			return true
		}
	}
	return false
}

func documentZIP(ext string, h []byte) bool {
	prefix := map[string]string{".docx": "word/", ".xlsx": "xl/", ".pptx": "ppt/"}[ext]
	// 小文件可直接借标准库验证中央目录；大文件只看有界的本地条目。
	if z, err := zip.NewReader(bytes.NewReader(h), int64(len(h))); err == nil {
		types, body := false, false
		for _, f := range z.File {
			types = types || f.Name == "[Content_Types].xml"
			body = body || prefix != "" && strings.HasPrefix(f.Name, prefix)
			if prefix == "" && f.Name == "mimetype" {
				r, err := f.Open()
				if err != nil {
					return false
				}
				b, err := io.ReadAll(io.LimitReader(r, 128))
				r.Close()
				suffix := map[string]string{".odt": "text", ".ods": "spreadsheet", ".odp": "presentation"}[ext]
				return err == nil && string(b) == "application/vnd.oasis.opendocument."+suffix
			}
		}
		return types && body
	}
	contentTypes, content := false, false
	for len(h) >= 30 && bytes.Equal(h[:4], []byte{'P', 'K', 3, 4}) {
		nameLen, extraLen := int(binary.LittleEndian.Uint16(h[26:28])), int(binary.LittleEndian.Uint16(h[28:30]))
		start := 30 + nameLen + extraLen
		if start > len(h) {
			break
		}
		name := string(h[30 : 30+nameLen])
		contentTypes = contentTypes || name == "[Content_Types].xml"
		content = content || prefix != "" && strings.HasPrefix(name, prefix)
		if prefix == "" && name == "mimetype" && binary.LittleEndian.Uint16(h[8:10]) == 0 {
			suffix := map[string]string{".odt": "text", ".ods": "spreadsheet", ".odp": "presentation"}[ext]
			return bytes.HasPrefix(h[start:], []byte("application/vnd.oasis.opendocument."+suffix))
		}
		if contentTypes && content {
			return true
		}
		if binary.LittleEndian.Uint16(h[6:8])&9 != 0 {
			break
		}
		size := uint64(binary.LittleEndian.Uint32(h[18:22]))
		if size > uint64(len(h)-start) {
			break
		}
		h = h[start+int(size):]
	}
	return false
}
