package dialogs

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/internal/draw"
	"github.com/tsugumi42/MewTypeUI/theme"
)

type rowColors struct{ bg, name, preview, time, service, draft, badge, badgeText, muted, receipt, verified ui.Color }

func colors(t *theme.Theme, selected, hovered, muted bool) rowColors {
	p := t.Colors().Conversation()
	v := rowColors{t.Materials().Conversation().Background(), p.NameForeground(), p.TextForeground(), p.TimestampForeground(), p.TextForegroundService(), p.DraftForeground(), p.UnreadBackground(), p.UnreadForeground(), p.UnreadBackgroundMuted(), p.SentIconForeground(), p.VerifiedIconBackground()}
	if hovered {
		v = rowColors{p.BackgroundHover(), p.NameForegroundHover(), p.TextForegroundHover(), p.TimestampForegroundHover(), p.TextForegroundServiceHover(), p.DraftForegroundHover(), p.UnreadBackgroundHover(), p.UnreadForegroundHover(), p.UnreadBackgroundMutedHover(), p.SentIconForegroundHover(), p.VerifiedIconBackgroundHover()}
	}
	if selected {
		v = rowColors{p.BackgroundActive(), p.NameForegroundActive(), p.TextForegroundActive(), p.TimestampForegroundActive(), p.TextForegroundServiceActive(), p.DraftForegroundActive(), p.UnreadBackgroundActive(), p.UnreadForegroundActive(), p.UnreadBackgroundMutedActive(), p.SentIconForegroundActive(), p.VerifiedIconBackgroundActive()}
	}
	if muted {
		v.badge = v.muted
	}
	return v
}

func hasDraft(item Conversation) bool {
	return item.Draft != "" && item.Unread == 0 && !item.UnreadMark && !item.Forum
}
func rowGeometry(t *theme.Theme, item Conversation, compact bool) (height, previewTop, tagTop float32) {
	m := t.Metrics().Conversation()
	height, previewTop, tagTop = m.RowHeight(), m.PreviewTop(), m.TagTop()
	if compact {
		return
	}
	if len(item.Tags) > 0 {
		height, previewTop = m.TaggedHeight(), m.TaggedPreviewTop()
	}
	if item.Forum {
		height, previewTop, tagTop = m.ForumHeight(), m.ForumPreviewTop(), m.ForumTagTop()
		if len(item.Tags) > 0 {
			height = m.TaggedForumHeight()
		}
	}
	return
}

// Row is a full-width rectangular conversation row. Set c.Key before calling
// it for a standalone row; List supplies identity from Conversation.ID.
func Row(c *ui.Context, item Conversation, o RowOptions) ui.Element {
	return buildRow(c, item, o, true)
}

