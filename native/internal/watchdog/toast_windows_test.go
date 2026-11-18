package watchdog

import (
	"encoding/base64"
	"testing"
	"unicode/utf16"
)

// ───────────── 通知脚本 ─────────────

func TestEncodePowerShell(t *testing.T) {
	enc := encodePowerShell("Write-Host '中文'")
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("%v", err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	if got := string(utf16.Decode(u)); got != "Write-Host '中文'" {
		t.Fatalf("解出来 %q", got)
	}
}
