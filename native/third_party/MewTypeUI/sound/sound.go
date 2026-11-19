package sound

// PreparedSound is an immutable resource handle prepared and owned by the host.
// The library neither decodes it nor retains it after Dispatch returns.
type PreparedSound = any

type Request struct {
	EventID, ScopeID, CueID string
	Resource                PreparedSound
	Gain                    float32
}
type Policy struct {
	Enabled, Muted, Silent, Eligible, OSOwnsSound bool
	// Nil means the host's global default, 100%; an explicit zero is silent.
	// Resolve thread/category inheritance before calling Dispatch.
	VolumePercent *float32
}
type Options struct {
	Preview bool
	OnPlay  func(Request)
}

// Dispatch is an explicit event entry point, never called by a control build or
// paint. Event deduplication and the notification 500ms scope window belong to
// the host, which also owns device errors, stop and mixing.
// paint/notification-sound.md#只给接口的合同我定的.
func Dispatch(request Request, policy Policy, o Options) bool {
	if !policy.Enabled {
		return false
	}
	if !o.Preview && (policy.Muted || policy.Silent || !policy.Eligible || policy.OSOwnsSound) {
		return false
	}
	gain := float32(1)
	if policy.VolumePercent != nil {
		gain = min(1, max(0, *policy.VolumePercent/100))
	}
	if gain == 0 || o.OnPlay == nil {
		return false
	}
	request.Gain = gain
	o.OnPlay(request)
	return true
}