func buildRow(c *ui.Context, item Conversation, o RowOptions, standalone bool) ui.Element {
	t := theme.T(c)
	m := t.Metrics().Conversation()
	ty := t.Typography()
	height, previewTop, tagTop := rowGeometry(t, item, o.Compact)
	_, titleHeight := measure(c, item.Name, ty.ConversationTitle())
	_, previewHeight := measure(c, "Mg", ty.ConversationPreview())
	// Fixed Telegram positions at 100%; enlarge the row for accessibility text.
	extra := max(float32(0), titleHeight-(m.PreviewTop()-m.TitleTop()))
	previewTop += extra
	tagTop += extra
	height = max(height+extra, previewTop+previewHeight+m.PaddingBottom())
	root := ui.Box(c).FillWidth().Height(height).Shrink(0).Label(item.Name).Disabled(o.Disabled).Clip()
	if standalone {
		root = root.OnClick(func() {
			if o.OnActivate != nil {
				o.OnActivate(item.ID)
			}
		}).Focusable().Role(ui.RoleButton)
	}
	hovered := root.Hovered()
	palette := colors(t, o.Selected, hovered, item.Muted)
	root.Background(palette.bg)
	pressed := t.Colors().Conversation().PressedBackground()
	if o.Selected {
		pressed = t.Colors().Conversation().PressedBackgroundActive()
	}
	ripple(c, root, pressed)
	if o.Menu != nil {
		root.ContextMenu(func(menu *ui.Menu) { o.Menu(menu, item.ID) })
	}
	draft := hasDraft(item)
	badgesAbove := o.Compact && !draft && (item.Unread > 0 || item.UnreadMark || item.Mention || item.Reaction || item.Poll)
	online := animate(root, "online", unit(item.Online && !badgesAbove), t.Motion().Online())
	activity := ui.Local(root, "activity-phase", func() activityPhase { return activityPhase{} })
	if activity.label != item.Activity || activity.kind != item.ActivityKind {
		activity.label, activity.kind = item.Activity, item.ActivityKind
		activity.since = c.Now()
	}
	root.Children(func() {
		avatar(c, item, palette.bg, online, o, badgesAbove)
		if o.Compact {
			if !draft {
				rowBadges(c, item, palette, true, o.Selected, m.PaddingTop()+m.AvatarSize()-m.BadgeHeight())
			}
			return
		}
		topLabel := item.Time
		if item.Promotion != nil {
			topLabel = item.Promotion.Label
		}
		timeWidth, _ := measure(c, topLabel, ty.ConversationTime())
		receipt := item.Receipt
		if item.Promotion != nil {
			receipt = NoReceipt
		}
		if draft {
			receipt = NoReceipt
			if item.DraftSaving {
				receipt = Sending
			}
		}
		right := m.PaddingRight()
		text(c, topLabel, ty.ConversationTime(), palette.time).Absolute().Top(m.TitleTop()).Right(right).SingleLine()
		if timeWidth > 0 {
			right += timeWidth + m.DateSkip()
		}
		if receipt != NoReceipt {
			src := clockIcon
			color := palette.receipt
			w, h, x, y := m.SendingIcon().Width(), m.SendingIcon().Height(), m.SendingIcon().Left(), m.SendingIcon().Top()
			if receipt == Sent {
				src = sentIcon
				w, h, x, y = m.SentIcon().Width(), m.SentIcon().Height(), m.SentIcon().Left(), m.SentIcon().Top()
			}
			if receipt == Read {
				src = readIcon
				w, h, x, y = m.ReadIcon().Width(), m.ReadIcon().Height(), m.ReadIcon().Left(), m.ReadIcon().Top()
			}
			if receipt == Sending {
				p := t.Colors().Conversation()
				color = p.SendingIconForeground()
				if o.Selected {
					color = p.SendingIconForegroundActive()
				} else if hovered {
					color = p.SendingIconForegroundHover()
				}
			}
			glyph := draw.Fit(c, src, w, h, w, h, t.Metrics().Glyph().SmallStroke())
			glyph.Element(c).TextColor(color).Absolute().Top(m.TitleTop() + y).Right(right + m.SendStateWidth() - x - w)
			right += m.SendStateWidth()
		}
		ui.Row(c).Absolute().Left(m.TitleLeft()).Right(right).Top(m.TitleTop()).Height(titleHeight).AlignItems(ui.Center).Gap(m.TypeSkip()).Children(func() {
			var src *ui.SVG
			var w, h, x, y float32
			switch item.Kind {
			case Group:
				src = groupIcon
				w, h, x, y = m.GroupIcon().Width(), m.GroupIcon().Height(), m.GroupIcon().Left(), m.GroupIcon().Top()
			case Channel:
				src = channelIcon
				w, h, x, y = m.ChannelIcon().Width(), m.ChannelIcon().Height(), m.ChannelIcon().Left(), m.ChannelIcon().Top()
			case Bot:
				src = botIcon
				w, h, x, y = m.BotIcon().Width(), m.BotIcon().Height(), m.BotIcon().Left(), m.BotIcon().Top()
			case Secret:
				src = lockIcon
				w, h = m.IconSize(), m.IconSize()
			}
			if src != nil {
				ic := t.Colors().Conversation().ChatIconForeground()
				if o.Selected {
					ic = t.Colors().Conversation().ChatIconForegroundActive()
				} else if hovered {
					ic = t.Colors().Conversation().ChatIconForegroundHover()
				}
				ui.Box(c).Size(w+x, titleHeight).Shrink(0).Children(func() {
					draw.Element(c, src, w, h).TextColor(ic).Absolute().Left(x).Top(y)
				})
			}
			text(c, item.Name, ty.ConversationTitle(), palette.name).SingleLine().MinWidth(0).Shrink(1)
			if item.Verified {
				icon(c, verifiedIcon, m.IconSize(), palette.verified)
			}
			if item.Premium {
				text(c, "★", ty.ConversationTitle(), palette.verified).Shrink(0)
			}
			if item.Warning != "" {
				text(c, item.Warning, ty.ConversationTag(), palette.draft).Shrink(0)
			}
			if item.Muted {
				// The row gap already accounts for TypeSkip of the mute separation.
				ui.Box(c).Size(m.MuteIconSize()+m.MuteSkip()-m.TypeSkip(), titleHeight).Shrink(0).Children(func() {
					icon(c, muteIcon, m.MuteIconSize(), palette.muted).Absolute().Right(0).Top((titleHeight - m.MuteIconSize()) / 2)
				})
			}
		})
		used := float32(0)
		if !draft {
			used = rowBadges(c, item, palette, false, o.Selected, previewTop)
		} else if item.Pinned {
			icon(c, pinIcon, m.IconSize(), palette.muted).Absolute().Top(previewTop).Right(m.PaddingRight())
			used = m.IconSize() + m.BadgePadding()
		}
		ui.Row(c).Absolute().Left(m.PreviewLeft()).Right(m.PaddingRight() + used).Top(previewTop).Height(previewHeight + unit(item.Forum)*m.TopicHeight()).AlignItems(ui.Start).Gap(m.TypeSkip()).Clip().Children(func() {
			if item.Promotion != nil && item.Promotion.Message != "" {
				text(c, item.Promotion.Message, ty.ConversationPreview(), palette.preview).SingleLine().MinWidth(0)
			} else if item.Activity != "" {
				activityIndicator(c, item.ActivityKind, palette.service, previewHeight, activity.since)
				text(c, item.Activity, ty.ConversationPreview(), palette.service).SingleLine().MinWidth(0).Shrink(1)
			} else if draft {
				ui.RichText(c, ui.Span{Text: item.DraftLabel, Color: palette.draft}, ui.Span{Text: item.Draft, Color: palette.preview}).Font(ty.Font()).FontSize(ty.ConversationPreview().Size() * c.Preferences().TextScale).SingleLine().MinWidth(0)
			} else {
				if item.Thumbnail != nil {
					ui.Image(c, item.Thumbnail).Size(previewHeight, previewHeight).Fit(ui.Cover).Radius(t.Metrics().Control().RadiusSmall()).Shrink(0)
				}
				ui.Column(c).MinWidth(0).Grow(1).Children(func() {
					if item.Forum {
						text(c, item.Topics, ty.ConversationPreview(), palette.service).SingleLine().Height(m.TopicHeight()).FillWidth()
					}
					color := palette.preview
					if item.Service {
						color = palette.service
					}
					if item.Sender != "" {
						ui.RichText(c, ui.Span{Text: item.Sender, Color: palette.name}, ui.Span{Text: item.Preview, Color: color}).Font(ty.Font()).FontSize(ty.ConversationPreview().Size() * c.Preferences().TextScale).SingleLine().FillWidth()
					} else {
						text(c, item.Preview, ty.ConversationPreview(), color).SingleLine().FillWidth()
					}
				})
			}
		})
		if len(item.Tags) > 0 {
			ui.Row(c).Absolute().Left(m.TitleLeft()).Right(m.PaddingRight()).Top(tagTop).Gap(m.TagGap()).Clip().Children(func() {
				space, h := measure(c, " ", ty.ConversationTag())
				for _, tag := range item.Tags {
					c.Key(tag.ID)
					fg, bg := tag.Color, tag.Color.Alpha(m.TagAlpha())
					if o.Selected {
						fg, bg = palette.bg, palette.preview
					}
					text(c, tag.Label, ty.ConversationTag(), fg).PaddingX(space * 3 / 2).Radius(h / 3).Background(bg).Shrink(0)
				}
			})
		}
	})
	return root
}

