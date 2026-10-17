'use strict'

// No Microsoft credentials, chat commands, arbitrary endpoints, or status pings.
// Protocol plugins own login/configuration and keep_alive replies; this module
// acknowledges teleports/chunk batches and changes only the player's view yaw.
const readline = require('node:readline')

const VERSION = '1.21.9'
const YAW = Object.freeze({ 'mark-a': 31.25, 'mark-b': 121.25 })

function argumentsFor (args) {
  if (args.length !== 2 || args[0] !== '--mode' || !['observe', 'mark-a', 'mark-b'].includes(args[1])) {
    throw new Error('client_arguments')
  }
  return args[1]
}

function attach (client, mode, emit, fail) {
  const counts = { chunks: 0, teleports: 0, keepalives: 0, chunk_batches: 0 }
  let initialYaw = null
  let inPlay = false
  let reported = false
  let marked = false
  const write = (name, value) => {
    try { client.write(name, value) } catch { fail('client_packet_write') }
  }
  const report = () => {
    if (reported || !inPlay || initialYaw === null || !counts.chunks || !counts.teleports || !counts.keepalives || !counts.chunk_batches) return
    if (!marked && mode !== 'observe') return
    reported = true
    emit({ event: 'ready', version: VERSION, protocol_client: 'minecraft-protocol@1.68.0',
      initial_yaw: initialYaw, sent_yaw: mode === 'observe' ? null : YAW[mode], ...counts })
  }
  client.on('playerJoin', () => { inPlay = true; report() })
  client.on('position', packet => {
    if (!Number.isFinite(packet.yaw) || !Number.isInteger(packet.teleportId)) return fail('client_position_invalid')
    // Initial login must be absolute. Guessing relative initial state would
    // invalidate persisted-yaw comparison. Later corrections are acknowledged.
    if (initialYaw === null) {
      const relativeYaw = typeof packet.flags === 'number' ? Boolean(packet.flags & 8) : Boolean(packet.flags?.yaw || packet.flags?.yRot)
      if (relativeYaw) return fail('client_initial_relative_yaw')
      initialYaw = packet.yaw
    }
    write('teleport_confirm', { teleportId: packet.teleportId })
    counts.teleports++
    write('player_loaded', {})
    if (mode !== 'observe') {
      write('look', { yaw: YAW[mode], pitch: 0, flags: { onGround: false, hasHorizontalCollision: false } })
      marked = true
    }
    report()
  })
  client.on('map_chunk', packet => {
    if (!Buffer.isBuffer(packet.chunkData) || packet.chunkData.length === 0) return fail('client_empty_chunk')
    counts.chunks++
    report()
  })
  client.on('chunk_batch_finished', () => {
    write('chunk_batch_received', { chunksPerTick: 8 })
    counts.chunk_batches++
    report()
  })
  client.on('keep_alive', () => { counts.keepalives++; report() })
  client.on('error', () => fail('client_protocol_error'))
  client.on('kick_disconnect', () => fail('client_kicked'))
  client.on('disconnect', () => fail('client_disconnected'))
  // Mirror a client tick without inventing physics/position. Rotation is the
  // only gameplay mutation, and reconnect/server playerdata proves acceptance.
  const tick = setInterval(() => { if (inPlay) write('tick_end', {}) }, 50)
  tick.unref()
  return { counts, stop: () => clearInterval(tick) }
}

function main () {
  let mode
  try { mode = argumentsFor(process.argv.slice(2)) } catch { process.stderr.write('client_arguments\n'); process.exitCode = 2; return }
  if (process.env.GITHUB_ACTIONS !== 'true' || process.env.RUNNER_ENVIRONMENT !== 'github-hosted') {
    process.stderr.write('client_hosted_only\n'); process.exitCode = 2; return
  }
  if (Number(process.versions.node.split('.')[0]) < 22) { process.exitCode = 2; return }
  let closing = false
  let failed = false
  let state
  const emit = value => process.stdout.write(JSON.stringify(value) + '\n')
  const fail = code => {
    if (failed || closing) return
    failed = true
    emit({ event: 'failure', code })
    closing = true
    state?.stop()
    client.end()
    setTimeout(() => process.exit(1), 100).unref()
    process.exitCode = 1
  }
  const mc = require('minecraft-protocol')
  // Fixed loopback target; never accept a caller-controlled address or port.
  const client = mc.createClient({ host: '127.0.0.1', port: 25565, username: 'AcceptanceBot',
    auth: 'offline', version: VERSION, keepAlive: true, hideErrors: true,
    checkTimeoutInterval: 35000, clientSettings: { viewDistance: 2, enableServerListing: false } })
  state = attach(client, mode, emit, fail)
  const timeout = setTimeout(() => fail('client_lifetime_timeout'), 180000)
  client.on('end', () => {
    state.stop()
    clearTimeout(timeout)
    if (!closing) fail('client_unexpected_end')
    input.close()
    process.stdin.destroy()
  })
  const input = readline.createInterface({ input: process.stdin, crlfDelay: Infinity })
  input.on('line', line => {
    if (line !== 'close') return fail('client_control_invalid')
    closing = true
    state.stop()
    client.end()
    // A stalled disconnect must not strand a runner or retain private state.
    setTimeout(() => process.exit(failed ? 1 : 0), 2000).unref()
  })
  input.on('close', () => { if (!closing) fail('client_control_eof') })
}

module.exports = { argumentsFor, attach, VERSION, YAW }
if (require.main === module) main()
