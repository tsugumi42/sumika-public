package linkreader

import (
	"encoding/json"
	"testing"
)

func TestResultRoundTrip(t *testing.T) {
	r := newResult(Link{Platform: "x", Kind: "post", ID: "1900000000000000013", Clean: "https://x.com/AP/status/1900000000000000013"}, "fx_v2")
	r.Stats["likes"] = available(int64(0))
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var got Result
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "1900000000000000013" || got.Stats["likes"].State != "available" || got.Stats["likes"].Value != 0 || got.Author.ID.State != "not_returned" || got.Author.ID.Reason != "interface_omitted" || !got.FetchedAt.Equal(r.FetchedAt) {
		t.Fatalf("lost cache contract: %+v", got)
	}
}