func avatar(c *ui.Context, item Conversation, bg ui.Color, online float32, o RowOptions, covered bool) {
	t := theme.T(c)
	m := t.Metrics().Conversation()
	first, second := avatarColors(t, item)
	box := ui.Box(c).Absolute().Left(m.PaddingLeft()).Top(m.PaddingTop()).Size(m.AvatarSize(), m.AvatarSize())
	if item.Stories.Count > 0 || item.Stories.Live {
		storyRing(c, box, item.Stories, o.Selected, item.Forum)
	}
	box.Children(func() {
		inset := float32(0)
		if item.Stories.Count > 0 || item.Stories.Live {
			inset = m.StoryWidth() * 3 / 2
		}
		size := m.AvatarSize() - 2*inset
		radius := size / 2
		if item.Forum {
			radius = size * m.ForumRadiusRatio()
		}
		a := ui.Box(c).Absolute().Left(inset).Top(inset).Size(size, size).Radius(radius).Clip().Gradient(first, second, 180).Center()
		if o.OnStory != nil && (item.Stories.Count > 0 || item.Stories.Live) {
			a.OnClick(func() { o.OnStory(item.ID) }).Focusable().Role(ui.RoleButton).Label("Stories " + item.ID)
		}
		a.Children(func() {
			avatarContent(c, item, size, c.Preferences().TextScale)
		})
		cornerBadge(c, box, item, bg, online, o, covered)
	})
}

