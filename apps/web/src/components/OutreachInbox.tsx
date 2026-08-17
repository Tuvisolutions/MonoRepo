"use client";

import Link from "next/link";
import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { adminFetch } from "@/lib/client-api";
import { formatDate } from "@/lib/constants";
import type { EmailMessage, InboxListResponse, InboxThread } from "@/lib/types";
import { Modal } from "@/components/Modal";
import { EmptyState, ErrorBanner } from "@/components/ui";

const pageSize = 50;
const refreshIntervalMs = 15_000;

export function OutreachInbox() {
  const [unreadOnly, setUnreadOnly] = useState(false);
  const [mailboxKey, setMailboxKey] = useState("");
  const [searchInput, setSearchInput] = useState("");
  const [searchQuery, setSearchQuery] = useState("");
  const [offset, setOffset] = useState(0);
  const [data, setData] = useState<InboxListResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [lastRefreshedAt, setLastRefreshedAt] = useState<string | null>(null);
  const [selectedThread, setSelectedThread] = useState<InboxThread | null>(null);
  const [messageDetail, setMessageDetail] = useState<EmailMessage | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const requestSequence = useRef(0);
  const detailRequestSequence = useRef(0);
  const inboxHeadingRef = useRef<HTMLHeadingElement>(null);

  const load = useCallback(async (manual = false) => {
    const requestID = ++requestSequence.current;
    if (manual) setRefreshing(true);
    setError(null);
    try {
      const result = await adminFetch<InboxListResponse>("outreach/inbox", {
        query: {
          unread: unreadOnly ? true : undefined,
          mailbox: mailboxKey || undefined,
          q: searchQuery || undefined,
          limit: pageSize,
          offset,
        },
      });
      if (requestID !== requestSequence.current) return;
      setData(result);
      setLastRefreshedAt(new Date().toISOString());
    } catch (err) {
      if (requestID !== requestSequence.current) return;
      setError(err instanceof Error ? err.message : "Failed to load inbox");
    } finally {
      if (requestID === requestSequence.current) {
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, [mailboxKey, offset, searchQuery, unreadOnly]);

  useEffect(() => {
    setLoading(true);
    void load();
    const timer = window.setInterval(() => void load(), refreshIntervalMs);
    return () => {
      window.clearInterval(timer);
      requestSequence.current += 1;
    };
  }, [load]);

  const openMessage = useCallback(async (thread: InboxThread) => {
    const requestID = ++detailRequestSequence.current;
    setSelectedThread(thread);
    setMessageDetail(null);
    setDetailError(null);
    setDetailLoading(true);
    try {
      const message = await adminFetch<EmailMessage>(`outreach/messages/${thread.reply_message_id}/read`, {
        method: "POST",
      });
      if (requestID !== detailRequestSequence.current) return;
      setMessageDetail(message);
      void load();
    } catch (err) {
      if (requestID !== detailRequestSequence.current) return;
      setDetailError(err instanceof Error ? err.message : "Failed to open message");
    } finally {
      if (requestID === detailRequestSequence.current) setDetailLoading(false);
    }
  }, [load]);

  const closeMessage = useCallback(() => {
    detailRequestSequence.current += 1;
    setSelectedThread(null);
    setMessageDetail(null);
    setDetailError(null);
    setDetailLoading(false);
  }, []);

  function submitSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setOffset(0);
    setSearchQuery(searchInput.trim());
  }

  return (
    <div>
      <div
        className="card"
        style={{
          marginBottom: "1rem",
          display: "flex",
          gap: "0.75rem",
          alignItems: "center",
          justifyContent: "space-between",
          flexWrap: "wrap",
        }}
      >
        <div>
          <h2 ref={inboxHeadingRef} tabIndex={-1} style={{ margin: 0, fontSize: "1.05rem" }}>
            Outreach inbox
          </h2>
          <p style={{ color: "var(--muted)", margin: "0.3rem 0 0" }}>
            Sent snapshots and captured owner replies. A reply pauses that restaurant&apos;s campaign.
          </p>
        </div>
        <div style={{ display: "flex", gap: "0.75rem", alignItems: "center", flexWrap: "wrap" }}>
          <form
            onSubmit={submitSearch}
            role="search"
            aria-label="Search all inbox mail"
            style={{ display: "flex", gap: "0.4rem", alignItems: "end", flexWrap: "wrap" }}
          >
            <label style={{ display: "grid", gap: "0.25rem", fontSize: "0.85rem" }}>
              <span>Global search</span>
              <input
                className="input"
                type="search"
                value={searchInput}
                onChange={(event) => setSearchInput(event.target.value)}
                placeholder="Subject, sender, restaurant, message…"
                maxLength={200}
                style={{ minWidth: "260px" }}
              />
            </label>
            <button className="btn btn-primary" type="submit" disabled={loading}>
              Search
            </button>
            {searchQuery ? (
              <button
                className="btn btn-secondary"
                type="button"
                onClick={() => {
                  setSearchInput("");
                  setSearchQuery("");
                  setOffset(0);
                }}
              >
                Clear
              </button>
            ) : null}
          </form>
          <label style={{ display: "grid", gap: "0.25rem", fontSize: "0.85rem" }}>
            <span>Receiving mailbox</span>
            <select
              className="input"
              value={mailboxKey}
              onChange={(event) => {
                setMailboxKey(event.target.value);
                setOffset(0);
              }}
            >
              <option value="">All mailboxes</option>
              {(data?.mailboxes || []).map((mailbox) => (
                <option key={mailbox.mailbox_key} value={mailbox.mailbox_key}>
                  {mailbox.mailbox_key}
                </option>
              ))}
            </select>
          </label>
          <label style={{ display: "flex", gap: "0.4rem", alignItems: "center", fontSize: "0.9rem" }}>
            <input
              type="checkbox"
              checked={unreadOnly}
              onChange={(event) => {
                setUnreadOnly(event.target.checked);
                setOffset(0);
              }}
            />
            Unread only
          </label>
          <button
            className="btn btn-secondary"
            type="button"
            disabled={loading || refreshing}
            onClick={() => void load(true)}
          >
            {refreshing ? "Refreshing…" : "Refresh"}
          </button>
          <span className="field-help">
            Page {Math.floor(offset / pageSize) + 1} · Auto-refresh every 15 seconds
            {lastRefreshedAt ? ` · Updated ${formatDate(lastRefreshedAt)}` : ""}
          </span>
        </div>
      </div>
      <ErrorBanner message={error} />
      {(data?.mailboxes || []).map((mailbox) =>
        mailbox.last_error ? (
          <div className="alert alert-error" key={mailbox.mailbox_key} style={{ marginBottom: "0.75rem" }}>
            <strong>{mailbox.mailbox_key}</strong>: inbox access is unavailable. {mailbox.last_error}
          </div>
        ) : null,
      )}
      {loading && !data ? <EmptyState message="Loading inbox…" /> : null}
      {!loading && !error && (data?.threads || []).length === 0 ? (
        <EmptyState
          message={
            searchQuery
              ? `No inbox mail matches “${searchQuery}” in the last 10 days.`
              : "No inbox mail received in the last 10 days."
          }
        />
      ) : null}
      {(data?.threads || []).length > 0 ? (
        <div className="table-wrap">
          <table className="data">
            <thead>
              <tr>
                <th>Restaurant</th>
                <th>Subject</th>
                <th>Text</th>
                <th>Received from</th>
                <th>Received by</th>
                <th>Received at</th>
                <th>Unread</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {(data?.threads || []).map((thread) => (
                <InboxRow
                  key={thread.reply_message_id}
                  thread={thread}
                  onOpen={() => void openMessage(thread)}
                  onReplied={() => load()}
                />
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      {data && data.total > 0 ? (
        <div
          style={{
            display: "flex",
            justifyContent: "space-between",
            alignItems: "center",
            gap: "0.75rem",
            marginTop: "0.75rem",
            flexWrap: "wrap",
          }}
        >
          <span style={{ color: "var(--muted)", fontSize: "0.85rem" }}>
            Showing {Math.min(offset + 1, data.total)}–{Math.min(offset + data.threads.length, data.total)} of {data.total}
          </span>
          <div style={{ display: "flex", gap: "0.5rem" }}>
            <button
              className="btn btn-secondary"
              type="button"
              disabled={offset === 0 || loading || refreshing}
              onClick={() => setOffset((value) => Math.max(0, value - pageSize))}
            >
              Previous
            </button>
            <button
              className="btn btn-secondary"
              type="button"
              disabled={offset + pageSize >= data.total || loading || refreshing}
              onClick={() => setOffset((value) => value + pageSize)}
            >
              Next
            </button>
          </div>
        </div>
      ) : null}
      <Modal
        open={selectedThread !== null}
        onClose={closeMessage}
        title={messageDetail?.subject || selectedThread?.subject || "(no subject)"}
        width={780}
        fallbackFocusRef={inboxHeadingRef}
      >
        {detailLoading ? (
          <p role="status" aria-live="polite" style={{ margin: 0, color: "var(--muted)" }}>
            Loading complete message…
          </p>
        ) : null}
        {detailError ? (
          <>
            <ErrorBanner message={detailError} />
            <div>
              <button
                className="btn btn-secondary"
                type="button"
                onClick={() => selectedThread && void openMessage(selectedThread)}
              >
                Try again
              </button>
            </div>
          </>
        ) : null}
        {messageDetail ? (
          <>
            <dl className="inbox-message-meta">
              <div>
                <dt>Received from</dt>
                <dd>{messageDetail.from_email || "—"}</dd>
              </div>
              <div>
                <dt>Received by</dt>
                <dd>{messageDetail.to_email || messageDetail.mailbox_key || "—"}</dd>
              </div>
              <div>
                <dt>Received at</dt>
                <dd>{formatDate(messageDetail.received_at)}</dd>
              </div>
            </dl>
            <pre className="inbox-message-body">{messageDetail.body_text || "No text content."}</pre>
          </>
        ) : null}
      </Modal>
    </div>
  );
}

function InboxRow({
  thread,
  onOpen,
  onReplied,
}: {
  thread: InboxThread;
  onOpen: () => void;
  onReplied: () => Promise<void>;
}) {
  const [replying, setReplying] = useState(false);
  const [subject, setSubject] = useState("");
  const [bodyText, setBodyText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const name = thread.restaurant_name || (thread.unmatched ? "Unmatched email" : "Unknown restaurant");
  const title = thread.restaurant_id ? (
    <Link href={`/restaurants/${thread.restaurant_id}?tab=messages`}>{name}</Link>
  ) : (
    name
  );
  async function sendReply(event: FormEvent) {
    event.preventDefault();
    if (!bodyText.trim()) return;
    if (
      !window.confirm(
        `Send this reply to ${thread.from_email || thread.email || "the inbound sender"} from ${thread.to_email || thread.mailbox_email || thread.mailbox_key}?`,
      )
    ) return;
    setBusy(true);
    setError(null);
    try {
      await adminFetch(`outreach/messages/${thread.reply_message_id}/reply`, {
        method: "POST",
        body: { subject: subject.trim() || undefined, body_text: bodyText },
      });
      setReplying(false);
      setSubject("");
      setBodyText("");
      await onReplied();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Reply failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
    <tr>
      <td>
        {title}
      </td>
      <td className="inbox-subject-cell">
        <button className="inbox-message-open inbox-message-subject" type="button" onClick={onOpen}>
          {thread.subject || "(no subject)"}
        </button>
      </td>
      <td className="inbox-text-cell">
        <div className="inbox-message-preview">{thread.text_snippet || "—"}</div>
      </td>
      <td>{thread.from_email || thread.email || "—"}</td>
      <td>{thread.to_email || thread.mailbox_email || thread.mailbox_key}</td>
      <td>{formatDate(thread.received_at)}</td>
      <td>{thread.unread_count}</td>
      <td>
        <button className="btn btn-secondary" type="button" onClick={onOpen}>
          Open
        </button>{" "}
        <button className="btn btn-secondary" type="button" onClick={() => setReplying((value) => !value)}>
          {replying ? "Cancel" : "Reply"}
        </button>
      </td>
    </tr>
    {replying ? (
      <tr>
        <td colSpan={8}>
          <form onSubmit={sendReply} className="card" style={{ display: "grid", gap: "0.65rem", margin: "0.5rem 0" }}>
            <strong>Reply from {thread.to_email || thread.mailbox_email || thread.mailbox_key}</strong>
            <label style={{ display: "grid", gap: "0.3rem" }}>
              <span>Subject / title (optional)</span>
              <input
                className="input"
                value={subject}
                onChange={(event) => setSubject(event.target.value)}
                maxLength={200}
                placeholder="Defaults to Re: original subject"
              />
            </label>
            <label style={{ display: "grid", gap: "0.3rem" }}>
              <span>Plain-text reply</span>
              <textarea
                className="textarea"
                rows={5}
                value={bodyText}
                onChange={(event) => setBodyText(event.target.value)}
                maxLength={10000}
                required
              />
            </label>
            {error ? <div className="alert alert-error">{error}</div> : null}
            <div>
              <button className="btn btn-primary" type="submit" disabled={busy || !bodyText.trim()}>
                {busy ? "Sending…" : "Review and send reply"}
              </button>
            </div>
          </form>
        </td>
      </tr>
    ) : null}
    </>
  );
}
