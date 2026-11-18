// Package dialogs provides host-owned conversation lists and their controls.
package dialogs

import "github.com/egoist/mygo/ui"

// Receipt describes an outgoing message. Incoming messages use NoReceipt.
type Receipt uint8

const (
	NoReceipt Receipt = iota
	Sending
	Sent
	Read
)

type PeerKind uint8

const (
	Person PeerKind = iota
	Group
	Channel
	Bot
	Secret
	Archive
)

type ActivityKind uint8

const (
	TypingActivity ActivityKind = iota
	RecordingActivity
	UploadingActivity
	ChoosingStickerActivity
)

// Stories are prepared counts; Live uses one attention-colored outline.
type Stories struct {
	Count, Unread int
	Live          bool
}

type Promotion struct {
	Label, Message string
}

// Conversation contains prepared presentation data. Labels (including draft,
// activity, time and counters) are localized by the host; no clock or I/O runs here.
// IDs are nonempty and unique. Avatar frames may be replaced by the host.
type Conversation struct {
	ID, Name, Initials string
	Avatar             *ui.Bitmap
	// AvatarRevision changes when Avatar.Update replaces pixels in place.
	AvatarRevision                              uint64
	AvatarIcon                                  *ui.SVG
	Kind                                        PeerKind
	Preview, Sender, Time                       string
	Service                                     bool
	Unread                                      int
	UnreadMark, Muted, Pinned                   bool
	Mention, Reaction, Poll                     bool
	Online                                      bool
	Receipt                                     Receipt
	Draft, DraftLabel                           string
	DraftSaving                                 bool
	Activity                                    string
	Verified, Premium                           bool
	Warning                                     string
	Topics                                      string
	Forum                                       bool
	Tags                                        []Tag
	Thumbnail                                   *ui.Bitmap
	Stories                                     Stories
	Call, Speaking, Subscribed, CommunityMember bool
	TTL                                         string
	ActivityKind                                ActivityKind
	Promotion                                   *Promotion
	VerificationCodes                           bool
}

type Tag struct {
	ID, Label string
	Color     ui.Color
}

type RowOptions struct {
	Selected, Compact, Disabled bool
	OnActivate                  func(string)
	Menu                        func(*ui.Menu, string)
	OnStory                     func(string)
	OnCommunity                 func(string)
}

type SearchOptions struct {
	Label, Placeholder, ClearLabel string
	Disabled                       bool
	OnChange                       func(string)
	OnSubmit                       func(string)
}

type RailMode uint8

const (
	IconsAndText RailMode = iota
	IconsOnly
	TextOnly
)

type Folder struct {
	ID, Name      string
	Icon          *ui.SVG
	Badge         string
	Muted, Locked bool
}

type RailOptions struct {
	Mode      RailMode
	OnSelect  func(string)
	OnLocked  func(string)
	Menu      func(*ui.Menu, string)
	OnReorder func(ids []string, before string)
}

// The public states expose one ID, so collapse mygo's keyed selection on commit.
func setChoice(choice *ui.Selection[string], id string) {
	choice.Clear()
	if id != "" {
		choice.Add(id)
	}
}
