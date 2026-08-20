export type AdminUser = {
  id: string;
  email: string;
  full_name?: string;
  role: string;
};

export type ScrapeJobProgress = {
  cells_total: number;
  cells_pending: number;
  cells_completed: number;
  cells_subdivided: number;
  cells_failed: number;
  cells_saturated: number;
  candidates_total: number;
  candidates_pending: number;
  candidates_imported: number;
  candidates_duplicate: number;
  candidates_failed: number;
};

export type ScrapeJob = {
  id: string;
  city: string;
  city_key: string;
  niche: string;
  status: string;
  cycle_number: number;
  max_requests_per_window: number;
  requests_used_window: number;
  requests_used_total: number;
  window_started_at?: string;
  resume_at?: string;
  waiting_reason?: string;
  last_error?: string;
  created_at: string;
  updated_at: string;
  progress: ScrapeJobProgress;
};

export type Restaurant = {
  id: string;
  name: string;
  email?: string;
  phone?: string;
  address?: string;
  status: string;
  is_contacted?: boolean;
  shown_interest?: boolean;
  email_sent?: boolean;
  email_send_count?: number;
  last_email_sent_at?: string;
  created_at?: string;
  updated_at?: string;
};

export type SharedEmailRestaurant = {
  id: string;
  name: string;
  status: string;
};

export type SharedEmailGroup = {
  email: string;
  restaurant_count: number;
  blocked_for_outreach: boolean;
  restaurants: SharedEmailRestaurant[];
};

export type SharedEmailGroupListResponse = {
  groups: SharedEmailGroup[];
  total: number;
};

export type BulkSendStatus = {
  pending_eligible_count: number;
  due_followup_count?: number;
  new_recipient_count?: number;
  paused_recipient_count?: number;
  completed_recipient_count?: number;
  sent_counts?: {
    total: number;
    phase_1: number;
    phase_2: number;
    phase_3: number;
    other: number;
  };
  max_sends: number;
  next_available_at?: string;
  active_job?: {
    job_id: string;
    status: string;
  };
  last_completed_job?: {
    job_id: string;
    status: string;
    summary?: {
      attempted: number;
      sent: number;
      failed: number;
      skipped: number;
      stopped_reason?: string;
    };
  };
  email_job: {
    enabled: boolean;
    enabled_at?: string;
    enabled_by?: string;
    updated_at?: string;
  };
  send_schedule: OutreachSendSchedule;
};

export type OutreachDeliveryOutcomeCounts = {
  total: number;
  sent: number;
  failed: number;
  unknown: number;
  skipped: number;
  sending: number;
};

export type DailyOutreachDeliverySender = {
  account_id: string;
  account_key: string;
  sender_email: string;
  counts: OutreachDeliveryOutcomeCounts;
};

export type DailyOutreachDelivery = {
  id: string;
  restaurant_id: string;
  restaurant_name: string;
  recipient_email: string;
  account_id: string;
  account_key: string;
  sender_email: string;
  campaign_step: number;
  status: "sending" | "sent" | "skipped" | "failed" | "unknown";
  outcome: string;
  error_code?: string;
  subject?: string;
  provider_message_id?: string;
  attempted_at: string;
  outcome_at?: string;
  sent_at?: string;
};

export type DailyOutreachDeliveryList = {
  date: string;
  timezone: "Australia/Sydney";
  summary: OutreachDeliveryOutcomeCounts;
  senders: DailyOutreachDeliverySender[];
  deliveries: DailyOutreachDelivery[];
  total: number;
  limit: number;
  offset: number;
};

export type OutreachSendSchedule = {
  timezone: "Australia/Sydney";
  start_time: string;
  end_time: string;
  updated_by?: string;
  updated_at?: string;
};

export type EmailAccountHealth = {
  account_key: string;
  provider: string;
  provider_identity: string;
  from_email: string;
  enabled: boolean;
  status: string;
  last_checked_at?: string;
  next_check_at?: string;
  provider_message_id?: string;
  last_error?: string;
};

export type EmailAccountHealthResponse = {
  enabled: boolean;
  recipient: string;
  interval_hours: number;
  accounts: EmailAccountHealth[];
};

export type DemoSite = {
  id: string;
  restaurant_id?: string;
  status: string;
  slug?: string;
  published_at?: string;
  updated_at: string;
};

