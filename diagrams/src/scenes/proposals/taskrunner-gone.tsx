import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {taskRunnerLayout} from '../../layouts/taskrunner';
import {Colors} from '../../theme';

// A sandbox that vanishes mid-run, traced from
// proposals/draft/taskrunner-owns-sandbox.md (Run, Events, Start): attach
// reports the run lost through the events channel and keeps the record; the
// next START clears the gone sandbox and fails with ErrGone, as today; the
// START after that creates a fresh sandbox.
export default makeScene2D(function* (view) {
  const a = taskRunnerLayout(view, 'A gone sandbox: lost, failed, then fresh');
  const {links} = a;

  a.registry.putRow('42', 'TaskRunner');
  a.state.putRow('sb', 'c1');
  a.state.putRow('running', 'true');
  a.taskstate.putRow('type', 'docker');
  a.taskstate.putRow('id', 'c1');
  a.taskstate.putRow('version', '1');
  a.taskstate.putRow('seq', '57');
  a.sandbox.subtitle('c1 · up');
  a.driver.activity('run v1');
  a.wal.putRow('last', '57');
  a.taskRunner.activity('Run: attach v1');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'Run v1 is attached when the sandbox disappears (someone ran docker rm). The stream drops.',
  );
  yield* all(
    a.sandbox.subtitle('c1 · gone', 0.4),
    a.driver.updateActivity(''),
    a.sandbox.opacity(0.35, 0.6),
  );

  yield* a.caption.show(
    2,
    'attach checks Status, gets StatusGone and reports the run lost. It does not clear the sandbox.',
  );
  yield* links.backend.rpc('Status', 'StatusGone', {color: Colors.runner});
  yield* a.taskstate.flash();

  yield* a.caption.show(
    3,
    'Run sends TaskLostEvent{42, v1}, selecting on ctx.Done. The send is unbuffered, made without tr.mu.',
  );
  yield* links.events.send('TaskLostEvent v1', {color: Colors.runner});
  yield* a.events.addRow('lost', '42 · v1');

  yield* a.caption.show(
    4,
    'handleEvents enqueues the backstop failed(v1). It never takes a TaskRunner lock.',
  );
  yield* a.manager.updateActivity('handleEvents');
  yield* all(a.events.dropRow('lost'), a.outbox.addRow('failed', '42 · v1'));
  yield* a.state.setRow('running', 'false');
  yield* a.taskRunner.updateActivity('');

  yield* a.caption.show(
    5,
    'The record stays, so if that event had been dropped at shutdown, Load’s backstop would still find it.',
  );
  yield* a.taskstate.flash();
  yield* links.managerApi.send('SubmitRunnerEvents', {color: Colors.runner});
  yield* a.outbox.dropRow('failed');

  yield* a.caption.show(
    6,
    'Later, START v2. Start stamps the record with v2 and calls sb.Start, which returns ErrGone.',
  );
  yield* a.manager.updateActivity('Start(42, v2)');
  yield* links.calls.send('Start(spec v2)', {color: Colors.runner});
  yield* a.taskRunner.updateActivity('Start · tr.mu held');
  yield* a.taskstate.setRow('version', '2');
  yield* links.backend.rpc('Start', 'ErrGone', {color: Colors.runner});

  yield* a.caption.show(
    7,
    'Start clears the gone sandbox (record removed, sb = nil) and returns ErrGone. The TaskRunner stays.',
  );
  yield* all(
    a.taskstate.dropRow('type'),
    a.taskstate.dropRow('id'),
    a.taskstate.dropRow('version'),
    a.taskstate.dropRow('seq'),
    a.state.setRow('sb', 'nil'),
    a.sandbox.opacity(0, 0.5),
    a.wal.dropRow('last'),
  );
  yield* links.calls.send('ErrGone', {back: true, color: Colors.runner});
  yield* a.taskRunner.updateActivity('');

  yield* a.caption.show(
    8,
    'As today, the manager gives the slot back and reports failed(v2): the user learns the workspace is gone.',
  );
  yield* a.outbox.addRow('failed', '42 · v2');
  yield* links.managerApi.send('SubmitRunnerEvents', {color: Colors.runner});
  yield* a.outbox.dropRow('failed');

  yield* a.caption.show(
    9,
    'The next START (v3) finds sb == nil, so Start creates a fresh sandbox and records it at v3 with Seq 0.',
  );
  yield* a.manager.updateActivity('Start(42, v3)');
  yield* links.calls.send('Start(spec v3)', {color: Colors.runner});
  yield* a.taskRunner.updateActivity('Start · tr.mu held');
  yield* links.backend.send('Create(spec)', {color: Colors.runner});
  a.sandbox.subtitle('c2');
  a.driver.activity('');
  a.sandbox.scale(0.92);
  yield* all(a.sandbox.opacity(1, 0.5), a.sandbox.scale(1, 0.5));
  yield* a.state.setRow('sb', 'c2');
  yield* a.taskstate.addRow('type', 'docker');
  yield* all(
    a.taskstate.addRow('id', 'c2'),
    a.taskstate.addRow('version', '3'),
    a.taskstate.addRow('seq', '0'),
  );
  yield* links.backend.send('Start', {color: Colors.runner});
  yield* all(
    a.sandbox.subtitle('c2 · up', 0.4),
    a.state.setRow('running', 'true'),
  );
  yield* a.taskRunner.updateActivity('Run: attach v3');
  yield* links.backend.rpc('Run(v3, spec)', 'Accepted', {
    color: Colors.runner,
  });
  yield* a.driver.updateActivity('run v3');
  yield* waitFor(2);
});
