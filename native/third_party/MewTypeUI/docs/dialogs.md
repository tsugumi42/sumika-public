# Dialogs

`dialogs.Row`, `List`, `Search`, `SearchScopes`, `Rail` and `GroupHeading` render prepared host data. All constructors take `*ui.Context` first and options last, and return a root `ui.Element`. Apply `theme.Apply` before constructing them.

```go
var state dialogs.ListState
model := dialogs.NewModel([]dialogs.Entry{
    {HeadingID: "recent", Heading: "Recent"},
    {Conversation: dialogs.Conversation{
        ID: "room-1", Name: "Rehearsal", Initials: "R",
        Preview: "See you tomorrow", Time: "09:41", Unread: 2,
    }},
})

func view(c *ui.Context) {
    theme.Apply(c, theme.Light())
    dialogs.List(c, model, &state, dialogs.ListOptions{
        Label: "Conversations",
        OnSelect: func(id string) { /* host changes the active conversation */ },
    }).Fill()
}
```

Build a new Model when the host changes or filters the snapshot; keep it between frames. NewModel borrows the immutable entry slice and builds an ID index once. Conversation IDs and HeadingIDs share a unique, nonempty namespace. The virtual list builds visible rows plus mygo's overscan. Never retain a returned Element. ListState and RailState belong to one list in one window; create separate states for separate windows.

Set `ListState.SelectedID` to select from the host. An empty ID means no selection. A click or Up/Down/Home/End/PageUp/PageDown updates it and calls OnSelect; Enter or a double click calls OnActivate. Group headings are skipped and remain at the top while their section scrolls. `state.ScrollTo(model, id, ui.Center)` reveals an ID without selecting it. The viewport stays at its offset when the host moves active conversations to the top. Menu and OnReorder callbacks receive IDs; the host owns actions and ordering.

Row supports regular, selected, hovered, pressed and disabled states; circular prepared avatars or initials; group/channel/bot/secret/archive kinds; title, sender, service preview, thumbnail, time; unread count/mark, muted badges, pins, mentions/reactions/polls; sending/sent/read; online transitions; drafts and typing; verified/premium/warning marks; folder tags and forum topics. Drafts are visible when there are no unread messages and the row is not a forum. Activity replaces the preview or draft; the draft label is host-localized and includes its separator. The host supplies the complete activity label (for example “typing…”), time, sender separator, warning and names. Avatar bitmaps can be updated for host-decoded video frames.

`Stories{Count, Unread}` adds segmented read/unread avatar rings; `OnStory` opens stories independently of the row action. `Speaking` drives the group-call spectrum. `Subscribed`, `CommunityMember`, and host-formatted `TTL` use the source corner-badge priority: compact unread, subscription, community, call/online, TTL. `AvatarRevision` invalidates TTL backdrop caching when a prepared avatar changes. `Promotion` supplies its tag and preview; `VerificationCodes` selects the dedicated row title/avatar. Set `ActivityKind` to `RecordingActivity`, `UploadingActivity` or `ChoosingStickerActivity`; the zero value keeps typing.

For standalone rows, set `c.Key(item.ID)` before Row. List supplies the identity and handles focus. `Compact` hides textual content and overlays unread badges on the avatar. Set the host pane width to `dialogs.CompactWidth(c)` (66 DIP with the default theme); List does not own the surrounding window's column sizing.

Search binds directly to a persistent query string. OnChange observes each edit or clear; OnSubmit observes Enter. Clear restores input focus. Provide localized Label, Placeholder and ClearLabel. Search does no filtering, debounce, networking or file access.

`SearchScopes(c, scopes, selectedID, options)` displays a host-localized heading, the selected scope, optional sender and independent cancel controls. Scope choices use stable IDs; `OnSelect`, `OnSender`, `OnCancelScope` and `OnCancelSender` report intentions. The host updates the prepared scope snapshot.

Rail binds to RailState.SelectedID. Folders have stable IDs, a host SVG (or the original filled folder), a host-formatted badge and muted/locked state. IconsAndText, IconsOnly and TextOnly correspond to the desktop's vertical folder modes. Labels wrap to three lines. Locked folders call OnLocked without changing the current folder. OnReorder reports host intent.

`FolderPages(c, orderedIDs, selectedID, content)` fills its assigned slot. The selected ID must be in the ordered IDs. It uses `Motion.Page` (240 ms easeOutCirc), `Sidebar.SlideShift` (100 DIP), parallax and disjoint visible clips. The callback may build both folders during a transition; keep each folder's model, selection and scroll state in the host. Content input is disabled during motion; ReduceMotion completes immediately. `dialogs/folders` and the complete chat gallery exercise rail and horizontal tab selection.

Gallery pages registered in gallery/dialogs.go cover rows, compact rows, the 50,000-row virtual list, search, folder rail, group headings, stories/corner badges/activities, and search scopes/senders. Geometry and behavioral provenance: reference/dialogs.md, theme-metrics.md, theme-colors.md and theme-behavior.md. Explicit substitutions and host boundaries: reference/exceptions.md.

## Verification record · 2026-11-03

The Windows native harness rendered the registered pages through mygo v0.7.4, with a 1266×862 DIP content area captured as 1582×1078 pixels. Real input selected a conversation, End reached conversation-49999, Enter activated that ID, and Up selected conversation-49998. The inspector reported 15 list items at the end, including overscan; scrolling back built 18 list items out of the 50,000-entry model.

Typing `49999` into search left exactly that conversation. Clearing and immediately typing `00042` preserved input focus and produced exactly that result. Switching light/night preserved the query and selection. Captures also covered ordinary/selected/pressed rows, draft and receipt states, live typing, the 66 DIP compact column, three folder modes, keyboard folder selection, locked appearance and group headings.

The assertion inversion overlay and its log live in the external dialogs scratch directory, outside the repository. This deliberately reverses the assertion helper and requires every named assertion to appear in the failed run before the normal suite is run. It verifies the assertion failure paths; it is not a mutation of each production behavior.

The first inversion run exposed two real failing behaviors: activation on a double click and host selection changed in the same frame as a reorder. Double-click notices arrive on pointer press, before release-based OnClick; activation now reads that notice directly. Host selection also supplies keyed membership so a replacement at the previous numeric row is recognized; list and folder commits retain exactly one selected ID.

A second native harness confirmed exactly one activation for a double click. Replacing both the selected ID and the order in one action selected `b`; a subsequent Down selected `a` in the conversation list and in a folder rail whose two folders had equal names. The inspector and rendered selection agreed.

Native screenshots were inspected directly; they were not compared side by side with a running Telegram window. Icon contours, font metrics and the host boundaries remain subject to the explicit reference exceptions.
