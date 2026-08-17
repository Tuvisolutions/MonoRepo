package outreach

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const messageSelectColumns = `
	id, restaurant_id, campaign_id, delivery_attempt_id, reply_token,
	direction, from_email, to_email, reply_to, subject, body_text,
	gmail_message_id, gmail_thread_id, rfc_message_id, mailbox_key,
	unmatched, read_at, received_at, created_at`

func (repo *Postgres) InsertMessage(ctx context.Context, message Message) (Message, error) {
	if repo.pool == nil {
		return Message{}, fmt.Errorf("database pool is not configured")
	}
	direction := strings.TrimSpace(message.Direction)
	if direction != MessageDirectionOutbound && direction != MessageDirectionInbound {
		return Message{}, fmt.Errorf("email message direction is invalid")
	}
	const query = `
		INSERT INTO email_messages (
			restaurant_id, campaign_id, delivery_attempt_id, reply_token,
			direction, from_email, to_email, reply_to, subject, body_text,
			gmail_message_id, gmail_thread_id, rfc_message_id, mailbox_key,
			unmatched, read_at, received_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, COALESCE($17, now()))
		ON CONFLICT DO NOTHING
		RETURNING` + messageSelectColumns
	var receivedAt any
	if !message.ReceivedAt.IsZero() {
		receivedAt = message.ReceivedAt
	}
	record, err := scanMessage(repo.pool.QueryRow(
		ctx,
		query,
		message.RestaurantID,
		message.CampaignID,
		message.DeliveryAttemptID,
		message.ReplyToken,
		direction,
		strings.TrimSpace(message.FromEmail),
		strings.TrimSpace(message.ToEmail),
		strings.TrimSpace(message.ReplyTo),
		strings.TrimSpace(message.Subject),
		strings.TrimSpace(message.BodyText),
		strings.TrimSpace(message.GmailMessageID),
		strings.TrimSpace(message.GmailThreadID),
		strings.TrimSpace(message.RFCMessageID),
		strings.TrimSpace(message.MailboxKey),
		message.Unmatched,
		message.ReadAt,
		receivedAt,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		if strings.TrimSpace(message.GmailMessageID) != "" {
			existing, lookupErr := repo.GetMessageByGmailID(ctx, message.MailboxKey, message.GmailMessageID)
			if lookupErr == nil {
				return existing, nil
			}
			if !errors.Is(lookupErr, pgx.ErrNoRows) {
				return Message{}, lookupErr
			}
		}
		if message.DeliveryAttemptID != nil {
			existing, lookupErr := scanMessage(repo.pool.QueryRow(
				ctx,
				`SELECT`+messageSelectColumns+` FROM email_messages WHERE delivery_attempt_id = $1 AND direction = $2`,
				message.DeliveryAttemptID,
				MessageDirectionOutbound,
			))
			if lookupErr == nil {
				return existing, nil
			}
			if !errors.Is(lookupErr, pgx.ErrNoRows) {
				return Message{}, lookupErr
			}
		}
		return Message{}, fmt.Errorf("insert email message conflicted without a stored row")
	}
	if err != nil {
		return Message{}, fmt.Errorf("insert email message: %w", err)
	}
	return record, nil
}

func (repo *Postgres) GetMessageByID(ctx context.Context, id uuid.UUID) (Message, error) {
	return scanMessage(repo.pool.QueryRow(ctx, `SELECT`+messageSelectColumns+` FROM email_messages WHERE id = $1`, id))
}

func (repo *Postgres) GetMessageByGmailID(ctx context.Context, mailboxKey, gmailMessageID string) (Message, error) {
	mailboxKey = strings.TrimSpace(mailboxKey)
	gmailMessageID = strings.TrimSpace(gmailMessageID)
	if mailboxKey == "" || gmailMessageID == "" {
		return Message{}, pgx.ErrNoRows
	}
	return scanMessage(repo.pool.QueryRow(
		ctx,
		`SELECT`+messageSelectColumns+` FROM email_messages WHERE mailbox_key = $1 AND gmail_message_id = $2`,
		mailboxKey,
		gmailMessageID,
	))
}

func (repo *Postgres) GetOutboundByReplyToken(ctx context.Context, token uuid.UUID) (Message, error) {
	if token == uuid.Nil {
		return Message{}, pgx.ErrNoRows
	}
	return scanMessage(repo.pool.QueryRow(
		ctx,
		`SELECT`+messageSelectColumns+`
		 FROM email_messages
		 WHERE direction = $1 AND reply_token = $2
		 ORDER BY created_at DESC
		 LIMIT 1`,
		MessageDirectionOutbound,
		token,
	))
}

func (repo *Postgres) GetOutboundByRFCMessageID(ctx context.Context, rfcMessageID string) (Message, error) {
	rfcMessageID = strings.TrimSpace(rfcMessageID)
	if rfcMessageID == "" {
		return Message{}, pgx.ErrNoRows
	}
	return scanMessage(repo.pool.QueryRow(
		ctx,
		`SELECT`+messageSelectColumns+`
		 FROM email_messages
		 WHERE direction = $1 AND lower(rfc_message_id) = lower($2)
		 ORDER BY created_at DESC
		 LIMIT 1`,
		MessageDirectionOutbound,
		rfcMessageID,
	))
}

func (repo *Postgres) GetOutboundByThreadID(ctx context.Context, threadID string) (Message, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return Message{}, pgx.ErrNoRows
	}
	return scanMessage(repo.pool.QueryRow(
		ctx,
		`SELECT`+messageSelectColumns+`
		 FROM email_messages
		 WHERE direction = $1 AND gmail_thread_id = $2
		 ORDER BY created_at DESC
		 LIMIT 1`,
		MessageDirectionOutbound,
		threadID,
	))
}