func avatarColors(t *theme.Theme, item Conversation) (ui.Color, ui.Color) {
	if item.VerificationCodes {
		return t.Colors().Avatar().SavedMessagesBackground(), t.Colors().Avatar().SavedMessagesBackground2()
	}
	if item.Kind == Archive {
		return t.Colors().Avatar().ArchiveUserpicBackground(), t.Colors().Avatar().ArchiveUserpicBackground()
	}
	return t.Colors().Avatar().Peer4UserpicBackground(), t.Colors().Avatar().Peer4UserpicBackground2()
}

func avatarContent(c *ui.Context, item Conversation, size, scale float32) {
	t := theme.T(c)
	src := item.AvatarIcon
	if item.Kind == Archive && src == nil {
		src = archiveIcon
	}
	if item.VerificationCodes && src == nil {
		src = verifiedIcon
	}
	if item.Avatar != nil {
		ui.Image(c, item.Avatar).Fill().Fit(ui.Cover)
	} else if src != nil {
		glyphSize := size - 2*t.Metrics().Conversation().PaddingLeft()
		// paint/icon-glyph.md#图形限框: avatar artwork has its own purpose.
		if src == archiveIcon {
			gm := t.Metrics().Glyph()
			draw.Fit(c, src, glyphSize, glyphSize, gm.Archive().Width(), gm.Archive().Height(), gm.Stroke()).Element(c).TextColor(t.Colors().Avatar().UserpicForeground())
		} else {
			ui.Icon(c, src).Size(glyphSize, glyphSize).TextColor(t.Colors().Avatar().UserpicForeground()).Shrink(0)
		}
	} else {
		style := t.Typography().Avatar()
		ui.Text(c, item.Initials).Font(t.Typography().Font()).FontSize(style.Size() * scale).FontWeight(style.Weight()).TextColor(t.Colors().Avatar().UserpicForeground())
	}
}

