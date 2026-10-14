package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/rssfeed"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type memoryRSSFeedStore struct {
	feeds  []tables.TableRSSFeed
	nextID uint
}

func (s *memoryRSSFeedStore) ListRSSFeeds(context.Context) ([]tables.TableRSSFeed, error) {
	return append([]tables.TableRSSFeed(nil), s.feeds...), nil
}

func (s *memoryRSSFeedStore) GetRSSFeed(_ context.Context, id uint) (*tables.TableRSSFeed, error) {
	for i := range s.feeds {
		if s.feeds[i].ID == id {
			feed := s.feeds[i]
			return &feed, nil
		}
	}
	return nil, configstore.ErrRSSFeedNotFound
}

func (s *memoryRSSFeedStore) CreateRSSFeed(_ context.Context, feed *tables.TableRSSFeed) error {
	s.nextID++
	feed.ID = s.nextID
	s.feeds = append(s.feeds, *feed)
	return nil
}

func (s *memoryRSSFeedStore) UpdateRSSFeed(_ context.Context, feed *tables.TableRSSFeed) error {
	for i := range s.feeds {
		if s.feeds[i].ID == feed.ID {
			s.feeds[i] = *feed
			return nil
		}
	}
	return configstore.ErrRSSFeedNotFound
}

func (s *memoryRSSFeedStore) DeleteRSSFeed(_ context.Context, id uint) error {
	for i := range s.feeds {
		if s.feeds[i].ID == id {
			s.feeds = append(s.feeds[:i], s.feeds[i+1:]...)
			return nil
		}
	}
	return configstore.ErrRSSFeedNotFound
}

func (s *memoryRSSFeedStore) SeedBuiltinRSSFeeds(context.Context) error { return nil }

const testRSSFeedBody = `<rss version="2.0"><channel>
<item><title>Old</title><link>https://example.com/old</link><guid>old</guid><pubDate>Mon, 01 Jan 2024 00:00:00 GMT</pubDate></item>
<item><title>Script link</title><link>javascript:alert(1)</link><guid>bad</guid><pubDate>Mon, 01 Jan 2024 00:00:00 GMT</pubDate></item>
</channel></rss>`

const testAtomFeedBody = `<feed xmlns="http://www.w3.org/2005/Atom">
<entry><id>new</id><title>New</title><link href="https://example.com/new"/><updated>2025-01-01T00:00:00Z</updated></entry>
</feed>`

func newRSSFeedTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/rss", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(testRSSFeedBody)) })
	mux.HandleFunc("/atom", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(testAtomFeedBody)) })
	mux.HandleFunc("/html", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html><body>hi</body></html>")) })
	mux.HandleFunc("/down", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestRSSFeedsHandler(srv *httptest.Server, feeds ...tables.TableRSSFeed) (*RSSFeedsHandler, *memoryRSSFeedStore) {
	store := &memoryRSSFeedStore{feeds: feeds, nextID: uint(len(feeds))}
	return &RSSFeedsHandler{store: store, manager: rssfeed.NewManagerWithClient(srv.Client())}, store
}

func rssRequest(body string, id uint) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	if body != "" {
		ctx.Request.SetBody([]byte(body))
	}
	if id != 0 {
		ctx.SetUserValue("id", strconv.FormatUint(uint64(id), 10))
	}
	return ctx
}

func TestRSSItemsMergesEnabledFeedsAndReportsFailures(t *testing.T) {
	srv := newRSSFeedTestServer(t)
	h, _ := newTestRSSFeedsHandler(srv,
		tables.TableRSSFeed{ID: 1, Slug: "rss", Name: "RSS", URL: srv.URL + "/rss", Icon: "openai", Source: tables.RSSFeedSourceBuiltin, Enabled: true},
		tables.TableRSSFeed{ID: 2, Slug: "atom", Name: "Atom", URL: srv.URL + "/atom", IconURL: "https://example.com/atom.png", Source: tables.RSSFeedSourceCustom, Enabled: true},
		tables.TableRSSFeed{ID: 3, Slug: "down", Name: "Down", URL: srv.URL + "/down", Source: tables.RSSFeedSourceCustom, Enabled: true},
		tables.TableRSSFeed{ID: 4, Slug: "off", Name: "Off", URL: srv.URL + "/atom", Source: tables.RSSFeedSourceCustom, Enabled: false},
	)
	ctx := rssRequest("", 0)
	h.listItems(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	var response rssItemsResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &response))
	require.Len(t, response.Items, 2, "disabled feed skipped, javascript: link dropped")
	require.Equal(t, "atom:new", response.Items[0].ID, "newest first across feeds")
	require.Equal(t, "Atom", response.Items[0].FeedName)
	require.Equal(t, "https://example.com/atom.png", response.Items[0].FeedIconURL)
	require.Equal(t, "rss:old", response.Items[1].ID)
	require.Equal(t, "openai", response.Items[1].FeedIcon)
	require.Equal(t, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), response.Items[1].PublishedAt)

	require.Len(t, response.Errors, 1)
	require.Equal(t, uint(3), response.Errors[0].FeedID)
	require.Equal(t, rssFeedUnavailableMessage, response.Errors[0].Message, "upstream detail must not leak")
}