func (repo *Postgres) FindRestaurantIDByEmail(ctx context.Context, email string) (uuid.UUID, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return uuid.Nil, pgx.ErrNoRows
	}
	var id uuid.UUID
	err := repo.pool.QueryRow(
		ctx,
		`SELECT id
		 FROM restaurants
		 WHERE lower(trim(email)) = $1
		    OR lower(trim(coalesce(last_email_recipient, ''))) = $1
		 ORDER BY updated_at DESC
		 LIMIT 1`,
		email,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

const inboxThreadsCTE = `
	WITH messages AS (
	  SELECT m.id, m.restaurant_id, m.direction, m.from_email, m.to_email,
	         m.subject, m.body_text, m.read_at, m.received_at, m.created_at, m.mailbox_key,
	         COALESCE(NULLIF(m.gmail_thread_id, ''), COALESCE(m.restaurant_id::text, m.id::text)) AS conversation_key,
	         r.name AS restaurant_name,
	         CASE WHEN m.direction = 'inbound' THEN m.from_email ELSE m.to_email END AS email
	  FROM email_messages m
	  LEFT JOIN restaurants r ON r.id = m.restaurant_id
	  WHERE m.received_at >= now() - interval '10 days'
	     OR $3 <> ''
	), thread_rollup AS (
	  SELECT mailbox_key,
	         conversation_key,
	         ((array_agg(restaurant_id ORDER BY received_at DESC, created_at DESC, id DESC)
	           FILTER (WHERE restaurant_id IS NOT NULL))[1]) AS restaurant_id,
	         COALESCE(max(restaurant_name), '') AS restaurant_name,
	         COALESCE((array_agg(email ORDER BY received_at DESC, created_at DESC, id DESC))[1], '') AS email,
	         bool_and(restaurant_id IS NULL) AS unmatched,
	         count(*) FILTER (WHERE direction = 'inbound' AND read_at IS NULL)::int AS unread_count,
	         (array_agg(direction ORDER BY received_at DESC, created_at DESC, id DESC))[1] AS last_direction,
	         (array_agg(left(btrim(regexp_replace(body_text, '\s+', ' ', 'g')), 180)
	           ORDER BY received_at DESC, created_at DESC, id DESC))[1] AS last_snippet,
	         max(received_at) AS last_at,
	         (array_agg(id ORDER BY received_at DESC, created_at DESC, id DESC))[1] AS last_message_id,
	         (array_agg(id ORDER BY received_at DESC, created_at DESC, id DESC)
	           FILTER (WHERE direction = 'inbound'))[1] AS reply_message_id
	  FROM messages
	  GROUP BY mailbox_key, conversation_key
	  HAVING bool_or(direction = 'inbound')
	), threads AS (
	  SELECT rollup.restaurant_id,
	         rollup.restaurant_name,
	         rollup.email,
	         rollup.mailbox_key,
	         inbound.to_email AS mailbox_email,
	         rollup.unmatched,
	         rollup.unread_count,
	         inbound.subject,
	         left(btrim(regexp_replace(inbound.body_text, '\s+', ' ', 'g')), 180) AS text_snippet,
	         inbound.from_email,
	         inbound.to_email,
	         inbound.received_at,
	         rollup.last_direction,
	         rollup.last_snippet,
	         rollup.last_at,
	         rollup.last_message_id,
	         rollup.reply_message_id
	  FROM thread_rollup rollup
	  JOIN messages inbound ON inbound.id = rollup.reply_message_id
	)`

const inboxThreadsLatestFirstOrder = `received_at DESC, reply_message_id DESC`

const inboxThreadsListQuery = inboxThreadsCTE + `
		SELECT restaurant_id, restaurant_name, email, mailbox_key, mailbox_email, unmatched, unread_count,
		       subject, text_snippet, from_email, to_email, received_at,
		       last_direction, last_snippet, last_at, last_message_id, reply_message_id
		FROM threads
		WHERE ($1 = '' OR mailbox_key = $1)
		  AND (NOT $2 OR unread_count > 0)
		  AND (
		    $3 = ''
		    OR restaurant_name ILIKE '%' || $3 || '%'
		    OR email ILIKE '%' || $3 || '%'
		    OR subject ILIKE '%' || $3 || '%'
		    OR text_snippet ILIKE '%' || $3 || '%'
		    OR from_email ILIKE '%' || $3 || '%'
		    OR to_email ILIKE '%' || $3 || '%'
		    OR mailbox_key ILIKE '%' || $3 || '%'
		  )
		ORDER BY ` + inboxThreadsLatestFirstOrder + `
		LIMIT $4 OFFSET $5`

func (repo *Postgres) ListInbox(ctx context.Context, unreadOnly bool, mailboxKey, search string, limit, offset int) (InboxList, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	mailboxKey = strings.TrimSpace(mailboxKey)
	search = strings.TrimSpace(search)
	countQuery := inboxThreadsCTE + `
		SELECT count(*)
		FROM threads
		WHERE ($1 = '' OR mailbox_key = $1)
		  AND (NOT $2 OR unread_count > 0)
		  AND (
		    $3 = ''
		    OR restaurant_name ILIKE '%' || $3 || '%'
		    OR email ILIKE '%' || $3 || '%'
		    OR subject ILIKE '%' || $3 || '%'
		    OR text_snippet ILIKE '%' || $3 || '%'
		    OR from_email ILIKE '%' || $3 || '%'
		    OR to_email ILIKE '%' || $3 || '%'
		    OR mailbox_key ILIKE '%' || $3 || '%'
		  )`
	var total int
	if err := repo.pool.QueryRow(ctx, countQuery, mailboxKey, unreadOnly, search).Scan(&total); err != nil {
		return InboxList{}, fmt.Errorf("count inbox threads: %w", err)
	}

	rows, err := repo.pool.Query(ctx, inboxThreadsListQuery, mailboxKey, unreadOnly, search, limit, offset)
	if err != nil {
		return InboxList{}, fmt.Errorf("list inbox threads: %w", err)
	}
	defer rows.Close()

	threads := make([]InboxThread, 0)
	for rows.Next() {
		thread, err := scanInboxThread(rows)
		if err != nil {
			return InboxList{}, fmt.Errorf("scan inbox thread: %w", err)
		}
		threads = append(threads, thread)
	}
	if err := rows.Err(); err != nil {
		return InboxList{}, err
	}
	mailboxes, err := repo.ListInboundMailboxStatuses(ctx)
	if err != nil {
		return InboxList{}, err
	}
	return InboxList{Threads: threads, Mailboxes: mailboxes, Total: total}, nil
}

func scanInboxThread(row messageScanner) (InboxThread, error) {
	var thread InboxThread
	var restaurantName *string
	if err := row.Scan(
		&thread.RestaurantID,
		&restaurantName,
		&thread.Email,
		&thread.MailboxKey,
		&thread.MailboxEmail,
		&thread.Unmatched,
		&thread.UnreadCount,
		&thread.Subject,
		&thread.TextSnippet,
		&thread.FromEmail,
		&thread.ToEmail,
		&thread.ReceivedAt,
		&thread.LastDirection,
		&thread.LastSnippet,
		&thread.LastAt,
		&thread.LastMessageID,
		&thread.ReplyMessageID,
	); err != nil {
		return InboxThread{}, err
	}
	if restaurantName != nil {
		thread.RestaurantName = *restaurantName
	}
	return thread, nil
}

func (repo *Postgres) ListInboundMailboxStatuses(ctx context.Context) ([]InboxMailboxStatus, error) {
	rows, err := repo.pool.Query(
		ctx,
		`SELECT mailbox_key, last_attempt_at, last_success_at, last_error
		 FROM outreach_inbound_sync
		 ORDER BY mailbox_key`,
	)
	if err != nil {
		return nil, fmt.Errorf("list inbound mailbox statuses: %w", err)
	}
	defer rows.Close()
	statuses := make([]InboxMailboxStatus, 0)
	for rows.Next() {
		var status InboxMailboxStatus
		if err := rows.Scan(&status.MailboxKey, &status.LastAttemptAt, &status.LastSuccessAt, &status.LastError); err != nil {
			return nil, fmt.Errorf("scan inbound mailbox status: %w", err)
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func (repo *Postgres) ListRestaurantMessages(ctx context.Context, restaurantID uuid.UUID) ([]Message, error) {
	rows, err := repo.pool.Query(
		ctx,
		`SELECT`+messageSelectColumns+`
		 FROM email_messages
		 WHERE restaurant_id = $1
		 ORDER BY created_at DESC`,
		restaurantID,
	)
	if err != nil {
		return nil, fmt.Errorf("list restaurant email messages: %w", err)
	}
	defer rows.Close()
	messages := make([]Message, 0)
	for rows.Next() {
		record, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, record)
	}
	return messages, rows.Err()
}

func (repo *Postgres) MarkMessageRead(ctx context.Context, id uuid.UUID) (Message, error) {
	record, err := scanMessage(repo.pool.QueryRow(
		ctx,
		`UPDATE email_messages
		 SET read_at = COALESCE(read_at, now())
		 WHERE id = $1
		 RETURNING`+messageSelectColumns,
		id,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, err
	}
	if err != nil {
		return Message{}, fmt.Errorf("mark email message read: %w", err)
	}
	return record, nil
}

func (repo *Postgres) MarkRestaurantInboundRead(ctx context.Context, restaurantID uuid.UUID) error {
	_, err := repo.pool.Exec(
		ctx,
		`UPDATE email_messages
		 SET read_at = now()
		 WHERE restaurant_id = $1 AND direction = $2 AND read_at IS NULL`,
		restaurantID,
		MessageDirectionInbound,
	)
	if err != nil {
		return fmt.Errorf("mark restaurant inbound messages read: %w", err)
	}
	return nil
}

func (repo *Postgres) GetInboundSync(ctx context.Context, mailboxKey string) (string, error) {
	var historyID string
	err := repo.pool.QueryRow(
		ctx,
		`SELECT history_id FROM outreach_inbound_sync WHERE mailbox_key = $1`,
		strings.TrimSpace(mailboxKey),
	).Scan(&historyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get inbound sync cursor: %w", err)
	}
	return historyID, nil
}

func (repo *Postgres) SetInboundSync(ctx context.Context, mailboxKey, historyID string) error {
	_, err := repo.pool.Exec(
		ctx,
		`INSERT INTO outreach_inbound_sync (mailbox_key, history_id, updated_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (mailbox_key) DO UPDATE
		 SET history_id = EXCLUDED.history_id, updated_at = now()`,
		strings.TrimSpace(mailboxKey),
		strings.TrimSpace(historyID),
	)
	if err != nil {
		return fmt.Errorf("set inbound sync cursor: %w", err)
	}
	return nil
}

func (repo *Postgres) MarkInboundPollAttempt(ctx context.Context, mailboxKey string) error {
	_, err := repo.pool.Exec(
		ctx,
		`INSERT INTO outreach_inbound_sync (mailbox_key, history_id, last_attempt_at, updated_at)
		 VALUES ($1, '', now(), now())
		 ON CONFLICT (mailbox_key) DO UPDATE
		 SET last_attempt_at = now(), updated_at = now()`,
		strings.TrimSpace(mailboxKey),
	)
	if err != nil {
		return fmt.Errorf("mark inbound mailbox poll attempt: %w", err)
	}
	return nil
}

func (repo *Postgres) RecordInboundPollResult(ctx context.Context, mailboxKey string, pollErr error) error {
	lastError := ""
	if pollErr != nil {
		lastError = snippet(pollErr.Error(), 500)
	}
	_, err := repo.pool.Exec(
		ctx,
		`INSERT INTO outreach_inbound_sync (
		   mailbox_key, history_id, last_attempt_at, last_success_at, last_error, updated_at
		 ) VALUES ($1, '', now(), CASE WHEN $2 = '' THEN now() ELSE NULL END, $2, now())
		 ON CONFLICT (mailbox_key) DO UPDATE
		 SET last_success_at = CASE WHEN EXCLUDED.last_error = '' THEN now() ELSE outreach_inbound_sync.last_success_at END,
		     last_error = EXCLUDED.last_error,
		     updated_at = now()`,
		strings.TrimSpace(mailboxKey),
		lastError,
	)
	if err != nil {
		return fmt.Errorf("record inbound mailbox poll result: %w", err)
	}
	return nil
}

type messageScanner interface {
	Scan(dest ...any) error
}

func scanMessage(row messageScanner) (Message, error) {
	var record Message
	err := row.Scan(
		&record.ID,
		&record.RestaurantID,
		&record.CampaignID,
		&record.DeliveryAttemptID,
		&record.ReplyToken,
		&record.Direction,
		&record.FromEmail,
		&record.ToEmail,
		&record.ReplyTo,
		&record.Subject,
		&record.BodyText,
		&record.GmailMessageID,
		&record.GmailThreadID,
		&record.RFCMessageID,
		&record.MailboxKey,
		&record.Unmatched,
		&record.ReadAt,
		&record.ReceivedAt,
		&record.CreatedAt,
	)
	if err != nil {
		return Message{}, err
	}
	return record, nil
}
