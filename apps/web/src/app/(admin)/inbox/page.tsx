import { OutreachInbox } from "@/components/OutreachInbox";
import { PageHeader } from "@/components/ui";

export default function InboxPage() {
  return (
    <div>
      <PageHeader
        title="Inbox"
        subtitle="Search and reply to owner emails received across every outreach mailbox"
      />
      <OutreachInbox />
    </div>
  );
}
