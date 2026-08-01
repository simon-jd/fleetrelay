import { useCallback, useEffect, useRef, useState } from 'react'
import { createEnvelope, listDevices, websocketUrl } from './api'
import type { Command, ConnectionState, Device, Envelope } from './types'

const CURSOR_KEY = 'fleetrelay.last-event-cursor'

function mergeDevice(devices: Device[], incoming: Partial<Device> & { device_id: string }): Device[] {
  const index = devices.findIndex((device) => device.device_id === incoming.device_id)
  if (index === -1) {
    return [...devices, { display_name: incoming.device_id, state: 'OFFLINE', capabilities: [], ...incoming }]
  }
  return devices.map((device, current) => current === index ? { ...device, ...incoming } : device)
}

export function useFleetRelay(token: string) {
  const [devices, setDevices] = useState<Device[]>([])
  const [commands, setCommands] = useState<Command[]>([])
  const [connection, setConnection] = useState<ConnectionState>('connecting')
  const [error, setError] = useState<string>()
  const socketRef = useRef<WebSocket | undefined>(undefined)
  const reconnectAttempt = useRef(0)
  const stopped = useRef(false)

  const send = useCallback((type: string, payload: unknown, deviceId?: string) => {
    const socket = socketRef.current
    if (!socket || socket.readyState !== WebSocket.OPEN) throw new Error('Realtime connection is not ready')
    socket.send(JSON.stringify(createEnvelope(type, payload, deviceId)))
  }, [])

  useEffect(() => {
    stopped.current = false
    const controller = new AbortController()
    listDevices(token, controller.signal).then(setDevices).catch((cause: Error) => {
      if (cause.name !== 'AbortError') setError(cause.message)
    })

    let timer: number | undefined
    const connect = () => {
      setConnection(reconnectAttempt.current ? 'recovering' : 'connecting')
      const socket = new WebSocket(websocketUrl())
      socketRef.current = socket
      socket.addEventListener('open', () => {
        reconnectAttempt.current = 0
        setConnection('online')
        setError(undefined)
        socket.send(JSON.stringify(createEnvelope('authenticate', { token })))
        socket.send(JSON.stringify(createEnvelope('subscribe', { topics: ['fleet', 'alerts', 'commands', 'leases'] })))
        const cursor = localStorage.getItem(CURSOR_KEY)
        if (cursor) socket.send(JSON.stringify(createEnvelope('resume', { last_event_cursor: cursor })))
      })
      socket.addEventListener('message', ({ data }) => {
        try {
          const event = JSON.parse(String(data)) as Envelope<Record<string, unknown>>
          if (event.event_cursor) localStorage.setItem(CURSOR_KEY, event.event_cursor)
          if (event.type === 'snapshot') {
            const snapshot = event.payload.devices as Device[] | undefined
            if (snapshot) setDevices(snapshot)
          } else if (['device.event', 'telemetry.update', 'lease.changed'].includes(event.type) && event.device_id) {
            setDevices((current) => mergeDevice(current, { device_id: event.device_id!, ...event.payload }))
          } else if (event.type === 'command.status') {
            const command = event.payload as unknown as Command
            setCommands((current) => [command, ...current.filter((item) => item.command_id !== command.command_id)].slice(0, 20))
          } else if (event.type === 'error') {
            setError(String(event.payload.message ?? 'Server rejected the request'))
          }
        } catch {
          setError('Received an invalid realtime message')
        }
      })
      socket.addEventListener('close', (event) => {
        if (stopped.current) return
        setConnection('recovering')
        if (event.code === 1013) setError('Client fell behind; reconnecting to refresh current state (event history may be missing)')
        const delay = Math.min(10_000, 500 * 2 ** reconnectAttempt.current++) + Math.random() * 250
        timer = window.setTimeout(connect, delay)
      })
      socket.addEventListener('error', () => socket.close())
    }
    connect()
    return () => {
      stopped.current = true
      controller.abort()
      window.clearTimeout(timer)
      socketRef.current?.close(1000, 'console unmounted')
    }
  }, [token])

  return { devices, commands, connection, error, setError, send }
}
