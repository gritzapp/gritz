import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {taskRunnerLayout} from '../../layouts/taskrunner';
import {Colors} from '../../theme';

// Pruning an archived task, traced from
// proposals/draft/taskrunner-owns-sandbox.md (Retire and Prune): the archived
// check runs outside tr.mu, Retire destroys the parked sandbox and retires the
// TaskRunner, the registry drops it, and a Start racing it gets ErrRetired.
export default makeScene2D(function* (view) {
  const a = taskRunnerLayout(view, 'Prune: Retire, then drop');
  const {links} = a;

  a.registry.putRow('42', 'TaskRunner');
  a.state.putRow('sb', 'c1');
  a.state.putRow('running', 'false');
  a.state.putRow('retired', 'false');
  a.taskstate.putRow('type', 'docker');
  a.taskstate.putRow('id', 'c1');
  a.taskstate.putRow('version', '3');
  a.taskstate.putRow('seq', '212');
  a.sandbox.subtitle('c1 · parked');
  a.driver.activity('');
  a.wal.putRow('last', '212');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'Prune walks a snapshot of the registry. For task 42 it calls GetTask without holding tr.mu.',
  );
  yield* a.manager.updateActivity('Prune: stale(42)?');
  yield* links.managerApi.rpc('GetTask(42)', 'archived', {
    color: Colors.runner,
  });

  yield* a.caption.show(
    2,
    'tr.Retire locks tr.mu. With no run claimed and the sandbox parked, retiring is safe.',
  );
  yield* links.calls.send('Retire()', {color: Colors.runner});
  yield* a.taskRunner.updateActivity('Retire · tr.mu held');
  yield* a.state.flash('running');
  yield* links.backend.rpc('Status', 'StatusParked', {color: Colors.runner});

  yield* a.caption.show(
    3,
    'It destroys the sandbox, clears the record and sb, and sets retired. Start will refuse from now on.',
  );
  yield* links.backend.send('Destroy', {color: Colors.runner});
  yield* all(a.sandbox.opacity(0, 0.5));
  yield* all(
    a.taskstate.dropRow('type'),
    a.taskstate.dropRow('id'),
    a.taskstate.dropRow('version'),
    a.taskstate.dropRow('seq'),
    a.state.setRow('sb', 'nil'),
  );
  yield* a.state.setRow('retired', 'true');
  yield* links.calls.send('true', {back: true, color: Colors.runner});

  yield* a.caption.show(
    4,
    'Prune deletes the entry under m.mu, only if m.tasks[42] is still this TaskRunner.',
  );
  yield* a.manager.updateActivity('Prune: drop(42)');
  yield* a.registry.dropRow('42');

  yield* a.caption.show(
    5,
    'Race: the task was unarchived and started, and a Poll goroutine still holds the old pointer.',
  );
  yield* a.manager.updateActivity('Poll: Start(42, v4)');
  yield* links.calls.send('Start(spec v4)', {color: Colors.runner});

  yield* a.caption.show(
    6,
    'Start returns ErrRetired and creates nothing. The manager gives the slot back and emits no failed.',
  );
  yield* a.state.flash('retired');
  yield* links.calls.send('ErrRetired', {back: true, color: Colors.runner});
  yield* a.outbox.flash();

  yield* a.caption.show(
    7,
    'The START command survives, so the next poll creates a fresh TaskRunner and its Start creates a sandbox.',
  );
  yield* a.manager.updateActivity('next Poll: m.taskRunner(42)');
  yield* a.registry.addRow('42', 'TaskRunner (new)');
  yield* all(
    a.state.setRow('retired', 'false'),
    a.state.setRow('sb', 'nil'),
    a.taskRunner.updateActivity(''),
  );
  yield* waitFor(2);
});
