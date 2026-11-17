package web

import (
	"context"
	"time"
)

type SearchRequest struct {
	Queries    []string
	MaxResults int
	TimeRange  string
}
type ProviderSearchRequest struct {
	Query      string
	MaxResults int
	TimeRange  string
}
type FetchRequest struct{ URL, Format string }
type Source struct {
	URL, Title, Snippet string
	SiteName            string
	PublishedAt         *time.Time
}
type Usage struct {
	UpstreamRequests, SuccessfulURLs int
	MicroCredits                     *int64
}
type ProviderSearchResult struct {
	Provider, RequestID string
	Sources             []Source
	Truncated           bool
	Usage               Usage
	Cacheable           bool
}
type ProviderFetchResult struct {
	Result    FetchResult
	Cacheable bool
}
type SearchResult struct {
	Provider          string
	Sources           []Source
	Truncated, Cached bool
	FetchedAt         time.Time
	Usage             Usage
}
type FetchResult struct {
	Provider, RequestID, RequestedURL, ReturnedURL string
	Title                                          string
	FinalURL                                       *string
	OriginStatus                                   *int
	ContentKind, Content                           string
	Truncated, CompletenessKnown                   bool
	TruncationReason                               string
	Cached                                         bool
	FetchedAt                                      time.Time
	Usage                                          Usage
}
type SearchProvider interface {
	ID() string
	Available() error
	Search(context.Context, ProviderSearchRequest) (ProviderSearchResult, error)
}
type FetchProvider interface {
	ID() string
	Available() error
	Fetch(context.Context, FetchRequest) (ProviderFetchResult, error)
}