func rowBadges(c *ui.Context, item Conversation, p rowColors, compact, selected bool, top float32) float32 {
	t := theme.T(c)
	m := t.Metrics().Conversation()
	container := ui.Row(c).Absolute().Top(top).Gap(m.BadgePadding()).AlignItems(ui.Center)
	container.Right(m.PaddingRight())
	used := float32(0)
	hasUnread := item.Unread > 0 || item.UnreadMark
	hasPersonal := item.Mention || item.Reaction
	container.Children(func() {
		marks := [3]struct {
			show  bool
			label string
		}{{item.Poll && (!compact || !hasUnread || !hasPersonal), "▥"}, {item.Reaction && !item.Mention, "♥"}, {item.Mention, "@"}}
		count := 0
		for _, mark := range marks {
			if mark.show {
				if compact {
					badge(c, mark.label, m.BadgeHeight(), m.BadgePadding(), p.badge, p.badgeText, t.Typography().Badge())
				} else {
					color := t.Colors().Conversation().MentionIconForeground()
					if mark.label == "♥" {
						color = t.Colors().Conversation().ReactionIconForeground()
					}
					if mark.label == "▥" {
						color = t.Colors().Conversation().PollIconForeground()
					}
					if item.Muted {
						color = p.muted
					} else if selected {
						color = p.name
					}
					ui.Box(c).Size(m.BadgeHeight(), m.BadgeHeight()).Center().Children(func() { text(c, mark.label, t.Typography().Badge(), color) })
				}
				used += m.BadgeHeight() + m.BadgePadding()
				count++
			}
		}
		if item.Unread > 0 || item.UnreadMark {
			label := ""
			if item.Unread > 0 {
				label = strconv.Itoa(item.Unread)
				// unread_counter_format.cpp:20-24 caps only the compact form.
				if compact && (item.Mention || item.Reaction || item.Poll) && item.Unread > 999 {
					label = "99+"
				} else if compact && item.Unread > 999999 {
					label = "99999+"
				}
			}
			w := badge(c, label, m.BadgeHeight(), m.BadgePadding(), p.badge, p.badgeText, t.Typography().Badge())
			used += w + m.BadgePadding()
		} else if count == 0 && item.Pinned && !compact {
			icon(c, pinIcon, m.IconSize(), p.muted)
			used += m.IconSize() + m.BadgePadding()
		}
	})
	return used
}
func badge(c *ui.Context, label string, height, padding float32, bg, fg ui.Color, style theme.TextStyle) float32 {
	w, h := measure(c, label, style)
	height = max(height, h)
	w = max(height, w+2*padding)
	ui.Box(c).Width(w).Height(height).Radius(height / 2).Background(bg).Center().Children(func() { text(c, label, style, fg).SingleLine() })
	return w
}

type activityPhase struct {
	label string
	kind  ActivityKind
	since time.Time
}

func typing(c *ui.Context, color ui.Color, height float32, since time.Time) {
	t := theme.T(c)
	m := t.Metrics().Activity()
	motion := t.Motion()
	reduced := c.Preferences().ReduceMotion
	ui.Box(c).Size(m.DotLeft()+3*m.DotGap(), height).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
		period, half, stagger := motion.Typing().Duration(), motion.TypingHalfPeriod().Duration(), motion.TypingStagger().Duration()
		for i := range 3 {
			progress := float32(0)
			if period > 0 && half > 0 {
				elapsed := time.Duration(0)
				if !reduced {
					elapsed = p.Now().Sub(since)
				}
				elapsed = (elapsed - time.Duration(i)*stagger) % period
				if elapsed < 0 {
					elapsed += period
				}
				curve := motion.Typing().Easing().Curve()
				if elapsed < half {
					progress = curve(float32(elapsed) / float32(half))
				} else if elapsed < 2*half {
					progress = 1 - curve(float32(elapsed-half)/float32(half))
				}
			}
			radius := m.DotSmallRadius() + (m.DotLargeRadius()-m.DotSmallRadius())*progress
			p.Fill(ui.Rect{X: r.X + m.DotLeft() + 0.5 + float32(i)*m.DotGap() - radius, Y: r.Y + r.H/2 - radius, W: 2 * radius, H: 2 * radius}, color, radius)
		}
		if !reduced && period > 0 {
			p.AnimationFrame()
		}
	})
}
