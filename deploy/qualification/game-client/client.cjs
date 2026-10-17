'use strict'

// No Microsoft credentials, chat commands, arbitrary endpoints, or status pings.
// Protocol plugins own login/configuration and keep_alive replies; this module
// acknowledges supported teleports/chunk batches and changes only view yaw.
const readline = require('node:readline')

const YAW = Object.freeze({ 'mark-a': 31.25, 'mark-b': 121.25 })
const PROTOCOL_DATA = 'minecraft-data@3.117.0'
const VERSION_CAPABILITIES = Object.freeze({
  '1.12.2': Object.freeze({ playerLoaded: false, chunkBatch: false, tickEnd: false }),
  '1.21.9': Object.freeze({ playerLoaded: true, chunkBatch: true, tickEnd: true })
})

function argumentsFor (args) {
  if (args.length !== 4 || args[0] !== '--mode' || !['observe', 'mark-a', 'mark-b'].includes(args[1]) ||
      args[2] !== '--version' || !Object.hasOwn(VERSION_CAPABILITIES, args[3])) {
    throw new Error('client_arguments')
  }
  return Object.freeze({ mode: args[1], version: args[3] })
}

function attach (client, mode, version, emit, fail) {
  const capabilities = VERSION_CAPABILITIES[version]
  if (!Object.hasOwn(VERSION_CAPABILITIES, version) || !['observe', 'mark-a', 'mark-b'].includes(mode)) throw new Error('client_arguments')
  const counts = { chunks: 0, teleports: 0, keepalives: 0, chunk_batches: 0 }
  let initialYaw = null
  let inPlay = false
  let reported = false
  let marked = false
  const write = (name, value) => {
    try { client.write(name, value) } catch { fail('client_packet_write') }
  }
  const report = () => {
    if (reported || !inPlay || initialYaw === null || !counts.chunks || !counts.teleports || !counts.keepalives ||
        (capabilities.chunkBatch && !counts.chunk_batches)) return
    if (!marked && mode !== 'observe') return
    reported = true
    const { chunk_batches, ...baseCounts } = counts
    emit({ event: 'ready', version, protocol_client: 'minecraft-protocol@1.68.0', protocol_data: PROTOCOL_DATA,
      protocol_capabilities: { player_loaded: capabilities.playerLoaded, chunk_batch: capabilities.chunkBatch, tick_end: capabilities.tickEnd },
      initial_yaw: initialYaw, sent_yaw: mode === 'observe' ? null : YAW[mode], ...baseCounts,
      ...(capabilities.chunkBatch ? { chunk_batches } : {}) })
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
    if (capabilities.playerLoaded) write('player_loaded', {})
    if (mode !== 'observe') {
      write('look', capabilities.tickEnd
        ? { yaw: YAW[mode], pitch: 0, flags: { onGround: false, hasHorizontalCollision: false } }
        : { yaw: YAW[mode], pitch: 0, onGround: false })
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
    if (!capabilities.chunkBatch) return fail('client_unexpected_chunk_batch')
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
  const tick = capabilities.tickEnd ? setInterval(() => { if (inPlay) write('tick_end', {}) }, 50) : null
  tick?.unref()
  return { counts, capabilities, stop: () => { if (tick) clearInterval(tick) } }
}

function main () {
  let config
  try { config = argumentsFor(process.argv.slice(2)) } catch { process.stderr.write('client_arguments\n'); process.exitCode = 2; return }
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
    auth: 'offline', version: config.version, keepAlive: true, hideErrors: true,
    checkTimeoutInterval: 35000, clientSettings: { viewDistance: 2, enableServerListing: false } })
  state = attach(client, config.mode, config.version, emit, fail)
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

module.exports = { argumentsFor, attach, VERSION_CAPABILITIES, YAW, PROTOCOL_DATA }
if (require.main === module) main()
