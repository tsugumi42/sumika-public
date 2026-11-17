package tools

import (
	"testing"

	"sumika-native/internal/jsjson"
)

func TestCallTargetFallsBackForOtherTools(t *testing.T) {
	for in, want := range map[string]string{
		`{"url":"https://example.test/a","other":"x"}`: "https://example.test/a",
		`{"zebra":"z","apple":"a"}`:                    "a",
		`{"path":"编造.txt"}`:                            "编造.txt",
		`{"n":1}`:                                      "",
	} {
		v, err := jsjson.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := CallTarget("mcp_编造", v); got != want {
			t.Errorf("%s：%q，想要 %q", in, got, want)
		}
	}
}