export type RestaurantProfile = {
  description?: string;
  opening_hours?: unknown;
  phone?: string;
  website?: string;
  address?: string;
  city?: string;
  state?: string;
  country?: string;
  latitude?: number | null;
  longitude?: number | null;
  google_place_id?: string;
  rating?: number | null;
  reviews_count?: number | null;
  price_level?: string | number | null;
  cuisines?: unknown;
  owners?: unknown;
  images?: unknown;
  dietary_options?: unknown;
  parking_info?: unknown;
  reservation_policy?: unknown;
  brand_tone?: unknown;
  scrape_status?: string;
  scrape_errors?: unknown;
};

export type ProfileReviewPreview = {
  restaurant_id: string;
  restaurant_name?: string;
  contact_email?: string;
  apollo_status?: string;
  apollo_email_found?: boolean;
  review_status?: string;
  reviewed_at?: string;
  reviewed_by?: string;
  restaurant_updated_at?: string;
  profile_updated_at?: string;
  profile?: RestaurantProfile;
};

export type SiteImage = {
  id?: string;
  url?: string;
  thumbnail_url?: string;
  image_type?: string;
  title?: string;
  source?: string;
};

export type SiteImages = {
  restaurant_id?: string;
  menu_images?: SiteImage[];
  gallery_images?: SiteImage[];
};

export type SiteMenuItem = {
  name?: string;
  category?: string;
  description?: string;
  price?: string;
  image_url?: string;
};

export type SiteReview = {
  reviewer?: string;
  review?: string;
  stars?: number | null;
  date?: string;
};

export type SiteContent = {
  restaurant_id?: string;
  place_id?: string;
  name?: string;
  menu_items?: SiteMenuItem[];
  menu_images?: SiteImage[];
  gallery_images?: SiteImage[];
  reviews?: SiteReview[];
  thumbnail?: string;
};

export type DemoTranscript = {
  id: string;
  role: string;
  content: string;
  occurred_at: string;
};

export type DemoSession = {
  id: string;
  demo_site_id?: string;
  restaurant_id: string;
  template_id: "1" | "2" | "3";
  started_at: string;
  last_seen_at: string;
  ended_at?: string;
  duration_seconds: number;
  transcript: DemoTranscript[];
};

export type GooglePhotoAttribution = {
  display_name: string;
  uri?: string;
  photo_uri?: string;
};

export type GooglePlacePhoto = {
  url: string;
  width_px?: number;
  height_px?: number;
  author_attributions: GooglePhotoAttribution[];
  google_maps_uri?: string;
  flag_content_uri?: string;
};

export type GooglePlacePhotos = {
  restaurant_id: string;
  google_place_id: string;
  photos: GooglePlacePhoto[];
  refreshed_at: string;
  urls_are_temporary: boolean;
};

export type RestaurantImage = {
  id: string;
  restaurant_id: string;
  url: string;
  thumbnail_url?: string;
  image_type?: string;
  title?: string;
  source?: string;
  sort_order?: number;
  created_at?: string;
  updated_at?: string;
  hidden_at?: string;
  hidden_by?: string;
};

export type RestaurantImages = {
  restaurant_id: string;
  menu_images: RestaurantImage[];
  gallery_images: RestaurantImage[];
  owned_media: RestaurantOwnedMedia[];
};

export type RestaurantOwnedMedia = {
  id: string;
  url: string;
  source_kind: "owner_upload" | "licensed";
  media_type: "exterior" | "interior" | "food" | "drink" | "logo" | "team" | "event" | "other";
  caption?: string;
  alt_text?: string;
  tags?: string[];
  quality_score?: number;
  hero_score?: number;
  width_px?: number;
  height_px?: number;
  orientation?: string;
  placement_role?: string;
  approval_status?: "draft" | "approved" | "rejected";
  rights_status?: "owner_granted" | "licensed";
  hidden_at?: string;
};

export type DemoLink = {
  demo_site_id: string;
  slug: string;
  status: string;
  expires_at?: string;
  preview_url?: string;
  created_at: string;
  updated_at: string;
};

export type GeneratedSiteTemplate = {
  id: string;
  name: string;
  url: string;
};

export type GeneratedSite = {
  restaurant_id: string;
  restaurant_name: string;
  google_place_id: string;
  site_index: number;
  templates: GeneratedSiteTemplate[];
  shareable: boolean;
};

export type OutreachSequenceStep = {
  id?: string;
  position: number;
  enabled: boolean;
  delay_hours: number;
  subject_template: string;
  body_text_template: string;
};

