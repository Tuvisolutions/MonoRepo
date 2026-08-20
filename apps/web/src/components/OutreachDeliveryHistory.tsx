"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { EmptyState, ErrorBanner, StatusBadge } from "@/components/ui";
import { adminFetch } from "@/lib/client-api";
import type {
  DailyOutreachDeliveryList,
  OutreachDeliveryOutcomeCounts,
} from "@/lib/types";

const PAGE_SIZE = 50;
const SYDNEY_TIMEZONE = "Australia/Sydney";

function sydneyDateValue(date = new Date()) {
  const parts = new Intl.DateTimeFormat("en-AU", {
    timeZone: SYDNEY_TIMEZONE,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(date);
  const values = Object.fromEntries(
    parts.map((part) => [part.type, part.value]),
  );
  return `${values.year}-${values.month}-${values.day}`;
}

function formatSydneyTimestamp(value?: string) {
  if (!value) return "—";
  try {
    return new Intl.DateTimeFormat("en-AU", {
      timeZone: SYDNEY_TIMEZONE,
      dateStyle: "medium",
      timeStyle: "short",
    }).format(new Date(value));
  } catch {
    return value;
  }
}

function phaseLabel(step: number) {
  return step > 0 ? `Email ${step} · Phase ${step}` : "Legacy email";
}

function CountCards({ counts }: { counts: OutreachDeliveryOutcomeCounts }) {
  const cards = [
    ["Attempts", counts.total],
    ["Marked sent", counts.sent],
    ["Failed", counts.failed],
    ["Outcome unknown", counts.unknown],
    ["Skipped", counts.skipped],
    ["In progress", counts.sending],
  ] as const;
  return (
    <div className="outreach-metrics delivery-outcome-metrics">
      {cards.map(([label, value]) => (
        <div className="card" key={label}>
          <div className="outreach-metric-label">{label}</div>
          <div className="outreach-metric-value">{value}</div>
        </div>
      ))}
    </div>
  );
}

export function OutreachDeliveryHistory() {
  const [selectedDate, setSelectedDate] = useState("");
  const [latestSydneyDate, setLatestSydneyDate] = useState("");
  const [accountID, setAccountID] = useState("");
  const [offset, setOffset] = useState(0);
  const [data, setData] = useState<DailyOutreachDeliveryList | null>(null);
  const [senderOptions, setSenderOptions] = useState<
    DailyOutreachDeliveryList["senders"]
  >([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const requestSequence = useRef(0);

  useEffect(() => {
    const today = sydneyDateValue();
    setLatestSydneyDate(today);
    setSelectedDate(today);
    const timer = window.setInterval(
      () => setLatestSydneyDate(sydneyDateValue()),
      60_000,
    );
    return () => window.clearInterval(timer);
  }, []);

  const load = useCallback(async () => {
    if (!selectedDate) return;
    const requestID = ++requestSequence.current;
    setLoading(true);
    setError(null);
    try {
      const response = await adminFetch<DailyOutreachDeliveryList>(
        "outreach/deliveries",
        {
          query: {
            date: selectedDate,
            account_id: accountID || undefined,
            limit: PAGE_SIZE,
            offset,
          },
        },
      );
      if (requestSequence.current === requestID) {
        setData(response);
        setSenderOptions(response.senders);
      }
    } catch (reason) {
      if (requestSequence.current === requestID) {
        setError(
          reason instanceof Error
            ? reason.message
            : "Could not load scheduled delivery history",
        );
      }
    } finally {
      if (requestSequence.current === requestID) {
        setLoading(false);
      }
    }
  }, [accountID, offset, selectedDate]);

  useEffect(() => {
    if (!selectedDate) return;
    void load();
    const timer =
      selectedDate === latestSydneyDate
        ? window.setInterval(load, 30_000)
        : undefined;
    return () => {
      if (timer) window.clearInterval(timer);
      requestSequence.current += 1;
    };
  }, [latestSydneyDate, load, selectedDate]);

  const pageStart = !data || data.total === 0 ? 0 : data.offset + 1;
  const pageEnd = data
    ? Math.min(data.offset + data.deliveries.length, data.total)
    : 0;
  const selectedSender = senderOptions.find(
    (sender) => sender.account_id === accountID,
  );

  function clearForFilterChange(willLoad = true) {
    requestSequence.current += 1;
    setData(null);
    setError(null);
    setLoading(willLoad);
  }

  function changePage(nextOffset: number) {
    requestSequence.current += 1;
    setLoading(true);
    setOffset(nextOffset);
  }

  return (
    <section aria-labelledby="delivery-history-title">
      <div className="card" style={{ marginBottom: "1rem" }}>
        <h2
          id="delivery-history-title"
          style={{ margin: 0, fontSize: "1.05rem" }}
        >
          Scheduled outreach delivery attempts
        </h2>
        <p
          style={{
            color: "var(--muted)",
            margin: "0.3rem 0 0",
            lineHeight: 1.5,
          }}
        >
          Review one Australia/Sydney calendar date by sending email ID. For
          each ID, view how many Email 1, Email 2, and Email 3 messages were
          marked sent, then open its recipients to see whom each attempt was
          addressed to and how it went. Marked sent means provider acceptance,
          not inbox delivery. Later bounces appear as failures only when
          separately recorded or reconciled. Template tests, inbox replies, and
          health checks are excluded.
        </p>
      </div>

      <div className="card delivery-history-filters">
        <label className="field-label" htmlFor="delivery-history-date">
          Sydney date
          <input
            id="delivery-history-date"
            className="input"
            type="date"
            value={selectedDate}
            max={latestSydneyDate || undefined}
            required
            onChange={(event) => {
              const nextDate = event.target.value;
              setSelectedDate(nextDate);
              setOffset(0);
              clearForFilterChange(Boolean(nextDate));
            }}
          />
        </label>
        <label className="field-label" htmlFor="delivery-history-sender">
          Email ID
          <select
            id="delivery-history-sender"
            className="select"
            value={accountID}
            onChange={(event) => {
              setAccountID(event.target.value);
              setOffset(0);
              clearForFilterChange();
            }}
          >
            <option value="">All email IDs</option>
            {senderOptions.map((sender) => (
              <option value={sender.account_id} key={sender.account_id}>
                {sender.sender_email || sender.account_key}
              </option>
            ))}
          </select>
        </label>
        <button
          className="btn btn-secondary"
          type="button"
          onClick={load}
          disabled={loading || !selectedDate}
        >
          {loading ? "Refreshing…" : "Refresh"}
        </button>
      </div>

      <ErrorBanner message={error} />
      {!selectedDate ? (
        <EmptyState message="Choose a Sydney date to view scheduled delivery attempts." />
      ) : null}
      {loading && !data ? (
        <div role="status" aria-live="polite">
          <EmptyState message="Loading scheduled delivery attempts…" />
        </div>
      ) : null}

      <div aria-busy={loading}>
        {data ? (
          <>
            <CountCards counts={data.summary} />

            <div className="card" style={{ marginBottom: "1rem" }}>
              <div className="delivery-history-heading">
                <div>
                  <h3 style={{ margin: 0, fontSize: "1rem" }}>
                    Sending email IDs
                  </h3>
                  <p className="field-help" style={{ margin: "0.25rem 0 0" }}>
                    Counts cover the complete Sydney date, not only the current
                    page. Select View recipients to see whom each email ID
                    attempted to contact and how every attempt went. Configured
                    IDs with no attempts are shown as zero.
                  </p>
                </div>
                {accountID ? (
                  <button
                    className="btn btn-secondary"
                    type="button"
                    onClick={() => {
                      setAccountID("");
                      setOffset(0);
                      clearForFilterChange();
                    }}
                  >
                    Show all email IDs
                  </button>
                ) : null}
              </div>
              {data.senders.length === 0 ? (
                <EmptyState message="No scheduled sending email IDs are configured." />
              ) : (
                <div className="table-wrap" style={{ marginTop: "0.85rem" }}>
                  <table className="data delivery-sender-table">
                    <thead>
                      <tr>
                        <th scope="col">Email ID</th>
                        <th scope="col">Attempts</th>
                        <th scope="col">Sent total</th>
                        <th scope="col">Email 1 sent</th>
                        <th scope="col">Email 2 sent</th>
                        <th scope="col">Email 3 sent</th>
                        <th scope="col">Other outcomes</th>
                        <th scope="col">Recipients</th>
                      </tr>
                    </thead>
                    <tbody>
                      {data.senders.map((sender) => (
                        <tr
                          key={sender.account_id}
                          data-selected={sender.account_id === accountID}
                        >
                          <td data-label="Email ID">
                            <div className="delivery-cell-content">
                              <strong className="delivery-break-anywhere">
                                {sender.sender_email || sender.account_key}
                              </strong>
                              <div className="field-help">
                                {sender.account_key}
                              </div>
                            </div>
                          </td>
                          <td data-label="Attempts">{sender.counts.total}</td>
                          <td data-label="Sent total">
                            <div className="delivery-cell-content">
                              <strong>{sender.counts.sent}</strong>
                              {sender.other_sent > 0 ? (
                                <div className="field-help">
                                  Includes {sender.other_sent} other or legacy
                                </div>
                              ) : null}
                            </div>
                          </td>
                          <td data-label="Email 1 sent">
                            {sender.phase_1_sent}
                          </td>
                          <td data-label="Email 2 sent">
                            {sender.phase_2_sent}
                          </td>
                          <td data-label="Email 3 sent">
                            {sender.phase_3_sent}
                          </td>
                          <td data-label="Other outcomes">
                            <div className="delivery-sender-outcomes">
                              <span>
                                Failed <strong>{sender.counts.failed}</strong>
                              </span>
                              <span>
                                Unknown <strong>{sender.counts.unknown}</strong>
                              </span>
                              <span>
                                Skipped <strong>{sender.counts.skipped}</strong>
                              </span>
                              <span>
                                In progress{" "}
                                <strong>{sender.counts.sending}</strong>
                              </span>
                            </div>
                          </td>
                          <td data-label="Recipients">
                            {sender.account_id === accountID ? (
                              <span
                                className="delivery-sender-viewing"
                                aria-live="polite"
                              >
                                Viewing recipients
                              </span>
                            ) : (
                              <button
                                className="btn btn-secondary btn-compact"
                                type="button"
                                aria-controls="delivery-recipient-list"
                                aria-label={`View recipients for ${sender.sender_email || sender.account_key}`}
                                onClick={() => {
                                  setAccountID(sender.account_id);
                                  setOffset(0);
                                  clearForFilterChange();
                                }}
                              >
                                View recipients
                              </button>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>

            <section
              id="delivery-recipient-list"
              aria-labelledby="delivery-recipient-list-heading"
            >
              <div
                className="card delivery-history-heading"
                style={{ marginBottom: "0.75rem" }}
              >
                <div>
                  <h3
                    id="delivery-recipient-list-heading"
                    style={{ margin: 0, fontSize: "1rem" }}
                  >
                    {selectedSender
                      ? `Recipients and outcomes for ${selectedSender.sender_email || selectedSender.account_key}`
                      : "Recipients across all email IDs"}
                  </h3>
                  <p
                    className="field-help"
                    style={{ margin: "0.25rem 0 0" }}
                    aria-live="polite"
                  >
                    {pageStart}–{pageEnd} of {data.total} for {data.date} ·{" "}
                    {data.timezone}
                  </p>
                </div>
              </div>

              {!loading && !error && data.deliveries.length === 0 ? (
                <EmptyState message="No scheduled outreach attempts match this date and email ID." />
              ) : null}

              {data.deliveries.length > 0 ? (
                <div className="table-wrap">
                  <table className="data delivery-history-table">
                  <thead>
                    <tr>
                      <th scope="col">Sydney time</th>
                      <th scope="col">Email ID</th>
                      <th scope="col">Restaurant / recipient</th>
                      <th scope="col">Email / subject</th>
                      <th scope="col">Outcome</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.deliveries.map((delivery) => (
                      <tr key={delivery.id}>
                        <td data-label="Sydney time">
                          <div className="delivery-cell-content">
                            <strong>
                              {formatSydneyTimestamp(delivery.attempted_at)}
                            </strong>
                            {delivery.outcome_at ? (
                              <div className="field-help">
                                Outcome{" "}
                                {formatSydneyTimestamp(delivery.outcome_at)}
                              </div>
                            ) : null}
                          </div>
                        </td>
                        <td
                          data-label="Email ID"
                          className="delivery-wrap-cell"
                        >
                          <div className="delivery-cell-content">
                            <div>
                              {delivery.sender_email || delivery.account_key}
                            </div>
                            <div className="field-help">
                              {delivery.account_key}
                            </div>
                          </div>
                        </td>
                        <td
                          data-label="Restaurant / recipient"
                          className="delivery-wrap-cell"
                        >
                          <div className="delivery-cell-content">
                            <Link
                              className="recipient-name"
                              href={`/restaurants/${delivery.restaurant_id}`}
                            >
                              {delivery.restaurant_name}
                            </Link>
                            <div className="field-help delivery-break-anywhere">
                              {delivery.recipient_email ||
                                "Recipient not recorded on this legacy attempt"}
                            </div>
                          </div>
                        </td>
                        <td
                          data-label="Email / subject"
                          className="delivery-wrap-cell"
                        >
                          <div className="delivery-cell-content">
                            <strong>
                              {phaseLabel(delivery.campaign_step)}
                            </strong>
                            <div className="field-help">
                              {delivery.subject ||
                                "Subject snapshot not recorded"}
                            </div>
                          </div>
                        </td>
                        <td data-label="Outcome" className="delivery-wrap-cell">
                          <div className="delivery-cell-content">
                            <StatusBadge status={delivery.status} />
                            <div style={{ marginTop: "0.3rem" }}>
                              {delivery.outcome}
                            </div>
                            {delivery.error_code ? (
                              <code className="field-help delivery-break-anywhere">
                                {delivery.error_code}
                              </code>
                            ) : null}
                            {delivery.provider_message_id ? (
                              <div className="field-help delivery-break-anywhere">
                                Provider ID:{" "}
                                <code>{delivery.provider_message_id}</code>
                              </div>
                            ) : null}
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                  </table>
                </div>
              ) : null}

              <div className="delivery-history-pagination">
                <button
                  className="btn btn-secondary"
                  type="button"
                  onClick={load}
                  disabled={loading}
                >
                  {loading ? "Refreshing…" : "Refresh"}
                </button>
                <div style={{ display: "flex", gap: "0.5rem" }}>
                  <button
                    className="btn btn-secondary"
                    type="button"
                    onClick={() => changePage(Math.max(0, offset - PAGE_SIZE))}
                    disabled={offset === 0 || loading}
                  >
                    Previous
                  </button>
                  <button
                    className="btn btn-secondary"
                    type="button"
                    onClick={() => changePage(offset + PAGE_SIZE)}
                    disabled={
                      offset + data.deliveries.length >= data.total || loading
                    }
                  >
                    Next
                  </button>
                </div>
              </div>
            </section>
          </>
        ) : null}
      </div>
    </section>
  );
}
