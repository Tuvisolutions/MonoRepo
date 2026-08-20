export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string;
  subtitle?: string;
  actions?: React.ReactNode;
}) {
  return (
    <div
      style={{
        display: "flex",
        flexWrap: "wrap",
        gap: "0.75rem",
        justifyContent: "space-between",
        alignItems: "flex-end",
        marginBottom: "1.1rem",
      }}
    >
      <div>
        <h1
          style={{
            margin: 0,
            fontFamily: "var(--font-fraunces), serif",
            fontSize: "1.7rem",
            fontWeight: 600,
          }}
        >
          {title}
        </h1>
        {subtitle ? (
          <p style={{ margin: "0.25rem 0 0", color: "var(--muted)" }}>
            {subtitle}
          </p>
        ) : null}
      </div>
      {actions ? <div style={{ display: "flex", gap: "0.5rem", flexWrap: "wrap" }}>{actions}</div> : null}
    </div>
  );
}

export function StatusBadge({ status }: { status?: string | null }) {
  if (!status) return <span className="badge badge-neutral">—</span>;
  const s = status.toLowerCase();
  let cls = "badge-neutral";
  if (
    ["running", "active", "enabled", "eligible", "approved", "published", "verified", "completed", "sent"].includes(
      s,
    )
  ) {
    cls = "badge-ok";
  } else if (["queued", "waiting", "paused", "draft", "pending", "sending", "unknown"].includes(s)) {
    cls = "badge-warn";
  } else if (
    ["failed", "rejected", "cancelled", "lost", "archived"].includes(s)
  ) {
    cls = "badge-bad";
  }
  return <span className={`badge ${cls}`}>{status}</span>;
}

export function EmptyState({ message }: { message: string }) {
  return (
    <div className="card" style={{ color: "var(--muted)", textAlign: "center" }}>
      {message}
    </div>
  );
}

export function ErrorBanner({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <div className="alert alert-error" style={{ marginBottom: "1rem" }}>
      {message}
    </div>
  );
}
