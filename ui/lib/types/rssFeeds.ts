export type RSSFeedSource = "builtin" | "custom";

export interface RSSFeed {
	id: number;
	slug: string;
	name: string;
	url: string;
	icon?: string;
	icon_url?: string;
	source: RSSFeedSource;
	enabled: boolean;
	created_at: string;
	updated_at: string;
}

export interface RSSFeedListResponse {
	feeds: RSSFeed[];
}

export interface RSSItem {
	id: string;
	feed_id: number;
	feed_slug: string;
	feed_name: string;
	feed_icon?: string;
	feed_icon_url?: string;
	feed_url: string;
	title: string;
	link: string;
	summary?: string;
	published_at: string;
}

export interface RSSFeedError {
	feed_id: number;
	feed_name: string;
	message: string;
}

export interface RSSItemsResponse {
	items: RSSItem[];
	errors: RSSFeedError[];
}

export interface CreateRSSFeedRequest {
	name: string;
	url: string;
	icon_url?: string;
	enabled?: boolean;
}

export interface UpdateRSSFeedRequest {
	id: number;
	name?: string;
	url?: string;
	icon_url?: string;
	enabled?: boolean;
}