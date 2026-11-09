import type { Metadata } from "next";
import { SharedRoadmapClient } from "./shared-roadmap";

// A customer's emailed link: no session, no sign-in, not indexed, and no
// referrer so the token never leaves in a request header.
export const metadata: Metadata = {
  title: "Product roadmap",
  robots: { index: false, follow: false },
  referrer: "no-referrer",
};

export default function SharedRoadmapPage() {
  return <SharedRoadmapClient />;
}
