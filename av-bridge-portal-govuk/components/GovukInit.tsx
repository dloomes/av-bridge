'use client';

import { useEffect } from 'react';

export default function GovukInit() {
  useEffect(() => {
    let cancelled = false;
    document.body.classList.add('govuk-frontend-supported');
    import('govuk-frontend').then((mod) => {
      if (cancelled) return;
      mod.initAll();
    });
    return () => {
      cancelled = true;
    };
  }, []);
  return null;
}
