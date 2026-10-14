package rssfeed

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newFeedServer(t *testing.T, hits *atomic.Int32, body func() (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		status, payload := body()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestManagerCachesAndCoalesces(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := newFeedServer(t, &hits, func() (int, string) {
		<-release
		return http.StatusOK, rss2Fixture
	})
	m := NewManagerWithClient(srv.Client())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Get(context.Background(), srv.URL); err != nil {
				t.Errorf("Get: %v", err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := hits.Load(); got != 1 {
		t.Fatalf("concurrent Gets should share one fetch, got %d", got)
	}

	if _, err := m.Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("cached Get: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("fresh entry should be served from cache, got %d fetches", got)
	}

	now := time.Now()
	m.now = func() time.Time { return now.Add(DefaultTTL + time.Second) }
	if _, err := m.Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("expired Get: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("expired entry should refetch, got %d fetches", got)
	}

	m.Forget(srv.URL)
	m.now = time.Now
	if _, err := m.Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("Get after Forget: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("Forget should drop the entry, got %d fetches", got)
	}
}

func TestManagerFetchErrors(t *testing.T) {
	var hits atomic.Int32
	status, payload := http.StatusOK, ""
	srv := newFeedServer(t, &hits, func() (int, string) { return status, payload })
	m := NewManagerWithClient(srv.Client())

	status, payload = http.StatusNotFound, "missing"
	if _, err := m.Fetch(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: expected status error, got %v", err)
	}

	status, payload = http.StatusOK, "<html><body>landing page</body></html>"
	if _, err := m.Fetch(context.Background(), srv.URL); !errors.Is(err, ErrNotAFeed) {
		t.Errorf("html: expected ErrNotAFeed, got %v", err)
	}

	status, payload = http.StatusOK, "<rss><channel>"+strings.Repeat("x", maxFeedBytes)+"</channel></rss>"
	if _, err := m.Fetch(context.Background(), srv.URL); !errors.Is(err, ErrFeedTooLarge) {
		t.Errorf("oversize: expected ErrFeedTooLarge, got %v", err)
	}
}

func TestManagerCachesErrorsBriefly(t *testing.T) {
	var hits atomic.Int32
	srv := newFeedServer(t, &hits, func() (int, string) { return http.StatusInternalServerError, "" })
	m := NewManagerWithClient(srv.Client())
	if _, err := m.Get(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error")
	}
	if _, err := m.Get(context.Background(), srv.URL); err == nil {
		t.Fatal("expected cached error")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("error should be cached, got %d fetches", got)
	}
	now := time.Now()
	m.now = func() time.Time { return now.Add(errorTTL + time.Second) }
	_, _ = m.Get(context.Background(), srv.URL)
	if got := hits.Load(); got != 2 {
		t.Fatalf("error entry should expire after errorTTL, got %d fetches", got)
	}
}

func TestNewManagerRefusesPrivateTargets(t *testing.T) {
	m := NewManager()
	for _, target := range []string{"http://127.0.0.1:1/feed", "http://169.254.169.254/latest/meta-data", "file:///etc/passwd"} {
		// Loopback can clear URL validation (it permits local development
		// hosts) but must still be refused by the dial-time guard.
		_, err := m.Fetch(context.Background(), target)
		if err == nil || !(strings.HasPrefix(err.Error(), "invalid feed url:") || strings.Contains(err.Error(), "non-public address")) {
			t.Errorf("%s: expected the target to be refused, got %v", target, err)
		}
	}
}
