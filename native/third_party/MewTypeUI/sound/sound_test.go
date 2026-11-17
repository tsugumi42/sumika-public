package sound

import "testing"

func check(t *testing.T, id string, ok bool) {
	t.Helper()
	if !ok {
		t.Errorf("assertion %s", id)
	}
}
func TestNotificationEligibilityAndPreview(t *testing.T) {
	resource := &struct{ samples [4]float32 }{samples: [4]float32{0, .5, 0, -.5}}
	calls := 0
	gain := float32(0)
	request := Request{EventID: "event-1", ScopeID: "scope", CueID: "message", Resource: resource}
	options := Options{OnPlay: func(r Request) { calls++; gain = r.Gain }}
	policy := Policy{Enabled: true, Eligible: true}
	// paint/notification-sound.md#音量与静音: resolved volume, not multiplication.
	sent := Dispatch(request, policy, options)
	check(t, "sound-default-gain", sent && calls == 1 && gain == 1)
	volume := float32(37)
	policy.VolumePercent = &volume
	Dispatch(request, policy, options)
	check(t, "sound-explicit-volume", calls == 2 && gain == .37)
	volume = 0
	sent = Dispatch(request, policy, options)
	check(t, "sound-zero-suppresses-request", !sent && calls == 2)
	volume = 75
	for _, reason := range []string{"global", "muted", "silent", "unknown", "os"} {
		p := policy
		switch reason {
		case "global":
			p.Enabled = false
		case "muted":
			p.Muted = true
		case "silent":
			p.Silent = true
		case "unknown":
			p.Eligible = false
		case "os":
			p.OSOwnsSound = true
		}
		before := calls
		sent = Dispatch(request, p, options)
		check(t, "sound-ineligible-suppresses", !sent && calls == before)
	}
	policy.Muted = true
	policy.Silent = true
	policy.Eligible = false
	policy.OSOwnsSound = true
	options.Preview = true
	sent = Dispatch(request, policy, options)
	check(t, "sound-preview-bypasses-session-policy", sent && calls == 3 && gain == .75)
	policy.Enabled = false
	sent = Dispatch(request, policy, options)
	check(t, "sound-preview-respects-global-disable", !sent && calls == 3)
	policy.Enabled = true
	volume = 0
	sent = Dispatch(request, policy, options)
	check(t, "sound-preview-respects-zero-volume", !sent && calls == 3)
}
