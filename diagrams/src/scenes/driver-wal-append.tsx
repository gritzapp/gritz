import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {driverWALLayout} from '../layouts/driverwal';
import {Colors} from '../theme';

// Append and live follow, traced from proposals/draft/driver-wal.md:
//
//   "Package internal/runner/wal"   Append, fsync, the durable mark, Changed
//   "Run, Status and Trim"          the Run loop and Trim
//   "Wiring"                        walClient behind logship and SubmitRunnerEvents
//   "Run record and boot ..."       Finished, then {version, finished}
//
// The final design: the runner forwards and the driver does not report
// directly (Accepted.reports_directly is false).
export default makeScene2D(function* (view) {
  const a = driverWALLayout(view, 'Driver WAL · append and follow');
  const {links} = a;

  a.wal.putRow('38', 'runner_event started v1');
  a.wal.putRow('39', 'log_chunk v1 · 32 KiB');
  a.wal.putRow('40', 'log_chunk v1 · 32 KiB');
  a.record.putRow('version', '1');
  a.record.putRow('state', 'running');
  a.taskstate.putRow('version', '1');
  a.taskstate.putRow('seq', '40');
  a.runner.activity('Run stream open');
  a.driver.activity('last = 40');
  a.agent.activity('running');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'The agent writes output; logship masks it and cuts a 32 KiB chunk, as it does today.',
  );
  yield* a.agent.pulse();
  yield* links.agentDriver.send('32 KiB', {color: Colors.driver});
  yield* a.driver.updateActivity('logship: cut chunk');

  yield* a.caption.show(
    2,
    'walClient.AppendLogChunk appends it: Write fsyncs before it returns, then the durable mark moves to 41.',
  );
  yield* links.driverWal.send('log_chunk v1', {color: Colors.driver});
  yield* a.wal.addRow('41', 'log_chunk v1 · 32 KiB');
  yield* a.driver.updateActivity('last = 41');

  yield* a.caption.show(
    3,
    'Append closes Changed(); the Run loop wakes, reads entry 41 and sends it with seq = 41.',
  );
  yield* a.wal.flash('41');
  yield* links.runnerDriver.send('seq 41 · log_chunk', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    4,
    'The runner stores the chunk with AppendLogChunk, then writes Seq = 41 to its taskstate record.',
  );
  yield* a.runner.updateActivity('forward 41');
  yield* links.runnerApi.rpc('AppendLogChunk', 'ok', {color: Colors.runner});
  yield* a.taskstate.setRow('seq', '41');

  yield* a.caption.show(
    5,
    'After the Seq write it calls Trim(41): TruncateFront drops 38–40 and keeps the last entry.',
  );
  yield* links.runnerDriver.rpc('Trim(after_seq 41)', 'first_seq 41', {
    color: Colors.runner,
  });
  yield* a.wal.dropRow('38');
  yield* a.wal.dropRow('39');
  yield* a.wal.dropRow('40');

  yield* a.caption.show(
    6,
    'The agent exits: stopped, the log tail, then Finished once the shipper has stopped; run.json last.',
  );
  yield* a.agent.updateActivity('exited');
  yield* links.driverWal.send('stopped v1', {color: Colors.driver});
  yield* a.wal.addRow('42', 'runner_event stopped v1');
  yield* a.wal.addRow('43', 'log_chunk v1 · 3 KiB');
  yield* a.wal.addRow('44', 'finished v1');
  yield* a.driver.updateActivity('last = 44');
  yield* links.driverRecord.send('{1, finished}', {color: Colors.driver});
  yield* a.record.setRow('state', 'finished');

  yield* a.caption.show(
    7,
    'The stream sends 42–44 and ends after Finished(v1); the runner enqueues stopped on its outbox.',
  );
  yield* links.runnerDriver.send('seq 42 · stopped', {back: true, color: Colors.driver});
  yield* a.outbox.addRow('#7', 'stopped v1');
  yield* a.taskstate.setRow('seq', '42');
  yield* links.runnerDriver.send('seq 43, 44', {back: true, color: Colors.driver});
  yield* links.runnerApi.rpc('AppendLogChunk', 'ok', {color: Colors.runner});
  yield* a.taskstate.setRow('seq', '44');
  yield* a.runner.updateActivity('Finished v1');

  yield* a.caption.show(
    8,
    'The outbox delivers stopped; Trim(44) leaves only Finished(v1), which retention never removes.',
  );
  yield* links.runnerApi.rpc('SubmitRunnerEvents', 'ok', {color: Colors.runner});
  yield* a.outbox.dropRow('#7');
  yield* links.runnerDriver.rpc('Trim(after_seq 44)', 'first_seq 44', {
    color: Colors.runner,
  });
  yield* all(a.wal.dropRow('41'), a.wal.dropRow('42'), a.wal.dropRow('43'));
  yield* a.runner.updateActivity('idle');
  yield* waitFor(2);
});
