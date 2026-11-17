package dialogs_test

import "testing"

func check(t *testing.T, label string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", label)
	}
}
