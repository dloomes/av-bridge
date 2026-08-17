// Server-component shell for the Entra vendor SSO landing page.
//
// The actual behaviour lives in ./callback-client.tsx as a client
// component so we can call useSearchParams. Next.js requires
// useSearchParams-using client components to be rendered inside a
// Suspense boundary — otherwise `next build` fails to prerender the
// static shell (missing-suspense-with-csr-bailout). This tiny shell
// exists solely to satisfy that requirement.

import { Suspense } from "react";
import { SignInCallbackClient } from "./callback-client";

export default function SignInCallbackPage() {
  return (
    <Suspense fallback={null}>
      <SignInCallbackClient />
    </Suspense>
  );
}
