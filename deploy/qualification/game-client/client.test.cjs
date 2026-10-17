'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const { EventEmitter } = require('node:events')
const { argumentsFor, attach } = require('./client.cjs')

function fixture (mode = 'observe', version = '1.21.9') {
  const client = new EventEmitter()
  const writes = []; const emitted = []; const failures = []
  client.write = (name, value) => writes.push({ name, value })
  const state = attach(client, mode, version, v => emitted.push(v), e => failures.push(e))
  return { client, writes, emitted, failures, state }
}

test('fixed CLI accepts only exact mode/version tuple arguments and no endpoint', () => {
  assert.deepEqual(argumentsFor(['--mode', 'mark-a', '--version', '1.12.2']), { mode: 'mark-a', version: '1.12.2' })
  assert.deepEqual(argumentsFor(['--mode', 'observe', '--version', '1.21.9']), { mode: 'observe', version: '1.21.9' })
  for (const args of [[], ['--mode', 'command', '--version', '1.12.2'], ['--mode', 'observe', '--version', '1.20.1'],
    ['--host', 'example.org', '--version', '1.12.2'], ['--mode', 'observe', '--version', '1.21.9', '--host', '127.0.0.1'],
    ['--mode', 'observe', '--version', '1.21.9', '--version', '1.12.2']]) {
    assert.throws(() => argumentsFor(args), /client_arguments/)
  }
})
test('login alone or empty status never qualifies; play/chunks/teleport/keepalive/batch required', t => {
  const f = fixture('observe', '1.21.9'); t.after(f.state.stop)
  f.client.emit('playerJoin')
  f.client.emit('position', { yaw: 31.25, teleportId: 7, flags: 0 })
  f.client.emit('map_chunk', { chunkData: Buffer.from([1]) })
  f.client.emit('keep_alive', {})
  assert.equal(f.emitted.length, 0)
  f.client.emit('chunk_batch_finished')
  assert.equal(f.emitted.length, 1)
  assert.equal(f.emitted[0].initial_yaw, 31.25)
  assert.deepEqual(f.writes.map(v => v.name), ['teleport_confirm', 'player_loaded', 'chunk_batch_received'])
})
test('marker uses gameplay rotation, and report keeps server yaw distinct', t => {
  const f = fixture('mark-b', '1.21.9'); t.after(f.state.stop)
  f.client.emit('playerJoin')
  f.client.emit('position', { yaw: 31.25, teleportId: 9, flags: 0 })
  f.client.emit('map_chunk', { chunkData: Buffer.from([1]) })
  f.client.emit('chunk_batch_finished')
  f.client.emit('keep_alive', {})
  assert.equal(f.writes.find(v => v.name === 'look').value.yaw, 121.25)
  assert.equal(f.emitted[0].initial_yaw, 31.25)
  assert.equal(f.emitted[0].sent_yaw, 121.25)
})
test('invalid position/chunk and protocol failure emit fixed failure codes', t => {
  const f = fixture('observe', '1.21.9'); t.after(f.state.stop)
  f.client.emit('position', { yaw: NaN, teleportId: 9 })
  f.client.emit('position', { yaw: 0, teleportId: 9, flags: 8 })
  f.client.emit('map_chunk', { chunkData: Buffer.alloc(0) })
  f.client.emit('error', new Error('secret-server-body'))
  assert.deepEqual(f.failures, ['client_position_invalid', 'client_initial_relative_yaw', 'client_empty_chunk', 'client_protocol_error'])
})
test('legacy 1.12.2 enters play, acknowledges position, observes chunks and changes yaw without modern packets', t => {
  const f = fixture('mark-b', '1.12.2'); t.after(f.state.stop)
  f.client.emit('playerJoin')
  f.client.emit('position', { x: 0, y: 64, z: 0, yaw: 31.25, pitch: 0, teleportId: 7, flags: 0 })
  f.client.emit('map_chunk', { chunkData: Buffer.from([1]) })
  f.client.emit('keep_alive', { keepAliveId: 1n })
  assert.equal(f.emitted.length, 1)
  assert.equal(f.emitted[0].version, '1.12.2')
  assert.equal(f.emitted[0].protocol_data, 'minecraft-data@3.117.0')
  assert.deepEqual(f.emitted[0].protocol_capabilities, { player_loaded: false, chunk_batch: false, tick_end: false })
  assert.equal(f.emitted[0].chunks, 1)
  assert.equal(f.emitted[0].teleports, 1)
  assert.equal(f.emitted[0].keepalives, 1)
  assert.equal(Object.hasOwn(f.emitted[0], 'chunk_batches'), false)
  assert.deepEqual(f.writes.map(v => v.name), ['teleport_confirm', 'look'])
  assert.equal(f.writes[1].value.yaw, 121.25)
  assert.equal(f.writes[1].value.onGround, false)
})

