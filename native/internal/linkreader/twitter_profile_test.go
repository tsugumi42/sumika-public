package linkreader

import (
	"context"
	"net/http"
	"slices"
	"testing"
)

// 作者资料和数字 ID（规格-画师原图 第四、七节）：只有 fx_v2 给资料；fx_v2、嵌入接口给数字 ID，vx 不给。

func TestFXAuthorProfile(t *testing.T) {
	f := testFetcher(t, fxOnly(func(r *http.Request) (*http.Response, error) {
		return response(200, fixture(t, "x-four")), nil
	}))
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/1900000000000000011"))
	if err != nil {
		t.Fatal(err)
	}
	p := r.Author.Profile
	if r.Source != "fx_v2" || r.Author.ID.State != "available" || r.Author.ID.Value == "" {
		t.Fatalf("fx_v2 应该给数字 ID：%+v", r.Author)
	}
	if p == nil || p.Bio == "" || p.Location != "编造城市" || p.Followers != 771918 || p.MediaCount != 12134 || p.Statuses != 27817 {
		t.Fatalf("资料不对：%+v", p)
	}
	// 简介里的链接用 fx 换好的真实地址，不是 t.co；主页地址也收进来
	if !slices.Contains(p.Links, "https://example.com/fixture/40") || !slices.Contains(p.Links, "https://example.com/fixture/34") {
		t.Fatalf("链接：%v", p.Links)
	}
}

// 作者对象很瘦（只有 ID、名字、账号、头像）时没有资料：Profile 是 nil，调用方不去判断。
func TestFXSlimAuthorHasNoProfile(t *testing.T) {
	const id = "1900000000000000002"
	f := testFetcher(t, fxOnly(func(r *http.Request) (*http.Response, error) {
		return response(200, fixture(t, "x-public-"+id)), nil
	}))
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/i/status/"+id))
	if err != nil {
		t.Fatal(err)
	}
	if r.Author.Profile != nil || r.Author.ID.State != "available" {
		t.Fatalf("瘦作者对象：%+v", r.Author)
	}
}

// vx 不给数字 ID，也没有资料：这一次认不出画师，也不判断（发送行为和今天一样）。
func TestVXHasNeitherIDNorProfile(t *testing.T) {
	f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.fxtwitter.com" {
			return response(503, ""), nil
		}
		return response(200, fixture(t, "vx-photo")), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/1900000000000000008"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "vx" || r.Author.ID.State == "available" || r.Author.Profile != nil {
		t.Fatalf("%s：%+v", r.Source, r.Author)
	}
}

// 嵌入接口给数字 ID（user.id_str），没有资料。
func TestSyndicationGivesIDButNoProfile(t *testing.T) {
	f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "cdn.syndication.twimg.com" {
			return response(200, syndBody), nil
		}
		return response(503, ""), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != RouteSyndication || r.Author.ID.State != "available" || r.Author.ID.Value != "7" || r.Author.Profile != nil {
		t.Fatalf("%s：%+v", r.Source, r.Author)
	}
}

// 资料缺字段：只给了简介没给数字，数字记 -1（比例线索不算）。
func TestProfileMissingNumbersAreMinusOne(t *testing.T) {
	a := xAuthor{Description: new(string)}
	p := a.profile()
	if p == nil || p.Followers != -1 || p.MediaCount != -1 || p.Statuses != -1 {
		t.Fatalf("%+v", p)
	}
	if (xAuthor{}).profile() != nil {
		t.Fatal("没有简介字段就没有资料")
	}
}
