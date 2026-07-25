import { useMemo, useState } from 'react'
import { useFleetRelay } from './useFleetRelay'
import type { Device } from './types'

const COMMANDS = ['START_TASK', 'PAUSE_TASK', 'RESUME_TASK', 'RETURN_HOME', 'CANCEL_TASK', 'QUERY_STATE']

function timeLeft(expiresAt?: string): string {
  if (!expiresAt) return 'available'
  const seconds = Math.max(0, Math.ceil((new Date(expiresAt).getTime() - Date.now()) / 1000))
  return seconds ? `${seconds}s remaining` : 'expired'
}

function DeviceCard({ device, selected, onSelect }: { device: Device; selected: boolean; onSelect: () => void }) {
  return (
    <button className={`device-card ${selected ? 'selected' : ''}`} onClick={onSelect} aria-pressed={selected}>
      <span className="device-title">
        <span className={`status-dot ${device.state.toLowerCase()}`} />
        <span><strong>{device.display_name}</strong><small>{device.floor ?? device.device_id}</small></span>
      </span>
      <span className="telemetry">
        <span><small>Battery</small><strong>{device.battery_percent ?? '—'}{device.battery_percent !== undefined && '%'}</strong></span>
        <span><small>Task</small><strong>{device.task_state ?? 'Idle'}</strong></span>
      </span>
      {device.alert && <span className="alert">⚠ {device.alert}</span>}
    </button>
  )
}

export default function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem('fleetrelay.token') ?? 'dev-operator-token')
  const [selectedId, setSelectedId] = useState<string>()
  const [pending, setPending] = useState<string>()
  const { devices, commands, connection, error, setError, send } = useFleetRelay(token)
  const selected = useMemo(() => devices.find((device) => device.device_id === selectedId) ?? devices[0], [devices, selectedId])
  const onlineCount = devices.filter((device) => device.state === 'ONLINE').length

  const attempt = (action: () => void, label: string) => {
    try {
      setPending(label)
      action()
      window.setTimeout(() => setPending(undefined), 800)
    } catch (cause) {
      setPending(undefined)
      setError(cause instanceof Error ? cause.message : 'Request failed')
    }
  }

  const acquireLease = () => selected && attempt(
    () => send('lease.acquire', { ttl_seconds: 30 }, selected.device_id),
    'ACQUIRE',
  )

  const issueCommand = (commandType: string) => selected && attempt(
    () => send('command.request', {
      command_type: commandType,
      idempotency_key: crypto.randomUUID(),
      lease_id: selected.lease?.lease_id,
      delivery_policy: 'REQUIRE_ONLINE',
      expires_at: new Date(Date.now() + 30_000).toISOString(),
      payload: {},
    }, selected.device_id),
    commandType,
  )

  return (
    <div className="shell">
      <header>
        <div className="brand"><span className="mark">FR</span><span><strong>FleetRelay</strong><small>Operator Console</small></span></div>
        <div className={`connection ${connection}`}><span />{connection}</div>
        <label className="token-field">Operator token
          <input aria-label="Operator token" value={token} onChange={(event) => {
            setToken(event.target.value)
            sessionStorage.setItem('fleetrelay.token', event.target.value)
          }} />
        </label>
      </header>

      <main>
        <section className="hero">
          <div><p className="eyebrow">LIVE FLEET</p><h1>Multi-floor operations</h1><p>Control is fenced by a 30-second lease. Reconnect refreshes current state; event history is not replayed.</p></div>
          <div className="stats"><span><strong>{onlineCount}</strong><small>online</small></span><span><strong>{devices.length}</strong><small>registered</small></span><span><strong>{devices.filter((d) => d.alert).length}</strong><small>alerts</small></span></div>
        </section>

        {error && <div className="error-banner" role="alert"><span>{error}</span><button onClick={() => setError(undefined)}>Dismiss</button></div>}

        <section className="workspace">
          <div className="fleet-panel panel">
            <div className="panel-title"><h2>Fleet</h2><span>real-time</span></div>
            <div className="device-grid">
              {devices.map((device) => <DeviceCard key={device.device_id} device={device} selected={selected?.device_id === device.device_id} onSelect={() => setSelectedId(device.device_id)} />)}
              {!devices.length && <div className="empty"><strong>Waiting for devices</strong><span>Start device-sim or verify Control API readiness.</span></div>}
            </div>
          </div>

          <aside className="control-panel panel">
            <div className="panel-title"><h2>Control</h2><span>{selected?.device_id ?? 'no device'}</span></div>
            {selected ? <>
              <div className="lease-card">
                <div><small>Current lease</small><strong>{selected.lease?.operator_id ?? 'Unclaimed'}</strong><span>{timeLeft(selected.lease?.expires_at)}</span></div>
                <button className="primary" onClick={acquireLease} disabled={connection !== 'online' || pending === 'ACQUIRE'}>{pending === 'ACQUIRE' ? 'Requesting…' : 'Acquire control'}</button>
              </div>
              <div className="command-grid">
                {COMMANDS.map((command) => <button key={command} onClick={() => issueCommand(command)} disabled={connection !== 'online' || pending === command || selected.state !== 'ONLINE'}>{pending === command ? 'Sending…' : command.replace('_', ' ')}</button>)}
              </div>
              <p className="safety-note">Commands require an active lease and online presence. Each request has a unique idempotency key and 30-second expiry.</p>
            </> : <div className="empty"><span>Select an online device to begin.</span></div>}
          </aside>
        </section>

        <section className="activity panel">
          <div className="panel-title"><h2>Command activity</h2><span>last 20 received updates</span></div>
          <div className="activity-table" role="table">
            <div className="activity-row head" role="row"><span>Command</span><span>Device</span><span>Status</span><span>Time</span></div>
            {commands.map((command) => <div className="activity-row" role="row" key={command.command_id}><strong>{command.command_type}</strong><span>{command.device_id}</span><span className={`pill ${command.status.toLowerCase()}`}>{command.status}</span><time>{new Date(command.created_at).toLocaleTimeString()}</time></div>)}
            {!commands.length && <div className="empty"><span>Command status updates will appear here.</span></div>}
          </div>
        </section>
      </main>
      <footer>FleetRelay V1 prototype · development environment only</footer>
    </div>
  )
}