export type OutreachEmailSignature = {
  name: string;
  title: string;
  additional_details: string;
};

export type OutreachSequence = {
  id: string;
  name: string;
  version: number;
  status: "draft" | "active" | "archived" | string;
  is_active: boolean;
  approved_at?: string;
  approved_by?: string;
  created_at: string;
  updated_at: string;
  signature: OutreachEmailSignature;
  steps: OutreachSequenceStep[];
};

export type OutreachSequenceListResponse = {
  active_sequence_id?: string;
  sequences: OutreachSequence[];
};

export type OutreachSequencePreviewStep = {
  position: number;
  subject: string;
  body_text: string;
  url_count: number;
};

export type OutreachSequencePreview = {
  restaurant_id?: string;
  restaurant_name: string;
  owner_first_name?: string;
  greeting?: string;
  greeting01: string;
  facts_used: string[];
  signature: OutreachEmailSignature;
  steps: OutreachSequencePreviewStep[];
};

export type RestaurantGreetingPreview = {
  restaurant_id: string;
  restaurant_name: string;
  greeting: string;
  greeting01: string;
  facts_used: string[];
};

export type OutreachTemplateTestSendResponse = {
  recipient_email: string;
  sequence_id: string;
  restaurant_id?: string;
  restaurant_name: string;
  greeting01: string;
  facts_used: string[];
  items: {
    template: "sequence";
    step?: number;
    subject: string;
    provider_message_id?: string;
  }[];
};

export type OutreachRecipient = {
  restaurant_id: string;
  restaurant_name: string;
  email: string;
  email_record_count: number;
  lifecycle_status: string;
  consent_basis: string;
  current_step: number;
  next_step?: number | null;
  last_sent_at?: string;
  next_send_at?: string;
  completed_at?: string;
  campaign_status: string;
  email_send_count: number;
  eligible: boolean;
  hold_reason?: string;
};

export type OutreachRecipientListResponse = {
  recipients: OutreachRecipient[];
  total: number;
};

export type Member = {
  id?: string;
  user_id?: string;
  email?: string;
  role?: string;
  member_role?: string;
  full_name?: string;
  created_at?: string;
};

export type ConsultationCalendarSlot = {
  date: string;
  time: string;
  iso: string;
  is_available: boolean;
  booked: boolean;
  past: boolean;
  effective_available: boolean;
  on_grid: boolean;
};

export type ConsultationCalendarMonth = {
  month: string;
  revision: number;
  booked_call_count: number;
  timezone: string;
  slot_duration_minutes: number;
  business_hour_start: string;
  business_hour_end: string;
  slots: ConsultationCalendarSlot[];
};

export type EmailMessage = {
  id: string;
  restaurant_id?: string;
  campaign_id?: string;
  delivery_attempt_id?: string;
  reply_token?: string;
  direction: "outbound" | "inbound";
  from_email: string;
  to_email: string;
  reply_to?: string;
  subject: string;
  body_text: string;
  gmail_message_id?: string;
  gmail_thread_id?: string;
  rfc_message_id?: string;
  mailbox_key?: string;
  unmatched?: boolean;
  read_at?: string;
  received_at: string;
  created_at: string;
};

export type OutreachEmailAccount = {
  id?: string;
  account_key: string;
  mailbox_email: string;
  from_email: string;
  source: "environment" | "database";
  enabled: boolean;
  effective: boolean;
  editable: boolean;
  credentials_stored: boolean;
  overrides_environment?: boolean;
  database_fallback?: boolean;
  shadowed_by_environment?: boolean;
  created_at?: string;
  updated_at?: string;
};

export type OutreachEmailAccountListResponse = {
  accounts: OutreachEmailAccount[];
  encryption_ready: boolean;
};

export type InboxThread = {
  restaurant_id?: string;
  restaurant_name?: string;
  email?: string;
  mailbox_key: string;
  mailbox_email: string;
  unmatched: boolean;
  unread_count: number;
  from_email: string;
  to_email: string;
  subject: string;
  text_snippet: string;
  received_at: string;
  last_direction: string;
  last_snippet: string;
  last_at: string;
  last_message_id: string;
  reply_message_id: string;
};

export type InboxMailboxStatus = {
  mailbox_key: string;
  last_attempt_at?: string;
  last_success_at?: string;
  last_error?: string;
};

export type InboxListResponse = {
  threads: InboxThread[];
  mailboxes: InboxMailboxStatus[];
  total: number;
};
