package configstore

import (
	"context"
	"errors"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BifrostAnnouncementsFeedSlug is the builtin feed enabled on every new deployment.
const BifrostAnnouncementsFeedSlug = "bifrost"

// BuiltinRSSFeeds is the catalog seeded into rss_feeds. Seeding inserts missing
// slugs only, so an admin's enabled/disabled choice survives restarts; URL and
// name changes made here are pushed to existing rows by SeedBuiltinRSSFeeds.
var BuiltinRSSFeeds = []tables.TableRSSFeed{
	{Slug: BifrostAnnouncementsFeedSlug, Name: "Bifrost Announcements", URL: "https://www.getbifrost.ai/announcements/rss.xml", Icon: "bifrost", Enabled: true},
	{Slug: "openai", Name: "OpenAI News", URL: "https://openai.com/news/rss.xml", Icon: "openai"},
	// Anthropic publishes no feed; the news page itself is read by rssfeed.ParseHTMLPage.
	{Slug: "anthropic", Name: "Anthropic News", URL: "https://www.anthropic.com/news", Icon: "anthropic"},
	{Slug: "gemini", Name: "Gemini News", URL: "https://blog.google/products/gemini/rss/", Icon: "gemini"},
	{Slug: "cursor", Name: "Cursor Changelog", URL: "https://cursor.com/changelog/rss.xml", Icon: "cursor"},
}

// ErrRSSFeedNotFound is returned when an id does not match a feed.
var ErrRSSFeedNotFound = errors.New("rss feed not found")

// RSSFeedStore is an optional interface, like NotificationStore, so test
// doubles of ConfigStore need not implement it.
type RSSFeedStore interface {
	ListRSSFeeds(ctx context.Context) ([]tables.TableRSSFeed, error)
	GetRSSFeed(ctx context.Context, id uint) (*tables.TableRSSFeed, error)
	CreateRSSFeed(ctx context.Context, feed *tables.TableRSSFeed) error
	UpdateRSSFeed(ctx context.Context, feed *tables.TableRSSFeed) error
	DeleteRSSFeed(ctx context.Context, id uint) error
	SeedBuiltinRSSFeeds(ctx context.Context) error
}

func (s *RDBConfigStore) ListRSSFeeds(ctx context.Context) ([]tables.TableRSSFeed, error) {
	var feeds []tables.TableRSSFeed
	err := s.DB().WithContext(ctx).Order("source ASC").Order("id ASC").Find(&feeds).Error
	return feeds, err
}

func (s *RDBConfigStore) GetRSSFeed(ctx context.Context, id uint) (*tables.TableRSSFeed, error) {
	var feed tables.TableRSSFeed
	if err := s.DB().WithContext(ctx).First(&feed, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRSSFeedNotFound
		}
		return nil, err
	}
	return &feed, nil
}

func (s *RDBConfigStore) CreateRSSFeed(ctx context.Context, feed *tables.TableRSSFeed) error {
	if err := s.DB().WithContext(ctx).Create(feed).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) || isUniqueConstraintError(err) {
			return ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (s *RDBConfigStore) UpdateRSSFeed(ctx context.Context, feed *tables.TableRSSFeed) error {
	result := s.DB().WithContext(ctx).Model(&tables.TableRSSFeed{}).Where("id = ?", feed.ID).
		Select("name", "url", "icon_url", "enabled", "updated_at").
		Updates(map[string]any{"name": feed.Name, "url": feed.URL, "icon_url": feed.IconURL, "enabled": feed.Enabled, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrRSSFeedNotFound
	}
	return nil
}

func (s *RDBConfigStore) DeleteRSSFeed(ctx context.Context, id uint) error {
	result := s.DB().WithContext(ctx).Delete(&tables.TableRSSFeed{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrRSSFeedNotFound
	}
	return nil
}

// SeedBuiltinRSSFeeds runs the package-level seed against this store; called
// on boot so catalog additions reach existing deployments without a migration.
func (s *RDBConfigStore) SeedBuiltinRSSFeeds(ctx context.Context) error {
	return SeedBuiltinRSSFeeds(ctx, s.DB())
}

// SeedBuiltinRSSFeeds inserts any missing builtin feed and refreshes the name,
// URL and icon of existing builtin rows, leaving Enabled untouched so an
// admin's choice is never reverted. Safe to run on every boot.
func SeedBuiltinRSSFeeds(ctx context.Context, db *gorm.DB) error {
	now := time.Now().UTC()
	for _, builtin := range BuiltinRSSFeeds {
		row := builtin
		row.Source = tables.RSSFeedSourceBuiltin
		row.CreatedAt = now
		row.UpdatedAt = now
		err := db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "slug"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "url", "icon", "source"}),
		}).Create(&row).Error
		if err != nil {
			return err
		}
	}
	return nil
}
