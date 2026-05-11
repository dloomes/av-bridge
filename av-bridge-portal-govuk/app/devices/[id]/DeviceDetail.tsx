'use client';

import Link from 'next/link';
import { useEffect, useMemo, useState } from 'react';
import { getDevice, sendCommand } from '@/lib/api';
import {
  formatMetricKey,
  formatMetricValue,
  formatStatusLabel,
  formatTimestamp,
  statusTagClass,
} from '@/lib/format';
import type { CommandResponse, Device, DeviceType } from '@/lib/types';

const POLL_INTERVAL_MS = 10_000;

type ButtonVariant = 'default' | 'start' | 'secondary' | 'warning';

interface CommandSpec {
  label: string;
  name: string;
  variant: ButtonVariant;
}

function commandsForType(type: DeviceType): CommandSpec[] {
  switch (type) {
    case 'display':
      return [
        { label: 'Power on', name: 'power_on', variant: 'start' },
        { label: 'Power off', name: 'power_off', variant: 'warning' },
        { label: 'Input HDMI 1', name: 'input_hdmi_1', variant: 'secondary' },
        { label: 'Input HDMI 2', name: 'input_hdmi_2', variant: 'secondary' },
        { label: 'Input HDMI 3', name: 'input_hdmi_3', variant: 'secondary' },
      ];
    case 'audio':
      return [
        { label: 'Mute', name: 'mute', variant: 'warning' },
        { label: 'Unmute', name: 'unmute', variant: 'default' },
        { label: 'Volume up', name: 'volume_up', variant: 'secondary' },
        { label: 'Volume down', name: 'volume_down', variant: 'secondary' },
      ];
    case 'video':
      return [
        { label: 'Mute', name: 'mute', variant: 'warning' },
        { label: 'Unmute', name: 'unmute', variant: 'default' },
      ];
    default:
      return [];
  }
}

function buttonClass(variant: ButtonVariant): string {
  switch (variant) {
    case 'start':
      return 'govuk-button govuk-button--start';
    case 'warning':
      return 'govuk-button govuk-button--warning';
    case 'secondary':
      return 'govuk-button govuk-button--secondary';
    default:
      return 'govuk-button';
  }
}

interface CommandFeedback {
  variant: 'success' | 'error';
  title: string;
  message: string;
}

export default function DeviceDetail({ id }: { id: string }) {
  const [device, setDevice] = useState<Device | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [feedback, setFeedback] = useState<CommandFeedback | null>(null);
  const [pendingCommand, setPendingCommand] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const data = await getDevice(id);
        if (cancelled) return;
        setDevice(data);
        setError(null);
      } catch (err) {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : 'Failed to load device');
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    const t = window.setInterval(load, POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(t);
    };
  }, [id]);

  const commands = useMemo(() => (device ? commandsForType(device.type) : []), [device]);

  async function runCommand(name: string, label: string) {
    setPendingCommand(name);
    setFeedback(null);
    try {
      const res: CommandResponse = await sendCommand(id, { name });
      setFeedback({
        variant: res.ok === false ? 'error' : 'success',
        title: res.ok === false ? 'Command failed' : 'Command sent',
        message: res.message ?? `${label} dispatched to device.`,
      });
    } catch (err) {
      setFeedback({
        variant: 'error',
        title: 'Command failed',
        message: err instanceof Error ? err.message : 'Unknown error',
      });
    } finally {
      setPendingCommand(null);
    }
  }

  return (
    <>
      <Link href="/" className="govuk-back-link">
        Back to dashboard
      </Link>

      {error && (
        <div className="govuk-error-summary" data-module="govuk-error-summary" role="alert">
          <h2 className="govuk-error-summary__title">There is a problem</h2>
          <div className="govuk-error-summary__body">
            <p>{error}</p>
          </div>
        </div>
      )}

      {loading && !device ? (
        <p className="govuk-body">Loading device&hellip;</p>
      ) : device ? (
        <>
          <span className="govuk-caption-l">Device {device.id}</span>
          <h1 className="govuk-heading-l">
            {device.name}{' '}
            <strong className={statusTagClass(device.status)}>
              {formatStatusLabel(device.status)}
            </strong>
          </h1>

          {feedback && (
            <div
              className={
                feedback.variant === 'error'
                  ? 'govuk-notification-banner'
                  : 'govuk-notification-banner govuk-notification-banner--success'
              }
              role={feedback.variant === 'error' ? 'alert' : 'region'}
              aria-labelledby="notification-title"
              data-module="govuk-notification-banner"
            >
              <div className="govuk-notification-banner__header">
                <h2 className="govuk-notification-banner__title" id="notification-title">
                  {feedback.variant === 'error' ? 'Important' : 'Success'}
                </h2>
              </div>
              <div className="govuk-notification-banner__content">
                <p className="govuk-notification-banner__heading">{feedback.title}</p>
                <p className="govuk-body">{feedback.message}</p>
              </div>
            </div>
          )}

          <div className="govuk-grid-row">
            <div className="govuk-grid-column-two-thirds">
              <h2 className="govuk-heading-m">Telemetry</h2>
              <h3 className="govuk-heading-s">Direct from device</h3>
              <dl className="govuk-summary-list">
                <Row term="Name" detail={device.name} />
                <Row term="Location" detail={device.location ?? '—'} />
                <Row term="Type" detail={device.type} />
                <Row term="Status" detail={formatStatusLabel(device.status)} />
                <Row term="Last seen" detail={formatTimestamp(device.last_seen)} />
                {Object.entries(device.metrics ?? {}).map(([key, value]) => (
                  <Row
                    key={key}
                    term={formatMetricKey(key)}
                    detail={formatMetricValue(value)}
                  />
                ))}
              </dl>
              {device.lens_metrics && Object.keys(device.lens_metrics).length > 0 && (
                <>
                  <h3 className="govuk-heading-s govuk-!-margin-top-6">Poly Lens</h3>
                  <dl className="govuk-summary-list">
                    {Object.entries(device.lens_metrics).map(([key, value]) => (
                      <Row
                        key={key}
                        term={formatMetricKey(key)}
                        detail={formatMetricValue(value)}
                      />
                    ))}
                  </dl>
                </>
              )}
              <p className="govuk-body-s">
                Refreshes every {POLL_INTERVAL_MS / 1000} seconds.
              </p>
            </div>

            <div className="govuk-grid-column-one-third">
              <h2 className="govuk-heading-m">Commands</h2>
              {commands.length === 0 ? (
                <p className="govuk-body">No commands available for this device type.</p>
              ) : (
                <div className="govuk-button-group">
                  {commands.map((cmd) => (
                    <button
                      key={cmd.name}
                      type="button"
                      className={buttonClass(cmd.variant)}
                      data-module="govuk-button"
                      disabled={pendingCommand !== null}
                      onClick={() => runCommand(cmd.name, cmd.label)}
                    >
                      {pendingCommand === cmd.name ? `${cmd.label}…` : cmd.label}
                    </button>
                  ))}
                </div>
              )}
            </div>
          </div>
        </>
      ) : null}
    </>
  );
}

function Row({ term, detail }: { term: string; detail: string }) {
  return (
    <div className="govuk-summary-list__row">
      <dt className="govuk-summary-list__key">{term}</dt>
      <dd className="govuk-summary-list__value">{detail}</dd>
    </div>
  );
}
