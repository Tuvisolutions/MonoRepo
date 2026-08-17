import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Tuvi Admin",
  description: "Internal admin portal for lead scrape, review, and outreach",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body className="antialiased">{children}</body>
    </html>
  );
}
