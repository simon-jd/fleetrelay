export type ConnectionState = 'connecting' | 'online' | 'recovering' | 'offline'
export type DeviceState = 'ONLINE' | 'OFFLINE' | 'RECONNECTING'
export type CommandStatus =
  | 'ACCEPTED'
  | 'PENDING_TRANSPORT'
  | 'DISPATCHED'
  | 'SUCCEEDED'
  | 'FAILED'
  | 'ACK_TIMEOUT'
  | 'EXPIRED'
  | 'REJECTED'

export interface Device {
  device_id: string
  display_name: string
  state: DeviceState
  battery_percent?: number
  task_state?: string
  floor?: string
  alert?: string
  capabilities: string[]
  last_seen_at?: string
  lease?: Lease
}

export interface Lease {
  lease_id: string
  operator_id: string
  lease_epoch: number
  expires_at: string
}

export interface Command {
  command_id: string
  device_id: string
  command_type: string
  status: CommandStatus
  created_at: string
}

export interface Envelope<T = unknown> {
  schema_version: 1
  message_id: string
  type: string
  device_id?: string
  sent_at: string
  event_id?: string
  event_cursor?: string
  payload: T
}