func TestRSSFeedCreateValidatesAndProbes(t *testing.T) {
	srv := newRSSFeedTestServer(t)
	h, store := newTestRSSFeedsHandler(srv)

	for name, body := range map[string]string{
		"missing name": `{"name":"  ","url":"` + srv.URL + `/rss"}`,
		"bad scheme":   `{"name":"x","url":"ftp://example.com/feed"}`,
		"not a feed":   `{"name":"x","url":"` + srv.URL + `/html"}`,
		"unreachable":  `{"name":"x","url":"` + srv.URL + `/down"}`,
	} {
		ctx := rssRequest(body, 0)
		h.createFeed(ctx)
		require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), name)
	}
	require.Empty(t, store.feeds, "rejected feeds must not be stored")

	ctx := rssRequest(`{"name":"x","url":"`+srv.URL+`/atom","icon_url":"javascript:alert(1)"}`, 0)
	h.createFeed(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "non-image icon_url rejected")

	ctx = rssRequest(`{"name":" My Releases ","url":"`+srv.URL+`/atom","icon_url":"https://example.com/logo.png"}`, 0)
	h.createFeed(ctx)
	require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.Len(t, store.feeds, 1)
	created := store.feeds[0]
	require.Equal(t, "My Releases", created.Name)
	require.Equal(t, tables.RSSFeedSourceCustom, created.Source)
	require.True(t, created.Enabled, "custom feeds default to enabled")
	require.Equal(t, "https://example.com/logo.png", created.IconURL)
	require.Regexp(t, `^custom-my-releases-`, created.Slug)
}

func TestRSSFeedBuiltinGuards(t *testing.T) {
	srv := newRSSFeedTestServer(t)
	h, store := newTestRSSFeedsHandler(srv,
		tables.TableRSSFeed{ID: 1, Slug: "openai", Name: "OpenAI", URL: srv.URL + "/rss", Source: tables.RSSFeedSourceBuiltin},
	)

	ctx := rssRequest(`{"url":"`+srv.URL+`/atom"}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "builtin URL is fixed")

	ctx = rssRequest(`{"icon_url":"https://example.com/logo.png"}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "builtin logo is fixed")

	ctx = rssRequest("", 1)
	h.deleteFeed(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "builtin cannot be deleted")

	ctx = rssRequest(`{"enabled":true}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.True(t, store.feeds[0].Enabled)

	ctx = rssRequest(`{"enabled":true}`, 99)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
}

func TestRSSFeedCustomUpdateAndDelete(t *testing.T) {
	srv := newRSSFeedTestServer(t)
	h, store := newTestRSSFeedsHandler(srv,
		tables.TableRSSFeed{ID: 1, Slug: "custom-x", Name: "X", URL: srv.URL + "/rss", Source: tables.RSSFeedSourceCustom, Enabled: true},
	)
	// Warm the cache so a URL change has an entry to drop.
	_, err := h.manager.Get(context.Background(), srv.URL+"/rss")
	require.NoError(t, err)

	ctx := rssRequest(`{"url":"`+srv.URL+`/html"}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), "new URL is probed")
	require.Equal(t, srv.URL+"/rss", store.feeds[0].URL)

	ctx = rssRequest(`{"name":"Renamed","url":"`+srv.URL+`/atom"}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.Equal(t, "Renamed", store.feeds[0].Name)
	require.Equal(t, srv.URL+"/atom", store.feeds[0].URL)

	const pngLogo = "data:image/png;base64,iVBORw0KGgo="
	ctx = rssRequest(`{"icon_url":"`+pngLogo+`"}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	require.Equal(t, pngLogo, store.feeds[0].IconURL)

	ctx = rssRequest(`{"icon_url":""}`, 1)
	h.updateFeed(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.Empty(t, store.feeds[0].IconURL, "empty icon_url clears the logo")

	ctx = rssRequest("", 1)
	h.deleteFeed(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.Empty(t, store.feeds)
}

func TestRSSFeedsUnavailableWithoutStore(t *testing.T) {
	h := NewRSSFeedsHandler(nil, nil)
	ctx := rssRequest("", 0)
	h.listItems(ctx)
	require.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())
}
