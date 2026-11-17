package linkreader

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const boothSource = "booth_json"
const boothPageSource = "booth_page"

// BoothInfo 保存价格字符串和各个规格；BOOTH 的 price 是展示用文字（如 "¥ 600~"），不解析也不换算。
// Variations 是商品 JSON 的 variations（规格名、价格、有没有库存、类型）；接口没给就是 nil。
type BoothInfo struct {
	Price      Field[string]    `json:"price"`
	Variations []BoothVariation `json:"variations,omitempty"`
}

// BoothVariation 是一个规格：Price 是日元整数（接口原样）；SoldOut 来自 is_empty_stock；Type 是 digital、via_warehouse 这类接口原词。
type BoothVariation struct {
	Name    Field[string] `json:"name"`
	Price   Field[int64]  `json:"price"`
	SoldOut bool          `json:"soldOut,omitempty"`
	Type    string        `json:"type,omitempty"`
}

type boothItem struct {
	ID          decimalID `json:"id"`
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Adult       *bool     `json:"is_adult"`
	SoldOut     *bool     `json:"is_sold_out"`
	EndOfSale   *bool     `json:"is_end_of_sale"`
	Published   *string   `json:"published_at"`
	Price       *string   `json:"price"`
	Wishes      *int64    `json:"wish_lists_count"`
	Category    *struct {
		Name *string `json:"name"`
	} `json:"category"`
	Tags *[]struct {
		Name *string `json:"name"`
	} `json:"tags"`
	Images *[]struct {
		Original *string `json:"original"`
	} `json:"images"`
	Variations *[]struct {
		Name       *string `json:"name"`
		Price      *int64  `json:"price"`
		EmptyStock *bool   `json:"is_empty_stock"`
		Type       *string `json:"type"`
	} `json:"variations"`
	Shop *struct {
		UUID      *string `json:"uuid"`
		Name      *string `json:"name"`
		Subdomain *string `json:"subdomain"`
		Thumbnail *string `json:"thumbnail_url"`
	} `json:"shop"`
}

// 页面里的原图地址：img 标签的 data-origin 给全尺寸版本；按 UUID 主干与 JSON 对应，不按位置。
var boothOriginPattern = regexp.MustCompile(`data-origin="([^"]+)"`)
var boothResizedSuffix = regexp.MustCompile(`_base_resized$`)

// boothImageKey 取图床路径去掉扩展名和 _base_resized 的部分，JSON 的缩略版与页面原图据此对应。
func boothImageKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := u.Path
	if ext := strings.LastIndex(p, "."); ext > strings.LastIndex(p, "/") {
		p = p[:ext]
	}
	return boothResizedSuffix.ReplaceAllString(p, "")
}

// boothPximgURL 只认 booth.pximg.net 的 https 地址；页面原图和 JSON 退回的地址都要过这一道。
func boothPximgURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "booth.pximg.net" && u.User == nil
}

// boothOrigins 取一次商品页 HTML，只保留 booth.pximg.net 的 https 原图；失败返回 nil，调用方降级为缩略版。
func (f *Fetcher) boothOrigins(ctx context.Context, page string) map[string]string {
	b, status, h, err := f.request(ctx, page, boothPageSource)
	if err != nil || httpFailure(status, h, boothPageSource) != nil {
		return nil
	}
	out := map[string]string{}
	for _, m := range boothOriginPattern.FindAllSubmatch(b, -1) {
		raw := html.UnescapeString(string(m[1]))
		if !boothPximgURL(raw) {
			continue
		}
		if key := boothImageKey(raw); key != "" {
			out[key] = raw
		}
	}
	return out
}

