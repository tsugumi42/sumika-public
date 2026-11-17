package gallery

import (
	"bytes"
	"image"
	"math"
	"testing"
)

func sameInk(t *testing.T, name string, a, b *image.RGBA) {
	t.Helper()
	if !bytes.Equal(a.Pix, b.Pix) {
		t.Errorf("%s: foreground pixels changed", name)
	}
}
func centerNear(t *testing.T, name string, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Errorf("%s: got %.4f want %.4f, tolerance %.4f physical px", name, got, want, tolerance)
	}
}
