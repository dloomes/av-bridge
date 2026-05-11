import type { DeviceStatus } from './types';

export function statusTagClass(status: DeviceStatus): string {
  switch (status) {
    case 'online':
      return 'govuk-tag govuk-tag--green';
    case 'offline':
      return 'govuk-tag govuk-tag--red';
    case 'degraded':
      return 'govuk-tag govuk-tag--yellow';
    default:
      return 'govuk-tag govuk-tag--grey';
  }
}

export function formatStatusLabel(status: DeviceStatus): string {
  return status.charAt(0).toUpperCase() + status.slice(1);
}

export function formatMetricValue(value: unknown): string {
  if (value === null || value === undefined) return '—';
  if (typeof value === 'boolean') return value ? 'Yes' : 'No';
  if (typeof value === 'number') return value.toString();
  return String(value);
}

export function formatMetricKey(key: string): string {
  return key
    .replace(/[_-]+/g, ' ')
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

export function formatTimestamp(value?: string): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString('en-GB', {
    day: '2-digit',
    month: 'short',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}
