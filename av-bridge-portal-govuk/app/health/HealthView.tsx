'use client';

import { useEffect, useState } from 'react';
import { getMetrics, getStatus } from '@/lib/api';
import type { ServiceStatus } from '@/lib/types';

export default function HealthView() {
  const [status, setStatus] = useState<ServiceStatus | null>(null);
  const [metrics, setMetrics] = useState<string>('');
  const [statusError, setStatusError] = useState<string | null>(null);
  const [metricsError, setMetricsError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    getStatus()
      .then((s) => !cancelled && setStatus(s))
      .catch((e: unknown) => !cancelled && setStatusError(e instanceof Error ? e.message : String(e)));
    getMetrics()
      .then((m) => !cancelled && setMetrics(m))
      .catch((e: unknown) => !cancelled && setMetricsError(e instanceof Error ? e.message : String(e)));
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <>
      <h1 className="govuk-heading-l">Service health</h1>

      <h2 className="govuk-heading-m">Status</h2>
      <p className="govuk-body-s">
        Source: <code>/api/v1/status</code>
      </p>
      <div className="govuk-inset-text">
        {statusError ? (
          <p className="govuk-body">Unable to load status: {statusError}</p>
        ) : status ? (
          <pre style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
            {JSON.stringify(status, null, 2)}
          </pre>
        ) : (
          <p className="govuk-body">Loading status&hellip;</p>
        )}
      </div>

      <h2 className="govuk-heading-m">Metrics</h2>
      <p className="govuk-body-s">
        Source: <code>/metrics</code>
      </p>
      <div className="govuk-inset-text">
        {metricsError ? (
          <p className="govuk-body">Unable to load metrics: {metricsError}</p>
        ) : metrics ? (
          <pre style={{ margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
            {metrics}
          </pre>
        ) : (
          <p className="govuk-body">Loading metrics&hellip;</p>
        )}
      </div>
    </>
  );
}
