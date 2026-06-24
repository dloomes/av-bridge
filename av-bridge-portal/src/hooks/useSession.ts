"use client";

import { useEffect, useState } from "react";
import {
  getScope,
  getToken,
  getUser,
  subscribe,
  type SessionUser,
} from "@/lib/session";

interface SessionSnapshot {
  token: string | null;
  user: SessionUser | null;
  scope: string | null;
  // hydrated flips to true once we've read localStorage in the browser.
  // Before that, treating "no token" as "signed out" would falsely flash
  // the sign-in CTA during SSR / first render.
  hydrated: boolean;
}

const empty: SessionSnapshot = {
  token: null,
  user: null,
  scope: null,
  hydrated: false,
};

export function useSession(): SessionSnapshot {
  const [snap, setSnap] = useState<SessionSnapshot>(empty);

  useEffect(() => {
    const read = () =>
      setSnap({
        token: getToken(),
        user: getUser(),
        scope: getScope(),
        hydrated: true,
      });
    read();
    return subscribe(read);
  }, []);

  return snap;
}
