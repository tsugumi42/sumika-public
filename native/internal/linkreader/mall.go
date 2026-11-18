package linkreader

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const mallSource = "bili_mall"
const mallPath = "/neul-next/detailuniversal/detail.html"

var mallZone = time.FixedZone("+08:00", 8*60*60)

// 普通商品与市集、魔力赏、票务的编号语义不同，必须先认路由再读 itemsId。
func recognizeMall(u *url.URL, raw string) (Link, bool) {
	u.RawQuery = strings.ReplaceAll(u.RawQuery, "&amp;", "&")
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Link{}, false
	}
	page := q["page"]
	if len(page) == 1 && page[0] == "mall-up_itemDetail" && (u.EscapedPath() == "/neul-next/index.html" || u.EscapedPath() == "/neul/index.html") {
		// 小店（工房）商品：同一个入口，换了页面路由；编号语义和会员购不同，单独成一种页面。
		ids := q["itemsId"]
		if len(ids) != 1 || len(ids[0]) > 20 || !positiveID(ids[0]) {
			return Link{}, false
		}
		l, _, _ := biliWorkshop(ids[0], raw)
		return l, true
	}
	if len(page) > 1 || (len(page) == 1 && page[0] != "detailuniversal_detail") {
		return Link{}, false
	}
	switch u.EscapedPath() {
	case mallPath, "/detail.html":
	case "/neul/index.html", "/neul-next/index.html":
		if len(page) != 1 {
			return Link{}, false
		}
	default:
		return Link{}, false
	}
	ids := q["itemsId"]
	if len(ids) != 1 || len(ids[0]) > 128 || !positiveID(ids[0]) {
		return Link{}, false
	}
	clean := "https://mall.bilibili.com" + mallPath + "?itemsId=" + ids[0]
	return Link{Platform: "bilibili", Kind: "mall", ID: ids[0], Clean: clean, Raw: raw}, true
}

// MallInfo 只保存商品展示字段；账号、下单参数和接口原文不进入结果。
type MallInfo struct {
	Price        Field[string]    `json:"price_yuan"`
	Deposit      Field[string]    `json:"deposit_yuan"`
	Status       Field[string]    `json:"status"`
	Shop         Field[string]    `json:"shop"`
	PresaleStart Field[time.Time] `json:"presale_start"`
	PresaleEnd   Field[time.Time] `json:"presale_end"`
	OnSale       Field[time.Time] `json:"on_sale"`
	Delivery     Field[string]    `json:"delivery_month"`
}

type mallItem struct {
	ID          decimalID  `json:"itemsId"`
	Name        string     `json:"name"`
	Price       *string    `json:"price"`
	MaxPrice    *string    `json:"maxPrice"`
	ItemsType   *decimalID `json:"itemsType"`
	ItemsStatus *int       `json:"itemsStatus"`
	SaleStatus  *int       `json:"saleStatus"`
	Images      *[]string  `json:"img"`
	ShopInfo    *struct {
		Name *string `json:"shopName"`
	} `json:"shopInfoVO"`
	Shop *struct {
		Name *string `json:"shopName"`
	} `json:"shopVO"`
	Advance *struct {
		Start      *int64  `json:"presaleStartOrderTime"`
		End        *int64  `json:"presaleEndOrderTime"`
		Deposit    *string `json:"deposit"`
		MaxDeposit *string `json:"maxDeposit"`
	} `json:"advState"`
	OnSale   *int64  `json:"autoOnSaleTime"`
	Delivery *string `json:"presaleDeliveryTimeStr"`
	SKUs     *struct {
		Items []struct {
			Stock *int64 `json:"stock"`
			Level *int   `json:"stockLevel"`
		} `json:"itemsSkuList"`
	} `json:"itemsSkuListVO"`
}

var mallMoney = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

func mallPrice(minimum, maximum *string) (Field[string], error) {
	for _, p := range []*string{minimum, maximum} {
		if p != nil && (len(*p) > 128 || !mallMoney.MatchString(*p)) {
			return Field[string]{}, failure("invalid_response", mallSource)
		}
	}
	if minimum == nil {
		return missing[string]("interface_omitted"), nil
	}
	value := *minimum
	if maximum == nil {
		return available(value + "（最高价未返回）"), nil
	}
	if *maximum != value {
		value += "～" + *maximum
	}
	return available(value), nil
}

func mallTime(p *int64) Field[time.Time] {
	if p == nil || *p <= 0 {
		return missing[time.Time]("no_valid_time")
	}
	return unixField(p)
}

func mallStatus(v *mallItem) Field[string] {
	unknown := Field[string]{State: "not_returned", Value: "状态未知", Reason: "unknown_sale_state"}
	if v.ItemsStatus == nil || *v.ItemsStatus != 1 || v.SaleStatus == nil || *v.SaleStatus != 11 || v.ItemsType == nil || (*v.ItemsType != "0" && *v.ItemsType != "1") {
		return unknown
	}
	if v.SKUs != nil && len(v.SKUs.Items) > 0 {
		allSold, hasStock := true, false
		for _, sku := range v.SKUs.Items {
			allSold = allSold && sku.Level != nil && *sku.Level == 4
			hasStock = hasStock || (sku.Level != nil && (*sku.Level == 1 || *sku.Level == 2) && sku.Stock != nil && *sku.Stock > 0)
		}
		if allSold {
			return available("售罄")
		}
		if *v.ItemsType == "0" && hasStock {
			return available("在售")
		}
	}
	if *v.ItemsType == "1" {
		return available("预售")
	}
	return unknown
}

