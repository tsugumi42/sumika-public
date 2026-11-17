package linkreader

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// B 站小店（工房）商品：mall.bilibili.com/mall-up-search/items/info，匿名可取，要带 gf.bilibili.com 的 Referer；
// 价格单位是分。商品编号和会员购不是一套：不存在时回 code 81102094（没有 success 字段）。
// show.bilibili.com/platform/malldetail.html 两种商品共用，所以工房查不到时再查一次会员购（结果的页面类型随之变成 mall）。
const workshopSource = "bili_workshop"

// WorkshopInfo 是小店商品独有的字段；店铺名、头像、编号放在 Result.Author。
type WorkshopInfo struct {
	Price         Field[string] `json:"price_yuan"`          // 现价；有优惠时是优惠价
	OriginalPrice Field[string] `json:"original_price_yuan"` // 有优惠时的原价
	Sold          Field[string] `json:"sold"`                // 接口给的销量说法，如“2万+”
	Status        Field[string] `json:"status"`
	Delivery      Field[string] `json:"delivery"` // 交付方式标签，如“线上交付”
}

type workshopItem struct {
	ID         decimalID `json:"itemsId"`
	Name       *string   `json:"name"`
	Status     *int      `json:"status"`
	SaleStatus *int      `json:"saleStatus"`
	Price      *int64    `json:"price"`
	Discount   *struct {
		Price *int64 `json:"discountPrice"`
	} `json:"itemsDiscountPriceVO"`
	Images *[]string `json:"mainImgList"`
	Desc   *string   `json:"description"`
	Sold   *string   `json:"saleNum"`
	Shop   *struct {
		Name *string   `json:"shopUserNickName"`
		Mid  decimalID `json:"shopUserMid"`
		Face *string   `json:"shopUserFace"`
	} `json:"shopInfo"`
	Tags *struct {
		Custom   []string `json:"customTagList"`
		Delivery []string `json:"deliveryTagList"`
	} `json:"itemsTag"`
}

// yuan 把分换成元，保留两位。
func yuan(fen int64) string { return fmt.Sprintf("%d.%02d", fen/100, fen%100) }

func (f *Fetcher) fetchWorkshop(ctx context.Context, l Link) (Result, error) {
	r, err := f.fetchWorkshopItem(ctx, l)
	var fe *FetchError
	if err == nil || !errors.As(err, &fe) || fe.Kind != "not_found" {
		return r, err
	}
	ml := Link{Platform: "bilibili", Kind: "mall", ID: l.ID, Clean: "https://mall.bilibili.com" + mallPath + "?itemsId=" + l.ID, Raw: l.Raw}
	mr, merr := f.fetchMall(ctx, ml)
	if merr == nil {
		return mr, nil
	}
	var mfe *FetchError
	if errors.As(merr, &mfe) && mfe.Kind != "not_found" {
		return Result{}, merr // 会员购那边是风控、限流之类，如实报，不说成“商品不存在”
	}
	return Result{}, err
}

func (f *Fetcher) fetchWorkshopItem(ctx context.Context, l Link) (Result, error) {
	var e struct {
		Success *bool         `json:"success"`
		Code    *int          `json:"code"`
		Data    *workshopItem `json:"data"`
	}
	ctx = withHeader(ctx, "Referer", "https://gf.bilibili.com/")
	if err := f.getJSON(ctx, "https://mall.bilibili.com/mall-up-search/items/info?itemsPreviewId=0&itemsId="+url.QueryEscape(l.ID), workshopSource, &e); err != nil {
		return Result{}, err
	}
	if e.Code != nil && *e.Code != 0 {
		return Result{}, biliFailure(*e.Code, workshopSource)
	}
	v := e.Data
	if e.Success == nil || !*e.Success || v == nil || v.Name == nil || strings.TrimSpace(*v.Name) == "" {
		return Result{}, failure("invalid_response", workshopSource)
	}
	if string(v.ID) != l.ID {
		return Result{}, failure("identity_mismatch", workshopSource)
	}
	r := newResult(l, workshopSource)
	r.Title = stringField(v.Name)
	r.Text = stringField(v.Desc)
	w := &WorkshopInfo{Price: missing[string]("interface_omitted"), OriginalPrice: missing[string]("not_applicable"), Sold: missing[string]("interface_omitted"), Status: Field[string]{State: "not_returned", Value: "状态未知", Reason: "unknown_sale_state"}, Delivery: missing[string]("interface_omitted")}
	if v.Price != nil && *v.Price >= 0 {
		w.Price = available(yuan(*v.Price))
		if v.Discount != nil && v.Discount.Price != nil && *v.Discount.Price >= 0 && *v.Discount.Price < *v.Price {
			w.Price = available(yuan(*v.Discount.Price))
			w.OriginalPrice = available(yuan(*v.Price))
		}
	}
	if v.Sold != nil {
		if s := strings.TrimSpace(strings.TrimPrefix(*v.Sold, "已售")); s != "" {
			w.Sold = available(s)
		}
	}
	// 只认实测过的组合（status 1、saleStatus 11 是在售）；别的取值不猜含义。
	if v.Status != nil && *v.Status == 1 && v.SaleStatus != nil && *v.SaleStatus == 11 {
		w.Status = available("在售")
	}
	if v.Tags != nil {
		if len(v.Tags.Delivery) > 0 {
			w.Delivery = available(strings.Join(v.Tags.Delivery, "、"))
		}
		if len(v.Tags.Custom) > 0 {
			r.Tags = available(v.Tags.Custom)
		}
	}
	r.Workshop = w
	if v.Shop != nil {
		r.Author.Name = stringField(v.Shop.Name)
		if v.Shop.Mid != "" {
			r.Author.ID = available(string(v.Shop.Mid))
		}
		if v.Shop.Face != nil && *v.Shop.Face != "" {
			r.Author.Avatar = available(httpsURL(*v.Shop.Face))
		}
	}
	appendBody(&r)
	if v.Images != nil {
		r.Media = available([]Media{})
		if len(*v.Images) > 0 && (*v.Images)[0] != "" {
			appendMedia(&r, imageMetadata(available(httpsURL((*v.Images)[0]))))
		}
	}
	return r, nil
}
