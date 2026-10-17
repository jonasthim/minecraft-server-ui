'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const { EventEmitter } = require('node:events')
const { argumentsFor, attach } = require('./client.cjs')

function fixture (mode = 'observe') {
  const client = new EventEmitter()
  const writes = []; const emitted = []; const failures = []
  client.write = (name, value) => writes.push({ name, value })
  const state = attach(client, mode, v => emitted.push(v), e => failures.push(e))
  return { client, writes, emitted, failures, state }
}

test('fixed CLI rejects caller-controlled endpoints and versions', () => {
  assert.equal(argumentsFor(['--mode', 'mark-a']), 'mark-a')
  for (const args of [[], ['--mode', 'command'], ['--host', 'example.org'], ['--mode', 'observe', '--version', '1.21.9']]) {
    assert.throws(() => argumentsFor(args), /client_arguments/)
  }
})
test('login alone or empty status never qualifies; play/chunks/teleport/keepalive/batch required', t => {
  const f = fixture(); t.after(f.state.stop)
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
  const f = fixture('mark-b'); t.after(f.state.stop)
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
  const f = fixture(); t.after(f.state.stop)
  f.client.emit('position', { yaw: NaN, teleportId: 9 })
  f.client.emit('position', { yaw: 0, teleportId: 9, flags: 8 })
  f.client.emit('map_chunk', { chunkData: Buffer.alloc(0) })
  f.client.emit('error', new Error('secret-server-body'))
  assert.deepEqual(f.failures, ['client_position_invalid', 'client_initial_relative_yaw', 'client_empty_chunk', 'client_protocol_error'])
})
test('locked offline client imports and exact 1.21.9 packets serialize without a connection', () => {
  const mc = require('minecraft-protocol')
  const serializer = mc.createSerializer({ state: 'play', version: '1.21.9' })
  assert.equal(typeof mc.createClient, 'function')
  for (const [name, params] of [
    ['teleport_confirm', { teleportId: 7 }],
    ['look', { yaw: 31.25, pitch: 0, flags: { onGround: false, hasHorizontalCollision: false } }],
    ['player_loaded', {}], ['chunk_batch_received', { chunksPerTick: 8 }], ['tick_end', {}]
  ]) assert.ok(serializer.createPacketBuffer({ name, params }).length > 0)
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
  assert.ok(fs.readFileSync(require.resolve('yggdrasil/src/Client.js'), 'utf8').includes('uuid.v4()'))
})