func (f *Fetcher) fetchBooth(ctx context.Context, l Link) (Result, error) {
	canonical, err := url.Parse(l.Clean)
	if err != nil {
		return Result{}, failure("invalid_target", boothSource)
	}
	lang := strings.Split(strings.TrimPrefix(canonical.Path, "/"), "/")[0]
	page := "https://booth.pm/" + lang + "/items/" + l.ID

	b, status, h, err := f.request(ctx, page+".json", boothSource)
	if err != nil {
		return Result{}, err
	}
	// 年龄确认是站点自己的页面；这里不带 adult Cookie，只把重定向到确认页的情况报成明确状态。
	if status == http.StatusFound || status == http.StatusSeeOther || status == http.StatusMovedPermanently || status == http.StatusTemporaryRedirect || status == http.StatusPermanentRedirect {
		loc := strings.ToLower(h.Get("Location"))
		if strings.Contains(loc, "adult") || strings.Contains(loc, "age_confirm") || strings.Contains(loc, "/age") {
			return Result{}, failure("age_restricted", boothSource)
		}
	}
	if err := httpFailure(status, h, boothSource); err != nil {
		return Result{}, err
	}
	var item boothItem
	if json.Unmarshal(b, &item) != nil {
		return Result{}, failure("invalid_response", boothSource)
	}
	if item.Adult != nil && *item.Adult {
		return Result{}, failure("age_restricted", boothSource)
	}
	if item.ID == "" {
		return Result{}, failure("invalid_response", boothSource)
	}
	if string(item.ID) != l.ID {
		return Result{}, failure("identity_mismatch", boothSource)
	}
	if item.Name == nil || strings.TrimSpace(*item.Name) == "" {
		return Result{}, failure("invalid_response", boothSource)
	}
	published, err := timestampField(item.Published)
	if err != nil {
		// timestampField 的来源是通用的 date；这里改报 BOOTH 自己的商品接口，便于排障。
		return Result{}, failure("invalid_response", boothSource)
	}

	r := newResult(l, boothSource)
	r.Title = available(*item.Name)
	r.Text = previewField(item.Description)
	appendBody(&r)
	r.Published = published
	r.Booth = &BoothInfo{Price: stringField(item.Price)}
	if item.Variations != nil {
		for _, v := range *item.Variations {
			bv := BoothVariation{Name: stringField(v.Name), Price: int64Field(v.Price)}
			if v.EmptyStock != nil {
				bv.SoldOut = *v.EmptyStock
			}
			if v.Type != nil {
				bv.Type = *v.Type
			}
			r.Booth.Variations = append(r.Booth.Variations, bv)
		}
	}
	r.Author = Author{Name: missing[string]("interface_omitted"), Account: missing[string]("interface_omitted"), ID: missing[string]("interface_omitted"), Avatar: missing[string]("interface_omitted")}
	if shop := item.Shop; shop != nil {
		r.Author.Name = stringField(shop.Name)
		r.Author.Account = stringField(shop.Subdomain)
		r.Author.Avatar = stringField(shop.Thumbnail)
		// 店铺没有数字 ID，只有 uuid；显示名不能当稳定 ID。
		if shop.UUID != nil && *shop.UUID != "" {
			r.Author.ID = available(*shop.UUID)
		}
	}
	if item.Category != nil {
		r.Category = stringField(item.Category.Name)
	}
	if item.Tags != nil {
		tags := []string{}
		for _, t := range *item.Tags {
			if t.Name != nil && *t.Name != "" {
				tags = append(tags, *t.Name)
			}
		}
		r.Tags = available(tags)
	}
	stat(&r, "wish_lists", item.Wishes)
	flag(&r, "sold_out", item.SoldOut)
	flag(&r, "end_of_sale", item.EndOfSale)

	if item.Images != nil {
		r.Media = available([]Media{})
		var origins map[string]string
		if len(*item.Images) > 0 {
			origins = f.boothOrigins(ctx, page)
		}
		allOriginal, count := true, 0
		for _, img := range *item.Images {
			if img.Original == nil || *img.Original == "" {
				continue
			}
			src := *img.Original
			full, ok := origins[boothImageKey(src)]
			if !ok {
				// JSON 给的缩略版地址也要过图床检查；不是 BOOTH 图床的地址不进结果。
				if !boothPximgURL(src) {
					continue
				}
				full = src
				allOriginal = false
			}
			m := imageMetadata(available(full))
			m.Referer = "https://booth.pm/"
			appendMedia(&r, m)
			count++
		}
		if count > 0 {
			r.Flags["original_images"] = available[jsonValue](allOriginal)
		}
	}
	return r, nil
}
