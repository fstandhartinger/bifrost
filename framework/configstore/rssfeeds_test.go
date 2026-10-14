package configstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func TestMigrationAddRSSFeedsTableSeedsBuiltins(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, migrationAddRSSFeedsTable(ctx, store.DB(), testMigrationLogger))

	feeds, err := store.ListRSSFeeds(ctx)
	require.NoError(t, err)
	require.Len(t, feeds, len(BuiltinRSSFeeds))
	bySlug := map[string]tables.TableRSSFeed{}
	for _, feed := range feeds {
		require.Equal(t, tables.RSSFeedSourceBuiltin, feed.Source)
		bySlug[feed.Slug] = feed
	}
	for _, slug := range []string{"bifrost", "openai", "anthropic", "gemini", "cursor"} {
		require.Contains(t, bySlug, slug)
		require.NotEmpty(t, bySlug[slug].Icon, "builtin %s must carry an icon key", slug)
	}
	require.True(t, bySlug[BifrostAnnouncementsFeedSlug].Enabled, "bifrost announcements are on by default")
	require.False(t, bySlug["openai"].Enabled, "vendor feeds are opt-in")
}

func TestSeedBuiltinRSSFeedsPreservesEnabledChoice(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, migrationAddRSSFeedsTable(ctx, store.DB(), testMigrationLogger))

	feeds, err := store.ListRSSFeeds(ctx)
	require.NoError(t, err)
	var bifrostFeed, openaiFeed tables.TableRSSFeed
	for _, feed := range feeds {
		switch feed.Slug {
		case BifrostAnnouncementsFeedSlug:
			bifrostFeed = feed
		case "openai":
			openaiFeed = feed
		}
	}
	bifrostFeed.Enabled = false
	openaiFeed.Enabled = true
	// Simulate a stale URL from an older catalog, which reseeding must refresh.
	openaiFeed.URL = "https://stale.example.com/rss"
	require.NoError(t, store.UpdateRSSFeed(ctx, &bifrostFeed))
	require.NoError(t, store.UpdateRSSFeed(ctx, &openaiFeed))

	require.NoError(t, store.SeedBuiltinRSSFeeds(ctx))
	require.NoError(t, store.SeedBuiltinRSSFeeds(ctx))

	feeds, err = store.ListRSSFeeds(ctx)
	require.NoError(t, err)
	require.Len(t, feeds, len(BuiltinRSSFeeds), "reseeding must not duplicate rows")
	for _, feed := range feeds {
		switch feed.Slug {
		case BifrostAnnouncementsFeedSlug:
			require.False(t, feed.Enabled, "admin's disable must survive reseed")
		case "openai":
			require.True(t, feed.Enabled, "admin's enable must survive reseed")
			require.Equal(t, "https://openai.com/news/rss.xml", feed.URL, "catalog URL is authoritative for builtins")
		}
	}
}

func TestRSSFeedStoreCustomLifecycle(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.DB().AutoMigrate(&tables.TableRSSFeed{}))
	now := time.Now().UTC()

	feed := &tables.TableRSSFeed{Slug: "custom-releases", Name: "Releases", URL: "https://example.com/releases.atom", Source: tables.RSSFeedSourceCustom, Enabled: true, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, store.CreateRSSFeed(ctx, feed))
	require.NotZero(t, feed.ID)

	duplicate := *feed
	duplicate.ID = 0
	require.ErrorIs(t, store.CreateRSSFeed(ctx, &duplicate), ErrAlreadyExists)

	feed.Name = "Renamed"
	feed.Enabled = false
	feed.IconURL = "https://example.com/logo.png"
	require.NoError(t, store.UpdateRSSFeed(ctx, feed))
	got, err := store.GetRSSFeed(ctx, feed.ID)
	require.NoError(t, err)
	require.Equal(t, "Renamed", got.Name)
	require.False(t, got.Enabled)
	require.Equal(t, "https://example.com/logo.png", got.IconURL)

	require.NoError(t, store.DeleteRSSFeed(ctx, feed.ID))
	_, err = store.GetRSSFeed(ctx, feed.ID)
	require.ErrorIs(t, err, ErrRSSFeedNotFound)
	require.ErrorIs(t, store.DeleteRSSFeed(ctx, feed.ID), ErrRSSFeedNotFound)
	require.ErrorIs(t, store.UpdateRSSFeed(ctx, feed), ErrRSSFeedNotFound)
}

// rssFeedBeforeIconURL is rss_feeds as the first add_rss_feeds_table shipped
// it, before the icon_url column. A database migrated by that build has the
// migration ID recorded, so AutoMigrate never runs again to add the column.
type rssFeedBeforeIconURL struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	Slug      string    `gorm:"type:varchar(64);not null;uniqueIndex:idx_rss_feeds_slug"`
	Name      string    `gorm:"type:varchar(120);not null"`
	URL       string    `gorm:"type:varchar(2048);not null"`
	Icon      string    `gorm:"type:varchar(64)"`
	Source    string    `gorm:"type:varchar(16);not null;default:'custom'"`
	Enabled   bool      `gorm:"not null;default:false"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

func (rssFeedBeforeIconURL) TableName() string { return "rss_feeds" }

func TestMigrationAddRSSFeedsIconURLColumnRepairsEarlyTables(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.DB().AutoMigrate(&rssFeedBeforeIconURL{}))
	now := time.Now().UTC()
	require.NoError(t, store.DB().Create(&rssFeedBeforeIconURL{Slug: "gemini", Name: "Gemini News", URL: "https://blog.google/products/gemini/rss/", Icon: "gemini", Source: tables.RSSFeedSourceBuiltin, CreatedAt: now, UpdatedAt: now}).Error)

	feed, err := store.GetRSSFeed(ctx, 1)
	require.NoError(t, err)
	feed.Enabled = true
	require.Error(t, store.UpdateRSSFeed(ctx, feed), "without the column every toggle fails")

	require.NoError(t, migrationAddRSSFeedsIconURLColumn(ctx, store.DB(), testMigrationLogger))
	require.NoError(t, migrationAddRSSFeedsIconURLColumn(ctx, store.DB(), testMigrationLogger), "running it again is a no-op")

	require.NoError(t, store.UpdateRSSFeed(ctx, feed))
	got, err := store.GetRSSFeed(ctx, 1)
	require.NoError(t, err)
	require.True(t, got.Enabled)
	require.NoError(t, store.SeedBuiltinRSSFeeds(ctx), "the boot-time reseed writes icon_url too")
}
