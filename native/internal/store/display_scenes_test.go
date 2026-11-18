package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestDisplayFourSceneOwnership(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	local, _ := set.Platform("local")
	qq, _ := set.Platform("qq")
	ctx := context.Background()
	if _, err := local.Events.Exec(`INSERT INTO local_topics(id,character_id,created_at) VALUES ('fork','a',1),('foreign','b',1)`); err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		st         *Store
		conv, text string
		obs        []string
	}
	for _, f := range []fixture{
		{local, "console:a:topic:fork", "fork without observation", nil},
		{qq, "qq:private:90001:90003", "first account", []string{"a"}},
		{qq, "qq:private:90002:90003", "second account", []string{"b"}},
		{qq, "qq:group:90004", "shared", []string{"a", "b"}},
		{qq, "qq:group:90004", "b only", []string{"b"}},
		{qq, "qq:group:90004", "unobserved", nil},
	} {
		channel := "qq"
		if f.st == local {
			channel = "console"
		}
		if _, err := f.st.AppendEvent(NewEvent{Channel: channel, Conversation: f.conv, ActorKind: "human", ActorID: "90003", Kind: "message", Text: f.text, CreatedAt: 1}, f.obs); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		platform, conv, char string
		want                 []string
	}{
		{"local", "console:a:topic:fork", "a", []string{"fork without observation"}},
		{"local", "console:a", "a", nil},
		{"qq", "qq:private:90001:90003", "a", []string{"first account"}},
		{"qq", "qq:private:90002:90003", "b", []string{"second account"}},
		{"qq", "qq:group:90004", "a", []string{"shared"}},
		{"qq", "qq:group:90004", "b", []string{"shared", "b only"}},
	} {
		st, _ := set.Platform(tc.platform)
		page, err := st.ReadDisplay(ctx, DisplayScope{Platform: tc.platform, Conversation: tc.conv, Characters: []string{"a", "b"}, Character: tc.char}, "", 200)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range page.Items {
			got = append(got, r.Text)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s filter=%s: got=%v want=%v", tc.conv, tc.char, got, tc.want)
		}
	}
	for _, conv := range []string{"console:a:topic:foreign", "console:a:topic:missing", "console:group:90004", "home:cabin"} {
		_, err := local.ReadDisplay(ctx, DisplayScope{Platform: "local", Conversation: conv, Characters: []string{"a", "b"}}, "", 200)
		if !errors.Is(err, ErrDisplayScope) {
			t.Fatalf("未收紧本地身份 %q: %v", conv, err)
		}
	}
}
