"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { withBasePath } from "@/lib/base-path";

const NAV = [
  { href: "/dashboard", label: "Dashboard" },
  { href: "/scrape-jobs", label: "Scrape jobs" },
  { href: "/restaurants", label: "Restaurants" },
  { href: "/consultation-calendar", label: "Consultation calendar" },
  { href: "/inbox", label: "Inbox" },
  { href: "/outreach", label: "Outreach" },
  { href: "/developer", label: "Developer" },
];

export function AdminShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const [email, setEmail] = useState<string | null>(null);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    fetch(withBasePath("/api/admin/auth/me"))
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => setEmail(d?.user?.email ?? null))
      .catch(() => setEmail(null));
  }, []);

  async function logout() {
    await fetch(withBasePath("/api/admin/auth/logout"), { method: "POST" });
    router.replace("/login");
    router.refresh();
  }

  return (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "minmax(0, 240px) 1fr",
        minHeight: "100vh",
      }}
      className="admin-shell"
    >
      <style>{`
        .admin-backdrop { display: none; }
        @media (max-width: 860px) {
          .admin-shell { grid-template-columns: 1fr !important; }
          .admin-sidebar {
            position: fixed;
            inset: 0 auto 0 0;
            width: min(80vw, 260px);
            z-index: 40;
            transform: translateX(${open ? "0" : "-105%"});
            transition: transform 0.2s ease;
          }
          .admin-backdrop {
            display: ${open ? "block" : "none"};
            position: fixed;
            inset: 0;
            background: rgba(0,0,0,0.35);
            z-index: 30;
          }
        }
      `}</style>

      <div className="admin-backdrop" onClick={() => setOpen(false)} />

      <aside
        className="admin-sidebar"
        style={{
          background: "var(--sidebar)",
          color: "var(--sidebar-text)",
          padding: "1.25rem 1rem",
          display: "flex",
          flexDirection: "column",
          gap: "1.25rem",
        }}
      >
        <div>
          <div
            style={{
              fontFamily: "var(--font-fraunces), serif",
              fontSize: "1.35rem",
              fontWeight: 600,
            }}
          >
            Tuvi Admin
          </div>
          <div style={{ color: "var(--sidebar-muted)", fontSize: "0.85rem" }}>
            Lead workflow
          </div>
        </div>

        <nav style={{ display: "grid", gap: "0.25rem" }}>
          {NAV.map((item) => {
            const active =
              pathname === item.href || pathname.startsWith(item.href + "/");
            return (
              <Link
                key={item.href}
                href={item.href}
                onClick={() => setOpen(false)}
                style={{
                  padding: "0.6rem 0.75rem",
                  background: active
                    ? "color-mix(in srgb, white 10%, transparent)"
                    : "transparent",
                  color: active ? "white" : "var(--sidebar-text)",
                  fontWeight: active ? 700 : 500,
                }}
              >
                {item.label}
              </Link>
            );
          })}
        </nav>

        <div style={{ marginTop: "auto", display: "grid", gap: "0.5rem" }}>
          <div style={{ fontSize: "0.8rem", color: "var(--sidebar-muted)" }}>
            {email || "…"}
          </div>
          <button
            type="button"
            className="btn btn-secondary"
            onClick={logout}
            style={{ width: "100%" }}
          >
            Sign out
          </button>
        </div>
      </aside>

      <div style={{ minWidth: 0 }}>
        <header
          style={{
            display: "flex",
            alignItems: "center",
            gap: "0.75rem",
            padding: "0.85rem 1.25rem",
            borderBottom: "1px solid var(--line)",
            background: "var(--bg-elevated)",
          }}
        >
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => setOpen(true)}
            style={{ display: "none" }}
            id="menu-btn"
          >
            Menu
          </button>
          <style>{`
            @media (max-width: 860px) {
              #menu-btn { display: inline-flex !important; }
            }
          `}</style>
          <div style={{ color: "var(--muted)", fontSize: "0.9rem" }}>
            internal_admin console
          </div>
        </header>
        <main style={{ padding: "1.25rem" }}>{children}</main>
      </div>
    </div>
  );
}
