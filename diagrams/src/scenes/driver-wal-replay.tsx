import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {driverWALLayout} from '../layouts/driverwal';
import {Colors} from '../theme';

// Runner restart and replay, traced from proposals/draft/driver-wal.md:
//
//   "Run, Status and Trim"   position check, TruncateFront, replay, live follow
//   "Sequence numbers"       First()-1 <= after_seq <= Last()
//
// The driver keeps no delivery state: the runner's Seq in taskstate/ is the
// only position.
export default makeScene2D(function* (view) {
  const a = driverWALLayout(view, 'Driver WAL · runner restart and replay');
  const {links} = a;

  a.wal.putRow('41', 'log_chunk v1 · 32 KiB');
  a.wal.putRow('42', 'log_chunk v1 · 32 KiB');
  a.wal.putRow('43', 'log_chunk v1 · 32 KiB');
  a.record.putRow('version', '1');
  a.record.putRow('state', 'running');
  a.taskstate.putRow('version', '1');
  a.taskstate.putRow('seq', '43');
  a.runner.activity('Run stream open');
  a.driver.activity('last = 43');
  a.agent.activity('running');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'The runner has stored everything up to 43 and recorded Seq = 43. Then its process dies.',
  );
  yield* a.taskstate.flash('seq');
  yield* all(a.runner.updateActivity('down'), a.host.subtitle('runner restarting', 0.4));

  yield* a.caption.show(
    2,
    'The run is unaffected: entries 44 and 45 are appended; the next KeepAlive write fails and frees the stream.',
  );
  yield* links.agentDriver.send('output', {color: Colors.driver});
  yield* a.wal.addRow('44', 'log_chunk v1 · 32 KiB');
  yield* links.agentDriver.send('output', {color: Colors.driver});
  yield* a.wal.addRow('45', 'log_chunk v1 · 32 KiB');
  yield* a.driver.updateActivity('last = 45 · no stream');

  yield* a.caption.show(
    3,
    'The new runner process reads Seq = 43 from taskstate/ and reattaches with Run(v1, after_seq: 43).',
  );
  yield* all(a.runner.updateActivity('Load: attach v1'), a.host.subtitle('', 0.4));
  yield* a.taskstate.flash('seq');
  yield* links.runnerDriver.send('Run(v1, after_seq 43)', {color: Colors.runner});

  yield* a.caption.show(
    4,
    'Position check: First − 1 = 40 ≤ 43 ≤ Last = 45. TruncateFront(44) drops what the runner has stored.',
  );
  yield* a.driver.updateActivity('40 ≤ 43 ≤ 45');
  yield* a.wal.dropRow('41');
  yield* a.wal.dropRow('42');
  yield* a.wal.dropRow('43');

  yield* a.caption.show(
    5,
    'Accepted (attached), then the replay of 44 and 45, each forwarded before Seq moves past it.',
  );
  yield* links.runnerDriver.send('Accepted · attached', {back: true, color: Colors.driver});
  yield* links.runnerDriver.send('seq 44, 45', {back: true, color: Colors.driver});
  yield* links.runnerApi.rpc('AppendLogChunk ×2', 'ok', {color: Colors.runner});
  yield* a.taskstate.setRow('seq', '45');

  yield* a.caption.show(
    6,
    'Caught up, the stream waits on Changed(); entry 46 is sent as soon as its Append returns.',
  );
  yield* a.runner.updateActivity('Run stream open');
  yield* links.agentDriver.send('output', {color: Colors.driver});
  yield* a.wal.addRow('46', 'log_chunk v1 · 32 KiB');
  yield* a.driver.updateActivity('last = 46');
  yield* links.runnerDriver.send('seq 46', {back: true, color: Colors.driver});
  yield* links.runnerApi.rpc('AppendLogChunk', 'ok', {color: Colors.runner});
  yield* a.taskstate.setRow('seq', '46');

  yield* a.caption.show(
    7,
    'A position outside [First − 1, Last] means entries are gone: Run returns OutOfRange and the run is lost.',
  );
  yield* a.wal.flash();
  yield* waitFor(2);
});
