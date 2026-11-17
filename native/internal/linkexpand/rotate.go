package linkexpand

import (
	"bytes"
	"crypto/rand"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"

	"sumika-native/internal/channel/types"
)

// Rotate180 把一张静态图转 180° 后作为新资源交回，原资源不动（调用方负责释放）；解不开、不是 PNG / JPEG 时返回错误。
// 只服务“图被协议端明确拒收后，换成合并转发再转 180° 发一次”：不加噪点、不改尺寸，格式保持（JPEG 质量 92）。
// 卡片是渲染好的整张图、封面已经画在里面，所以转整张图就等于封面也转了；动图、视频不转（调用方只传静态图）。
func Rotate180(res *types.MediaResource) (*types.MediaResource, error) {
	if res == nil {
		return nil, errors.New("没有图片")
	}
	var raw []byte
	if e := res.Read(func(r io.Reader) error {
		var err error
		raw, err = io.ReadAll(io.LimitReader(r, 64<<20))
		return err
	}); e != nil {
		return nil, e
	}
	src, format, e := image.Decode(bytes.NewReader(raw))
	if e != nil {
		return nil, e
	}
	if format != "jpeg" && format != "png" {
		return nil, errors.New("只旋转 PNG 和 JPEG")
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	flat := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(flat, flat.Bounds(), src, b.Min, draw.Src)
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			dst.SetNRGBA(w-1-x, h-1-y, flat.NRGBAAt(x, y))
		}
	}
	var out bytes.Buffer
	mime := "image/png"
	if format == "jpeg" {
		if e = jpeg.Encode(&out, dst, &jpeg.Options{Quality: 92}); e != nil {
			return nil, e
		}
		mime = "image/jpeg"
	} else if e = png.Encode(&out, dst); e != nil {
		return nil, e
	}
	return types.MemoryMedia(rand.Text(), mime, w, h, out.Bytes()), nil
}
