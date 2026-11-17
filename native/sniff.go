package main

// 按字节头认动图、数帧（规格-原生界面第二版 3.5 的"统一入口"里认格式的那一半）。
// 不看 mime，不看 seg.Animated：那个标记入库时认的，只用来预判；这里认的才算数。
// 只读头和块的长度，不解压像素，所以对很大的文件也快；读到越界就当没有这一块，不 panic。

import (
	"bytes"
	"encoding/binary"

	"sumika-native/internal/mediastore"
)

type animKind int

const (
	animNone animKind = iota
	animGIF
	animAPNG
	animWebP
)

// sniffAnim 认出是哪种动图格式、声明了几帧。不是动图（静态 GIF 只有一帧也算 GIF，帧数 1）返回 animNone。
// 帧数是文件头 / 块结构里声明的数，不等于一定解得出来（截断、不一致的文件解码时才报错）
func sniffAnim(data []byte) (animKind, int) {
	switch {
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return animGIF, mediastore.GIFFrameCount(bytes.NewReader(data))
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		if n := apngFrameCount(data); n > 0 {
			return animAPNG, n
		}
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		if n, anim := mediastore.WebPFrameCount(bytes.NewReader(data), int64(len(data))); anim {
			return animWebP, n
		}
	}
	return animNone, 0
}

// apngFrameCount 在第一个 IDAT 之前找 acTL 块，返回它声明的帧数；没有 acTL（普通 PNG）返回 0
func apngFrameCount(data []byte) int {
	p := 8
	for p+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[p:]))
		typ := string(data[p+4 : p+8])
		switch typ {
		case "acTL":
			if size >= 8 && p+16 <= len(data) {
				return int(binary.BigEndian.Uint32(data[p+8:]))
			}
			return 0
		case "IDAT", "IEND":
			return 0
		}
		if size < 0 || size > len(data) {
			return 0
		}
		p += 12 + size // 长度 + 类型 + 内容 + 校验
	}
	return 0
}
