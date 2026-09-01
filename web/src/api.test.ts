import { describe, expect, it, vi } from 'vitest'
import { createEnvelope } from './api'

describe('createEnvelope', () => {
  it('creates the versioned websocket contract', () => {
    vi.spyOn(crypto, 'randomUUID').mockReturnValue('00000000-0000-4000-8000-000000000000')
    const envelope = createEnvelope('lease.acquire', { ttl_seconds: 30 }, 'vacuum-floor-2')
    expect(envelope).toMatchObject({
      schema_version: 1,
      message_id: '00000000-0000-4000-8000-000000000000',
      type: 'lease.acquire',
      device_id: 'vacuum-floor-2',
      payload: { ttl_seconds: 30 },
    })
    expect(Number.isNaN(Date.parse(envelope.sent_at))).toBe(false)
  })
})
