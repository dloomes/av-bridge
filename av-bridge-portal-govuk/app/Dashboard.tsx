'use client';

import Link from 'next/link';
import { useEffect, useMemo, useRef, useState } from 'react';
import { eventsWebSocketUrl, listDevices } from '@/lib/api';
import {
  formatMetricKey,
  formatMetricValue,
  formatStatusLabel,
  formatTimestamp,
  statusTagClass,
} from '@/lib/format';
import { groupDevicesByLocation } from '@/lib/grouping';
import type { Device, DeviceEvent } from '@/lib/types';

const POLL_INTERVAL_MS = 15_000;
const MAX_EVENTS = 20;

export default function Dashboard() {
  const [devices, setDevices] = useState<Device[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [events, setEvents] = useState<DeviceEvent[]>([]);
  const [wsState, setWsState] = useState<'connecting' | 'open' | 'closed'>('connecting');

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const data = await listDevices();
        if (cancelled) return;
        setDevices(data);
        setError(null);
      } catch (err) {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : 'Failed to load devices');
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    const id = window.setInterval(load, POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, []);

  useEffect(() => {
    let socket: WebSocket | null = null;
    let closed = false;
    try {
      socket = new WebSocket(eventsWebSocketUrl());
    } catch {
      setWsState('closed');
      return;
    }
    socket.onopen = () => setWsState('open');
    socket.onclose = () => {
      if (!closed) setWsState('closed');
    };
    socket.onerror = () => setWsState('closed');
    socket.onmessage = (msg) => {
      try {
        const parsed = JSON.parse(msg.data) as DeviceEvent;
        setEvents((prev) => [parsed, ...prev].slice(0, MAX_EVENTS));
      } catch {
        setEvents((prev) =>
          [
            {
              timestamp: new Date().toISOString(),
              message: typeof msg.data === 'string' ? msg.data : 'Event received',
            },
            ...prev,
          ].slice(0, MAX_EVENTS),
        );
      }
    };
    return () => {
      closed = true;
      socket?.close();
    };
  }, []);

  const summary = useMemo(() => {
    const total = devices.length;
    const online = devices.filter((d) => d.status === 'online').length;
    const offline = devices.filter((d) => d.status === 'offline').length;
    const degraded = devices.filter((d) => d.status === 'degraded').length;
    return { total, online, offline, degraded };
  }, [devices]);

  const groups = useMemo(() => groupDevicesByLocation(devices), [devices]);

  return (
    <>
      <h1 className="govuk-heading-l">Device dashboard</h1>

      {error && (
        <div
          className="govuk-error-summary"
          data-module="govuk-error-summary"
          aria-labelledby="error-summary-title"
          role="alert"
        >
          <h2 className="govuk-error-summary__title" id="error-summary-title">
            There is a problem
          </h2>
          <div className="govuk-error-summary__body">
            <p>{error}</p>
          </div>
        </div>
      )}

      <SummaryCards
        total={summary.total}
        online={summary.online}
        offline={summary.offline}
        degraded={summary.degraded}
      />

      <div className="govuk-grid-row">
        <div className="govuk-grid-column-two-thirds">
          <h2 className="govuk-heading-m">Devices</h2>
          {loading && devices.length === 0 ? (
            <p className="govuk-body">Loading devices&hellip;</p>
          ) : devices.length === 0 ? (
            <p className="govuk-body">No devices reported.</p>
          ) : groups.length === 1 && groups[0].building === null ? (
            // No building info — render rooms flat without the accordion wrapper.
            groups[0].rooms.map((room) => (
              <RoomBlock key={room.room} room={room.room} devices={room.devices} />
            ))
          ) : (
            <div
              className="govuk-accordion"
              data-module="govuk-accordion"
              id="device-accordion"
            >
              {groups.map((group, gi) => {
                const deviceCount = group.rooms.reduce(
                  (n, r) => n + r.devices.length,
                  0,
                );
                return (
                  <div
                    className="govuk-accordion__section"
                    key={group.building ?? `__group_${gi}`}
                  >
                    <div className="govuk-accordion__section-header">
                      <h3 className="govuk-accordion__section-heading">
                        <span
                          className="govuk-accordion__section-button"
                          id={`device-accordion-heading-${gi}`}
                        >
                          {group.building ?? 'Other'}
                        </span>
                      </h3>
                      <div className="govuk-accordion__section-summary govuk-body">
                        {deviceCount} device{deviceCount === 1 ? '' : 's'} across{' '}
                        {group.rooms.length} room
                        {group.rooms.length === 1 ? '' : 's'}
                      </div>
                    </div>
                    <div
                      className="govuk-accordion__section-content"
                      id={`device-accordion-content-${gi}`}
                    >
                      {group.rooms.map((room) => (
                        <RoomBlock
                          key={room.room}
                          room={room.room}
                          devices={room.devices}
                        />
                      ))}
                    </div>
                  </div>
                );
              })}
            </div>
          )}
          <p className="govuk-body-s">
            Updates every {POLL_INTERVAL_MS / 1000} seconds from{' '}
            <code>/api/v1/devices</code>.
          </p>
        </div>

        <div className="govuk-grid-column-one-third">
          <h2 className="govuk-heading-m">Recent events</h2>
          <p className="govuk-body-s">
            Live feed via WebSocket{' '}
            <strong
              className={
                wsState === 'open'
                  ? 'govuk-tag govuk-tag--green'
                  : wsState === 'connecting'
                    ? 'govuk-tag govuk-tag--blue'
                    : 'govuk-tag govuk-tag--red'
              }
            >
              {wsState === 'open' ? 'Connected' : wsState === 'connecting' ? 'Connecting' : 'Disconnected'}
            </strong>
          </p>
          <EventsTable events={events} />
        </div>
      </div>
    </>
  );
}