test('locked serializers accept modern batch packets and legacy packet shapes without a connection', () => {
  const mc = require('minecraft-protocol')
  const mcData = require('minecraft-data')
  assert.equal(typeof mc.createClient, 'function')
  const modern = mc.createSerializer({ state: 'play', version: '1.21.9' })
  const legacy = mc.createSerializer({ state: 'play', version: '1.12.2' })
  for (const [name, params] of [
    ['teleport_confirm', { teleportId: 7 }],
    ['look', { yaw: 31.25, pitch: 0, flags: { onGround: false, hasHorizontalCollision: false } }],
    ['player_loaded', {}], ['chunk_batch_received', { chunksPerTick: 8 }], ['tick_end', {}]
  ]) assert.ok(modern.createPacketBuffer({ name, params }).length > 0)
  for (const [name, params] of [
    ['teleport_confirm', { teleportId: 7 }], ['look', { yaw: 121.25, pitch: 0, onGround: false }],
    ['keep_alive', { keepAliveId: 1n }]
  ]) assert.ok(legacy.createPacketBuffer({ name, params }).length > 0)
  const legacyMappings = mcData('1.12.2').protocol.play.toServer.types.packet[1][0].type[1].mappings
  for (const name of ['player_loaded', 'chunk_batch_received', 'tick_end']) {
    assert.equal(Object.values(legacyMappings).includes(name), false, `legacy protocol unexpectedly defines ${name}`)
  }
})
test('reviewed uuid override retains CommonJS v4 callsites and has locked integrity', () => {
  const { createRequire } = require('node:module')
  const fs = require('node:fs')
  const lock = require('./package-lock.json')
  assert.equal(lock.packages['node_modules/uuid'].version, '11.1.1')
  assert.equal(lock.packages['node_modules/uuid'].integrity, 'sha512-vIYxrBCC/N/K+Js3qSN88go7kIfNPssr/hHCesKCQNAjmgvYS2oqr69kIufEG+O4+PfezOH4EbIeHCfFov8ZgQ==')
  for (const name of ['yggdrasil', '@azure/msal-node']) {
    const parent = createRequire(require.resolve(name))
    const uuid = parent('uuid')
    assert.equal(uuid.version(uuid.v4()), 4)
    assert.equal(parent('uuid/package.json').version, '11.1.1')
  }
  assert.ok(!Object.values(lock.packages).some(p => p.hasInstallScript))
  const protocol = lock.packages['node_modules/minecraft-protocol']
  assert.equal(protocol.version, '1.68.0')
  assert.equal(protocol.integrity, 'sha512-atAGg/fhIVqyK6voWvED5QA+4ah65PNwpoDnkHawWhVzQbwFii77KcF6OpsU/Xlbi0o9GpihHiPdORSxXEA+Ug==')
  const protocolData = lock.packages['node_modules/minecraft-data']
  assert.equal(protocolData.version, '3.117.0')
  assert.equal(protocolData.integrity, 'sha512-glKv0EEmVwKvJwdNAKAal2P7gi/nijU9rEMp8gJmqYYRVswS6hA0q9566lcGHH07Ocmpqw5bMRbARC25pR13gA==')
  assert.ok(fs.readFileSync(require.resolve('yggdrasil/src/Client.js'), 'utf8').includes('uuid.v4()'))
})
