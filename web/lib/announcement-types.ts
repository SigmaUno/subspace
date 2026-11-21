export interface Announcement {
  id: string;
  text: string;
  brief_id: string;
  expires_at: string | null;
  created_at: string;
}

export interface AnnouncementList {
  announcements: Announcement[] | null;
}

export interface Published {
  announcement: Announcement;
  delivered: number;
}
