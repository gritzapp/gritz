import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {taskRunnerLayout} from '../../layouts/taskrunner';
import {Colors} from '../../theme';

// A runner restart with task 42's sandbox still up, traced from
// proposals/draft/taskrunner-owns-sandbox.md (Restore, Events, Driver WAL):
// the command starts HandleEvents before Load, Restore adopts the record and claims the run in
// one lock acquisition, and Run reattaches from the record's Seq.
export default makeScene2D(function* (view) {
  const a = taskRunnerLayout(view, 'Load: Restore claims the run');
  const {links} = a;

  a.taskstate.putRow('type', 'docker');
  a.taskstate.putRow('id', 'c1');
  a.taskstate.putRow('version', '1');
  a.taskstate.putRow('seq', '57');
  a.sandbox.subtitle('c1 · up');
  a.driver.activity('run v1');
  a.wal.putRow('first', '58');
  a.wal.putRow('last', '64');
  a.manager.activity('booting');
  a.taskRunner.opacity(0);
  links.calls.opacity(0);
  links.events.opacity(0);
  links.backend.opacity(0);

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'The runner restarted. The driver kept running v1 and appending to its WAL; only taskstate/ survived.',
  );
  yield* all(a.taskstate.flash(), a.wal.setRow('last', '71'));

  yield* a.caption.show(
    2,
    'The command starts go m.HandleEvents(ctx) before Load, on the root ctx the senders also use.',
  );
  yield* a.manager.updateActivity('go HandleEvents(ctx)');
  yield* a.events.flash();

  yield* a.caption.show(
    3,
    'For each record, get-or-create its TaskRunner. It starts with no sandbox.',
  );
  yield* a.manager.updateActivity('Load: m.taskRunner(42)');
  yield* a.registry.addRow('42', 'TaskRunner');
  yield* all(a.taskRunner.opacity(1, 0.4), links.calls.opacity(1, 0.4));
  yield* all(
    a.state.addRow('sb', 'nil'),
    a.state.addRow('running', 'false'),
  );

  yield* a.caption.show(
    4,
    'tr.Restore(rec) locks tr.mu, opens the handle with backend.Open and adopts the record, Seq included.',
  );
  yield* links.calls.send('Restore(rec)', {color: Colors.runner});
  yield* a.taskRunner.updateActivity('Restore · tr.mu held');
  yield* all(links.backend.opacity(1, 0.3), links.events.opacity(1, 0.3));
  yield* a.state.setRow('sb', 'c1');

  yield* a.caption.show(
    5,
    'Status says StatusUp, so in the same lock acquisition it claims the run. Nothing can start in between.',
  );
  yield* links.backend.rpc('Status', 'StatusUp', {color: Colors.runner});
  yield* a.state.setRow('running', 'true');
  yield* links.calls.send('StatusUp', {back: true, color: Colors.runner});

  yield* a.caption.show(
    6,
    'The manager counts the slot and runs tr.Run(ctx, nil), which reattaches to v1 with after_seq = 57.',
  );
  yield* a.manager.updateActivity('go tr.Run(ctx, nil)');
  yield* a.taskRunner.updateActivity('Run: attach v1');
  yield* links.backend.send('Run(v1, after_seq 57)', {color: Colors.runner});
  yield* links.backend.send('seq 58…71, live', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    7,
    'attach advances Seq under tr.mu, only while tr.sb is still c1, then calls Trim outside the lock.',
  );
  yield* a.taskstate.setRow('seq', '71');
  yield* links.backend.send('Trim(71)', {color: Colors.runner});
  yield* a.wal.setRow('first', '71');

  yield* a.caption.show(
    8,
    'A parked or gone record would get the backstop instead: failed(rec.Version) if the task is still RUNNING.',
  );
  yield* a.outbox.flash();
  yield* waitFor(2);
});
