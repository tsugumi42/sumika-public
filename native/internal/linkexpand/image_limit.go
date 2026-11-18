package linkexpand

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"sumika-native/internal/linkreader"
)

// 发送前的图片体积检查（ADR-333）。
//
// QQ 和 SnowLuma 都没有公开的单图上限：SnowLuma 的图片走 base64 直传，源码里没有图片体积校验，QQ 自己的上限也查不到权威说法；
// 所以用保守值 20 MB（也是下载端 imageLimit 的上限），写进配置 linkReader.expand.imageMaxMB（1～20，默认 20），
// 超过的图不下载、不发送，写明“超过大小上限”退文字。X 的图走 name=orig 取原图，原图超限或取不到时退 name=large 再试一次。

// defaultImageMaxBytes 是配置没给时的上限，等于下载端上限
const defaultImageMaxBytes = imageLimit

func (p *Preparer) imageMax() int64 {
	if p.ImageMaxBytes > 0 && p.ImageMaxBytes < defaultImageMaxBytes {
		return p.ImageMaxBytes
	}
	return defaultImageMaxBytes
}

// largeVariant 把 X 图片地址里的 name=orig 换成 name=large；不是 X 的原图地址返回 false
func largeVariant(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != "pbs.twimg.com" {
		return "", false
	}
	q := u.Query()
	if q.Get("name") != "orig" {
		return "", false
	}
	q.Set("name", "large")
	u.RawQuery = q.Encode()
	return u.String(), true
}

// canTryLarge：原图超限，或原图取不到（地址失效、连不上）时，值得换 large 试一次；限流、要登录换 large 也一样不行
func canTryLarge(err error) bool {
	if errors.Is(err, linkreader.ErrMediaLimit) {
		return true
	}
	var fe *linkreader.FetchError
	return errors.As(err, &fe) && (fe.Kind == "not_found" || fe.Kind == "network")
}

// loadImage 下载一张图并检查体积：超过上限当作 ErrMediaLimit；X 的原图超限或取不到时退 large 再试一次
func (p *Preparer) loadImage(ctx context.Context, platform string, src linkreader.Media) (Loaded, error) {
	limit := p.imageMax()
	loaded, err := p.Media.Image(ctx, platform, src)
	if err == nil && loaded.Resource != nil && loaded.Resource.Size > limit {
		err = errors.Join(linkreader.ErrMediaLimit, loaded.Resource.Release())
		loaded = Loaded{}
	}
	if err == nil || platform != "x" || !canTryLarge(err) {
		return loaded, err
	}
	alt, ok := largeVariant(src.URL.Value)
	if !ok {
		return loaded, err
	}
	small := src
	small.URL.Value = alt
	small.Size = linkreader.Field[int64]{}
	l2, err2 := p.Media.Image(ctx, platform, small)
	if err2 == nil && l2.Resource != nil && l2.Resource.Size > limit {
		err2 = errors.Join(linkreader.ErrMediaLimit, l2.Resource.Release())
	}
	if err2 != nil {
		return Loaded{}, err // 两档都不行，报原图那一档的原因
	}
	l2.Note = fmt.Sprintf("原图取不到或超过 %d MB，用的是 large 档（较小）", limit/1_000_000)
	return l2, nil
}
