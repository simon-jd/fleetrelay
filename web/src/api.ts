import type { Device, Envelope } from './types'

const API_BASE = import.meta.env.VITE_API_BASE_URL ?? '/api/v1'

export async function listDevices(token: string, signal?: AbortSignal): Promise<Device[]> {
  const response = await fetch(`${API_BASE}/devices`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    signal,
  })
  if (!response.ok) throw new Error(`Device query failed (${response.status})`)
  const body = (await response.json()) as Device[] | { devices: Device[] }
  return Array.isArray(body) ? body : body.devices
}

export function createEnvelope<T>(type: string, payload: T, deviceId?: string): Envelope<T> {
  return {
    schema_version: 1,
    message_id: crypto.randomUUID(),
    type,
    device_id: deviceId,
    sent_at: new Date().toISOString(),
    payload,
  }
}

export function websocketUrl(): string {
  const configured = import.meta.env.VITE_WS_URL as string | undefined
  if (configured) return configured
  const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${scheme}//${window.location.host}/ws`
}
