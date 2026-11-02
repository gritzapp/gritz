import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {driverWALLayout} from '../layouts/driverwal';
import {Colors} from '../theme';

// Driver crash, tail repair and boot reconciliation, traced from
// proposals/draft/driver-wal.md:
//
//   "Package internal/runner/wal"     Open and repair, CRC framing
//   "Run record and boot ..."         the reconciliation table
//   "Sequence numbers"                a cut index is reused safely
export default makeScene2D(function* (view) {
  const a = driverWALLayout(view, 'Driver WAL · driver crash and reconcile');
  const {links} = a;

  a.wal.putRow('50', 'runner_event started v2');
  a.wal.putRow('51', 'log_chunk v2 · 32 KiB');
  a.record.putRow('version', '2');
  a.record.putRow('state', 'running');
  a.taskstate.putRow('version', '2');
  a.taskstate.putRow('seq', '51');
  a.runner.activity('Run stream open');
  a.driver.activity('last = 51');
  a.agent.activity('running');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'The driver is appending entry 52 when it is killed: half the record reaches the file, no fsync returns.',
  );
  yield* links.driverWal.send('log_chunk v2', {color: Colors.driver});
  yield* a.wal.addRow('52', 'torn · ½ record');
  yield* a.wal.flash('52', Colors.bad);
  yield* all(
    a.driver.updateActivity('killed'),
    a.agent.updateActivity('killed'),
    a.sandbox.subtitle('gritz-42 · driver restarting', 0.4),
  );

  yield* a.caption.show(
    2,
    'Append never returned, so the mark stayed at 51 and 52 was never streamed. The runner retries Run.',
  );
  yield* a.runner.updateActivity('reattaching');
  yield* a.taskstate.flash('seq');

  yield* a.caption.show(
    3,
    'On boot wal.Open scans the tail segment and stops at the first incomplete or bad-CRC record.',
  );
  yield* a.driver.updateActivity('wal.Open: scan tail');
  yield* a.wal.flash('52', Colors.bad);

  yield* a.caption.show(
    4,
    'It truncates the file there and fsyncs it. Index 52 is free again; no reader ever saw the old one.',
  );
  yield* a.wal.dropRow('52');
  yield* a.driver.updateActivity('last = 51');

  yield* a.caption.show(
    5,
    'run.json says v2 is running and the last entry is not Finished(v2): the driver died mid-run.',
  );
  yield* a.record.flash('state');
  yield* a.wal.flash('51');

  yield* a.caption.show(
    6,
    'Before serving it appends failed and Finished for v2, then rewrites run.json as finished.',
  );
  yield* links.driverWal.send('failed v2', {color: Colors.driver});
  yield* a.wal.addRow('52', 'runner_event failed v2');
  yield* a.wal.addRow('53', 'finished v2');
  yield* links.driverRecord.send('{2, finished}', {color: Colors.driver});
  yield* all(
    a.record.setRow('state', 'finished'),
    a.driver.updateActivity('serving · last = 53'),
    a.sandbox.subtitle('gritz-42 · running', 0.4),
  );

  yield* a.caption.show(
    7,
    'The runner reattaches with Run(v2, after_seq: 51) and gets 52 and 53 like any other outcome.',
  );
  yield* links.runnerDriver.send('Run(v2, after_seq 51)', {color: Colors.runner});
  yield* links.runnerDriver.send('seq 52 failed · 53 Finished', {
    back: true,
    color: Colors.driver,
  });
  yield* a.outbox.addRow('#8', 'failed v2');
  yield* a.taskstate.setRow('seq', '53');
  yield* links.runnerApi.rpc('SubmitRunnerEvents', 'ok', {color: Colors.runner});
  yield* a.outbox.dropRow('#8');

  yield* a.caption.show(
    8,
    'Had it died after Finished(v2) was durable but before run.json, boot would only rewrite run.json.',
  );
  yield* a.record.flash();
  yield* a.runner.updateActivity('idle');
  yield* waitFor(2);
});
