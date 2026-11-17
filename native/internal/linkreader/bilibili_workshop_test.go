package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const workshopURL = "https://mall.bilibili.com/neul-next/index.html?page=mall-up_itemDetail&itemsId=800001&track_id=__BGMT__"

// 数据都是编的：店名、商品名、编号不对应任何真实小店。
const workshopBody = `{"success":true,"data":{"shopInfo":{"shopId":1,"shopUserNickName":"纸舟的小店","shopUserMid":770001,"shopUserFace":"https://i0.hdslb.com/bfs/face/fake.jpg"},
"itemsId":800001,"name":"折纸教程合集","status":1,"saleStatus":11,"price":100,"itemsDiscountPriceVO":{"discountType":1,"discountPrice":10},
"mainImgList":["//i0.hdslb.com/bfs/mallup/fake1.png","//i0.hdslb.com/bfs/mallup/fake2.png"],"description":"这是编的商品介绍。\n第二行。","saleNum":"已售2万+",
"itemsTag":{"customTagList":["折纸","教程"],"deliveryTagList":["线上交付","支付后自动发货"]}}}`

func TestWorkshopFetch(t *testing.T) {
	var referer string
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/mall-up-search/items/info" {
			t.Fatalf("不该请求：%s", req.URL)
		}
		referer = req.Header.Get("Referer")
		if req.URL.Query().Get("itemsId") != "800001" || req.URL.Query().Get("itemsPreviewId") != "0" {
			t.Fatalf("参数不对：%s", req.URL.RawQuery)
		}
		return response(200, workshopBody), nil
	})
	l := link(t, workshopURL)
	if l.Kind != "workshop" {
		t.Fatalf("%#v", l)
	}
	r, err := f.Fetch(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if referer != "https://gf.bilibili.com/" {
		t.Fatalf("Referer = %q", referer)
	}
	if r.Kind != "workshop" || r.Title.Value != "折纸教程合集" || r.Author.Name.Value != "纸舟的小店" || r.Author.ID.Value != "770001" {
		t.Fatalf("%+v", r)
	}
	w := r.Workshop
	if w == nil || w.Price.Value != "0.10" || w.OriginalPrice.Value != "1.00" || w.Sold.Value != "2万+" || w.Status.Value != "在售" || w.Delivery.Value != "线上交付、支付后自动发货" {
		t.Fatalf("%+v", w)
	}
	if len(r.Media.Value) != 1 || r.Media.Value[0].URL.Value != "https://i0.hdslb.com/bfs/mallup/fake1.png" {
		t.Fatalf("封面：%+v", r.Media)
	}
	if len(r.Tags.Value) != 2 || !strings.Contains(r.Text.Value, "第二行") {
		t.Fatalf("%+v %+v", r.Tags, r.Text)
	}
}

// 没有优惠时只有一个价格；价格、状态取不到时标明，不写成 0 元。
func TestWorkshopPriceAndStatusMissing(t *testing.T) {
	body := `{"success":true,"data":{"itemsId":800002,"name":"无价格商品","status":2,"saleStatus":12,"price":2599}}`
	f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
	r, err := f.Fetch(context.Background(), link(t, "https://gf.bilibili.com/item/detail/800002"))
	if err != nil {
		t.Fatal(err)
	}
	w := r.Workshop
	if w.Price.Value != "25.99" || w.OriginalPrice.State != "not_returned" || w.Status.State != "not_returned" || w.Sold.State != "not_returned" {
		t.Fatalf("%+v", w)
	}
	if r.Author.Name.State != "not_returned" {
		t.Fatalf("店铺名不该有：%+v", r.Author)
	}
}

func TestWorkshopFallsBackToMall(t *testing.T) {
	var paths []string
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		if req.URL.Path == "/mall-up-search/items/info" {
			return response(200, `{"message":"查询的作品不存在","code":81102094}`), nil
		}
		return response(200, `{"success":true,"code":0,"data":{"itemsId":800003,"name":"会员购的商品","price":"99.00","maxPrice":"99.00","itemsStatus":1,"saleStatus":11,"itemsType":"0"}}`), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://show.bilibili.com/platform/malldetail.html?itemsId=800003"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "mall" || r.Mall == nil || r.Title.Value != "会员购的商品" || len(paths) != 2 {
		t.Fatalf("%+v %v", r, paths)
	}
}

func TestWorkshopNotFoundEverywhere(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/mall-up-search/items/info" {
			return response(200, `{"message":"查询的作品不存在","code":81102094}`), nil
		}
		return response(200, `{"success":false,"data":null,"code":81100001,"message":"商品不存在或被删除"}`), nil
	})
	_, err := f.Fetch(context.Background(), link(t, workshopURL))
	wantError(t, err, "not_found")
}

// 会员购那边是风控时如实报风控，不说成商品不存在。
func TestWorkshopMallRiskControlNotHidden(t *testing.T) {
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/mall-up-search/items/info" {
			return response(200, `{"message":"查询的作品不存在","code":81102094}`), nil
		}
		return response(200, `{"success":false,"code":-352}`), nil
	})
	_, err := f.Fetch(context.Background(), link(t, workshopURL))
	wantError(t, err, "risk_control")
}

func TestWorkshopBadResponses(t *testing.T) {
	for name, body := range map[string]string{
		"空商品":    `{"success":true,"data":null}`,
		"没有名字":   `{"success":true,"data":{"itemsId":800001}}`,
		"编号对不上":  `{"success":true,"data":{"itemsId":800009,"name":"别的商品"}}`,
		"失败没有码":  `{"success":false}`,
		"不是JSON": `<html>`,
	} {
		t.Run(name, func(t *testing.T) {
			f := testFetcher(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
			_, err := f.Fetch(context.Background(), link(t, workshopURL))
			if err == nil {
				t.Fatal("应当失败")
			}
		})
	}
}
