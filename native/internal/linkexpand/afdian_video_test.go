package linkexpand

import "testing"

// 爱发电的免费视频动态：取数端给了视频地址，默认模板要把视频发出来（原来模板没有 $视频，只发封面）。
func TestAfdianFreeVideoIsSent(t *testing.T) {
	r := siteResult("afdian", "post", "90032")
	r.Title, r.Text = av("编的视频动态"), av("编的说明")
	r = withMedia(r, goldenImage(1), goldenVideo())
	d := Build(&r, "-71", Options{}, 0)
	videos := 0
	for _, p := range d.Parts {
		for _, it := range p.Body {
			if it.Kind == "video" && it.Media.URL.Value == goldenVideo().URL.Value {
				videos++
			}
		}
	}
	if videos != 1 {
		t.Fatalf("视频应发一次，实际 %d：%+v", videos, d.Parts)
	}
}
