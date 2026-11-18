package memview

import (
	"context"
	"errors"

	"sumika-native/internal/api"
	"sumika-native/internal/memory"
)

// paramErr 把 memory 包的参数错误换成本包的；别的错误原样（不带正文）
func paramErr(err error) error {
	if errors.Is(err, memory.ErrBrowseParam) {
		return ErrParam
	}
	return err
}

func (s *Service) ListMemory(ctx context.Context, req api.MemoryListRequest) (api.Result[api.MemoryPage], error) {
	b, err := s.browser(req.Character)
	if err != nil {
		return failed[api.MemoryPage](err)
	}
	q := memory.BrowseQuery{Kind: req.Kind, Groups: req.Groups, ScopeKind: req.ScopeKind, Conversation: req.Conversation, Subject: req.Subject,
		Query: req.Query, Cursor: string(req.Cursor), Limit: req.Limit}
	if s.opt.ResolvePerson != nil && req.Query != "" {
		q.SubjectIDs = s.opt.ResolvePerson(req.Query)
	}
	var page memory.BrowsePage
	if err := s.run(ctx, func(ctx context.Context) (err error) {
		page, err = b.List(ctx, q)
		return err
	}); err != nil {
		return failed[api.MemoryPage](paramErr(err))
	}
	out := api.MemoryPage{Truncated: page.Truncated}
	out.Items = make([]api.MemoryRow, len(page.Rows))
	for i, r := range page.Rows {
		out.Items[i] = s.row(r)
	}
	out.NextCursor, out.HasMore = api.Cursor(page.Next), page.Next != ""
	return ready(out)
}

func (s *Service) ReadMemoryDetail(ctx context.Context, character, ref string) (api.Result[api.MemoryDetail], error) {
	b, err := s.browser(character)
	if err != nil {
		return failed[api.MemoryDetail](err)
	}
	var d memory.Detail
	if err := s.run(ctx, func(ctx context.Context) (err error) {
		d, err = b.Detail(ctx, ref)
		return err
	}); err != nil {
		return failed[api.MemoryDetail](paramErr(err))
	}
	out := api.MemoryDetail{
		Row: s.row(d.Row), Origin: originText(d.Origin), SpeechAct: memory.CleanLine(d.SpeechAct, 30), SupersededBy: d.SupersededBy,
		Recall: api.MemoryRecall{Can: d.Recall.Can, Reason: d.Recall.Reason}, Places: s.places(d.Row.Scopes), SourcesTotal: d.SourcesTotal, Migrated: d.Schema.Migrated(),
		Whisper: d.Whisper.Text(),
	}
	if d.Occasions != nil {
		o := *d.Occasions
		out.Occasions = &api.MemoryOccasions{Have: o.Have, Need: o.Need, Self: o.Self, Rows: o.Rows, Auto: o.Auto}
	}
	for _, src := range d.Sources {
		out.Sources = append(out.Sources, api.MemorySource{Conversation: src.Conversation, Seq: src.Seq, Label: s.convLabel(src.Conversation)})
	}
	if d.Row.Kind == "episode" {
		out.Sources, out.SourcesTotal = s.episodeSources(d.Row)
	}
	return ready(out)
}

func (s *Service) ReadMemoryEvidence(ctx context.Context, character, ref string, cursor api.Cursor) (api.Result[api.MemoryEvidencePage], error) {
	b, err := s.browser(character)
	if err != nil {
		return failed[api.MemoryEvidencePage](err)
	}
	var p memory.EvidencePage
	if err := s.run(ctx, func(ctx context.Context) (err error) {
		p, err = b.Evidence(ctx, ref, string(cursor))
		return err
	}); err != nil {
		return failed[api.MemoryEvidencePage](paramErr(err))
	}
	out := api.MemoryEvidencePage{Total: p.Total, Missing: p.Missing}
	for _, e := range p.Rows {
		out.Items = append(out.Items, api.MemoryEvidenceRow{
			Day: memory.CleanLine(e.Day, 20), Speaker: s.who(e.Speaker), BySubject: e.BySubject, Verdict: memory.CleanLine(e.Verdict, 30), At: e.At,
			Conversation: api.MemorySource{Conversation: e.Conversation, Seq: e.Seq, Label: s.convLabel(e.Conversation)},
		})
	}
	out.NextCursor, out.HasMore = api.Cursor(p.Next), p.Next != ""
	return ready(out)
}

func (s *Service) ReadMemoryRevisions(ctx context.Context, character, ref string, cursor api.Cursor) (api.Result[api.MemoryRevisionPage], error) {
	b, err := s.browser(character)
	if err != nil {
		return failed[api.MemoryRevisionPage](err)
	}
	var p memory.RevisionPage
	if err := s.run(ctx, func(ctx context.Context) (err error) {
		p, err = b.Revisions(ctx, ref, string(cursor))
		return err
	}); err != nil {
		return failed[api.MemoryRevisionPage](paramErr(err))
	}
	var out api.MemoryRevisionPage
	for _, r := range p.Rows {
		out.Items = append(out.Items, s.revision(r))
	}
	out.NextCursor, out.HasMore = api.Cursor(p.Next), p.Next != ""
	return ready(out)
}

func (s *Service) ReadMemoryRecent(ctx context.Context, character string) (api.Result[[]api.MemoryRecentChange], error) {
	b, err := s.browser(character)
	if err != nil {
		return failed[[]api.MemoryRecentChange](err)
	}
	var rows []memory.RecentRow
	if err := s.run(ctx, func(ctx context.Context) (err error) {
		rows, err = b.Recent(ctx)
		return err
	}); err != nil {
		return failed[[]api.MemoryRecentChange](paramErr(err))
	}
	out := make([]api.MemoryRecentChange, len(rows))
	for i, r := range rows {
		out[i] = api.MemoryRecentChange{MemoryRevision: s.revision(r.RevisionRow), Ref: "f:" + memory.CleanLine(r.FactID, 80), Text: r.Text, Exists: r.Exists}
	}
	return ready(out)
}

// episodeSources：片段没有修订表，也没有 source_seqs，出处是 from_seq..to_seq 这一段。只给头尾各最多 3 句（中间的不列），总数是这一段的句数
func (s *Service) episodeSources(r memory.BrowseRow) ([]api.MemorySource, int) {
	if r.Conversation == "" || r.FromSeq <= 0 || r.ToSeq < r.FromSeq {
		return nil, 0
	}
	label := s.convLabel(r.Conversation)
	total := int(min(r.ToSeq-r.FromSeq+1, int64(1)<<31))
	var seqs []int64
	for q := r.FromSeq; q <= r.ToSeq && q < r.FromSeq+3; q++ {
		seqs = append(seqs, q)
	}
	for q := max(r.ToSeq-2, r.FromSeq+3); q <= r.ToSeq; q++ {
		seqs = append(seqs, q)
	}
	out := make([]api.MemorySource, len(seqs))
	for i, q := range seqs {
		out[i] = api.MemorySource{Conversation: r.Conversation, Seq: q, Label: label}
	}
	return out, total
}

// originText：facts.origin 存的是出处那句话的 origin（谁说的那一类）。认得的翻成人话，不认得的不显示（不把内部字段值露给界面）
func originText(o string) string {
	switch o {
	case "owner":
		return "你说的"
	case "peer":
		return "对方说的"
	case "human":
		return "群友说的"
	case "external":
		return "外部来的话"
	case "character":
		return "她自己说的"
	}
	return ""
}
