package linkreader

import (
	"context"
	"strings"
	"testing"
)

// BOOTH 的规格（variations）取进 Booth.Variations：名字、价格、没库存、类型（用户 2026-11-12：文字版列出价格和规格）。
// 名字是编的；接口不给 variations 时是 nil。
func TestBoothVariations(t *testing.T) {
	body := boothFile(t, "booth-item-book.json")
	withVar := strings.Replace(body, "\n \"shop\":", `
 "variations": [
  {"id": 1, "name": "纸船电子版", "price": 600, "is_empty_stock": false, "type": "digital"},
  {"id": 2, "name": "纸船实体版", "price": 1200, "is_empty_stock": true, "type": "via_warehouse"},
  {"id": 3, "name": null, "price": null}
 ],
 "shop":`, 1)
	if withVar == body {
		t.Fatal("夹具里找不到 shop 字段，测试没把规格塞进去")
	}
	var calls []boothCall
	f := boothFetcher(t, &calls, 200, withVar, nil, 500, "")
	r, err := f.Fetch(context.Background(), link(t, boothBookURL))
	if err != nil {
		t.Fatal(err)
	}
	v := r.Booth.Variations
	if len(v) != 3 {
		t.Fatalf("规格：%+v", v)
	}
	if v[0].Name.Value != "纸船电子版" || v[0].Price.Value != 600 || v[0].SoldOut || v[0].Type != "digital" {
		t.Fatalf("第一个规格：%+v", v[0])
	}
	if !v[1].SoldOut || v[1].Price.Value != 1200 {
		t.Fatalf("第二个规格：%+v", v[1])
	}
	if v[2].Name.State == "available" || v[2].Price.State == "available" {
		t.Fatalf("缺的名字和价格应当是没取到：%+v", v[2])
	}

	calls = nil
	f = boothFetcher(t, &calls, 200, body, nil, 500, "")
	if r, err = f.Fetch(context.Background(), link(t, boothBookURL)); err != nil || r.Booth.Variations != nil {
		t.Fatalf("接口没给规格时应当是 nil：%v %+v", err, r.Booth)
	}
}
