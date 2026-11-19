package main

import (
	"reflect"
	"testing"

	"sumika-native/internal/config"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/linktemplate"
)

// 默认样式必须和线上默认配置真发时的排版选项一模一样；别的样式只改模板和转发两项。
func TestStyleOptionsFollowLiveDefaults(t *testing.T) {
	expand := config.DefaultLinks().Expand
	live := linkplugin.BuildOptions(expand)
	if !live.ForwardVideo || live.KeepTrailingHashtags {
		t.Fatalf("默认配置应是视频进转发、去结尾 hashtag：%+v", live)
	}
	for _, key := range []string{"x", "bilibili-video", "pixiv-artwork"} {
		site, _, ok := linktemplate.SiteByKey(key)
		if !ok {
			t.Fatalf("没有站点 %s", key)
		}
		for _, st := range site.Styles() {
			got := styleOptions(expand, site, st)
			want := linkplugin.BuildOptions(expand)
			if st.ID == linktemplate.StyleDefault {
				if st.Direct != "" {
					t.Errorf("%s 默认样式不该改模板", key)
				}
				if st.Forward == "" || st.Forward == linktemplate.ForwardAuto {
					if !reflect.DeepEqual(got.Templates, live.Templates) {
						t.Errorf("%s 默认样式的模板表要和线上一样", key)
					}
				}
			}
			if st.Direct != "" {
				want.Templates[site.Key] = st.Direct
			}
			if st.Forward != "" {
				set := want.Sites[site.Key]
				set.Forward = st.Forward
				want.Sites[site.Key] = set
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s/%s 的选项和线上不一致：\n得到 %+v\n要   %+v", key, st.ID, got, want)
			}
		}
	}
}