// MallNow 是判断“今年”用的当前时间；别的包的测试也会钉住它（linkexpand 的基线），所以导出。
var MallNow = time.Now

// mallStamp 是会员购时间的写法，同链接文字版各平台统一的写法（用户 2026-11-12）：今年的“10-22 19:55”，往年的带年份，不写时区。
func mallStamp(t time.Time) string {
	t = t.In(mallZone)
	if t.Year() == MallNow().In(mallZone).Year() {
		return t.Format("01-02 15:04")
	}
	return t.Format("2006-01-02 15:04")
}

// Details 同时供有序文字块和卡片消费，避免两个出口给同一价格、时间不同解释。
func (m *MallInfo) Details() []string {
	value := func(f Field[string]) string {
		if f.State != "available" || f.Value == "" {
			return "未返回"
		}
		return f.Value
	}
	status := "状态未知"
	if m.Status.State == "available" {
		status = m.Status.Value
	}
	price := "未返回"
	if m.Price.State == "available" {
		price = "¥" + m.Price.Value
	}
	lines := []string{"价格：" + price, "状态：" + status, "店铺：" + value(m.Shop)}
	if m.Deposit.State == "available" {
		lines = append(lines, "定金：¥"+m.Deposit.Value)
	}
	for _, item := range []struct {
		name  string
		field Field[time.Time]
	}{{"预售开始", m.PresaleStart}, {"预售结束", m.PresaleEnd}, {"开售", m.OnSale}} {
		if item.field.State == "available" {
			lines = append(lines, item.name+"："+mallStamp(item.field.Value))
		}
	}
	if m.Delivery.State == "available" && m.Delivery.Value != "" {
		lines = append(lines, "预计到货补款："+m.Delivery.Value)
	}
	return lines
}

func (f *Fetcher) fetchMall(ctx context.Context, l Link) (Result, error) {
	var e struct {
		Success *bool     `json:"success"`
		Code    *int      `json:"code"`
		Data    *mallItem `json:"data"`
	}
	if err := f.getJSON(ctx, "https://mall.bilibili.com/mall-search-items/items/merchant/info?itemsId="+l.ID, mallSource, &e); err != nil {
		return Result{}, err
	}
	if e.Code == nil || e.Success == nil {
		return Result{}, failure("invalid_response", mallSource)
	}
	if *e.Code != 0 {
		return Result{}, biliFailure(*e.Code, mallSource)
	}
	if !*e.Success || e.Data == nil || e.Data.ID == "" || strings.TrimSpace(e.Data.Name) == "" {
		return Result{}, failure("invalid_response", mallSource)
	}
	v := e.Data
	if string(v.ID) != l.ID {
		return Result{}, failure("identity_mismatch", mallSource)
	}
	price, err := mallPrice(v.Price, v.MaxPrice)
	if err != nil {
		return Result{}, err
	}
	m := &MallInfo{Price: price, Deposit: missing[string]("interface_omitted"), Status: mallStatus(v), Shop: missing[string]("interface_omitted"), PresaleStart: mallTime(nil), PresaleEnd: mallTime(nil), OnSale: mallTime(v.OnSale), Delivery: isoDateField(stringField(v.Delivery))}
	if v.ShopInfo != nil {
		m.Shop = stringField(v.ShopInfo.Name)
	}
	if (m.Shop.State != "available" || strings.TrimSpace(m.Shop.Value) == "") && v.Shop != nil {
		m.Shop = stringField(v.Shop.Name)
	}
	if v.Advance != nil {
		m.PresaleStart, m.PresaleEnd = mallTime(v.Advance.Start), mallTime(v.Advance.End)
		m.Deposit, err = mallPrice(v.Advance.Deposit, v.Advance.MaxDeposit)
		if err != nil {
			return Result{}, err
		}
	}
	r := newResult(l, mallSource)
	r.Mall, r.Title = m, available(v.Name)
	r.Text = available(strings.Join(m.Details(), "\n"))
	appendBody(&r)
	if v.Images != nil {
		r.Media = available([]Media{})
		if len(*v.Images) > 0 && (*v.Images)[0] != "" {
			image := (*v.Images)[0]
			if strings.HasPrefix(image, "//") {
				image = "https:" + image
			}
			appendMedia(&r, imageMetadata(available(image)))
		}
	}
	if v.ItemsType != nil {
		r.Flags["items_type"] = available[jsonValue](string(*v.ItemsType))
	}
	if v.ItemsStatus != nil {
		r.Flags["items_status"] = available[jsonValue](*v.ItemsStatus)
	}
	if v.SaleStatus != nil {
		r.Flags["sale_status"] = available[jsonValue](*v.SaleStatus)
	}
	return r, nil
}