function SummaryCards({
  total,
  online,
  offline,
  degraded,
}: {
  total: number;
  online: number;
  offline: number;
  degraded: number;
}) {
  const cards: { label: string; value: number; tag?: string }[] = [
    { label: 'Total devices', value: total },
    { label: 'Online', value: online, tag: 'govuk-tag govuk-tag--green' },
    { label: 'Offline', value: offline, tag: 'govuk-tag govuk-tag--red' },
    { label: 'Degraded', value: degraded, tag: 'govuk-tag govuk-tag--yellow' },
  ];
  return (
    <div className="govuk-grid-row">
      {cards.map((card) => (
        <div key={card.label} className="govuk-grid-column-one-quarter">
          <div className="govuk-summary-card">
            <div className="govuk-summary-card__title-wrapper">
              <h2 className="govuk-summary-card__title">{card.label}</h2>
              {card.tag && <strong className={card.tag}>{card.label}</strong>}
            </div>
            <div className="govuk-summary-card__content">
              <p className="govuk-heading-xl govuk-!-margin-bottom-0">{card.value}</p>
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}

function RoomBlock({ room, devices }: { room: string; devices: Device[] }) {
  return (
    <div className="govuk-!-margin-bottom-4">
      <h4 className="govuk-heading-s govuk-!-margin-bottom-2">
        {room}{' '}
        <span className="govuk-hint govuk-!-display-inline govuk-!-font-weight-regular">
          · {devices.length} device{devices.length === 1 ? '' : 's'}
        </span>
      </h4>
      <ul className="govuk-list" style={{ paddingLeft: 0 }}>
        {devices.map((device) => (
          <li key={device.id}>
            <DeviceCard device={device} />
          </li>
        ))}
      </ul>
    </div>
  );
}

function DeviceCard({ device }: { device: Device }) {
  const metricEntries = Object.entries(device.metrics ?? {}).slice(0, 4);
  return (
    <div className="govuk-summary-card">
      <div className="govuk-summary-card__title-wrapper">
        <h3 className="govuk-summary-card__title">{device.name}</h3>
        <strong className={statusTagClass(device.status)}>
          {formatStatusLabel(device.status)}
        </strong>
      </div>
      <div className="govuk-summary-card__content">
        <dl className="govuk-summary-list">
          <div className="govuk-summary-list__row">
            <dt className="govuk-summary-list__key">Location</dt>
            <dd className="govuk-summary-list__value">{device.location ?? '—'}</dd>
          </div>
          <div className="govuk-summary-list__row">
            <dt className="govuk-summary-list__key">Type</dt>
            <dd className="govuk-summary-list__value">{device.type}</dd>
          </div>
          {metricEntries.map(([key, value]) => (
            <div className="govuk-summary-list__row" key={key}>
              <dt className="govuk-summary-list__key">{formatMetricKey(key)}</dt>
              <dd className="govuk-summary-list__value">{formatMetricValue(value)}</dd>
            </div>
          ))}
        </dl>
        <p className="govuk-body">
          <Link href={`/devices/${device.id}`} className="govuk-link">
            View details
          </Link>
        </p>
      </div>
    </div>
  );
}

function EventsTable({ events }: { events: DeviceEvent[] }) {
  if (events.length === 0) {
    return <p className="govuk-body">No events received yet.</p>;
  }
  return (
    <table className="govuk-table">
      <caption className="govuk-table__caption govuk-table__caption--s govuk-visually-hidden">
        Most recent device events
      </caption>
      <thead className="govuk-table__head">
        <tr className="govuk-table__row">
          <th scope="col" className="govuk-table__header">Time</th>
          <th scope="col" className="govuk-table__header">Device</th>
          <th scope="col" className="govuk-table__header">Event</th>
        </tr>
      </thead>
      <tbody className="govuk-table__body">
        {events.map((event, idx) => (
          <tr key={`${event.timestamp}-${idx}`} className="govuk-table__row">
            <td className="govuk-table__cell">{formatTimestamp(event.timestamp)}</td>
            <td className="govuk-table__cell">{event.device_name ?? event.device_id ?? '—'}</td>
            <td className="govuk-table__cell">{event.message}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
