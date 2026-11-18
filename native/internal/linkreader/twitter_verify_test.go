package linkreader

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// fx 的 author.verification 取进 Author.Verification；没给、给了不认识的种类都是“不知道”，不当成没认证。
func TestXVerificationParsed(t *testing.T) {
	for _, c := range []struct {
		name, json, state, want string
	}{
		{"蓝标", `{"verified":true,"verified_at":null,"type":"individual"}`, "available", VerifyBlue},
		{"金标", `{"verified":true,"type":"business"}`, "available", VerifyBusiness},
		{"机构金标", `{"verified":true,"verified_at":null,"type":"organization"}`, "available", VerifyBusiness},
		{"灰标", `{"verified":true,"type":"government"}`, "available", VerifyGovernment},
		{"没认证", `{"verified":false}`, "available", VerifyNone},
		{"没给", `null`, "not_returned", ""},
		{"缺种类", `{"verified":true}`, "not_returned", ""},
		{"不认识的种类", `{"verified":true,"type":"martian"}`, "not_returned", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := `{"code":200,"status":{"id":"90001","text":"编的推文","media":{},"author":{"name":"纸云","screen_name":"paper_cloud","verification":` + c.json + `}}}`
			f := testFetcher(t, fxOnly(func(*http.Request) (*http.Response, error) { return response(200, body), nil }))
			r, err := f.Fetch(context.Background(), link(t, "https://x.com/paper_cloud/status/90001"))
			if err != nil {
				t.Fatal(err)
			}
			if v := r.Author.Verification; v.State != c.state || v.Value != c.want {
				t.Fatalf("认证：%+v", v)
			}
		})
	}
	// vx 不给认证信息：是“不知道”
	f := testFetcher(t, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "vxtwitter") {
			return response(200, fixture(t, "vx-video")), nil
		}
		return response(503, ""), nil
	})
	r, err := f.Fetch(context.Background(), link(t, "https://x.com/AP/status/1900000000000000013"))
	if err != nil || r.Author.Verification.State == "available" {
		t.Fatalf("vx 不该有认证：%+v %v", r.Author.Verification, err)
	}
}
