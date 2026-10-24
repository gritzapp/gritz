import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {taskRunnerLayout} from '../../layouts/taskrunner';
import {Colors} from '../../theme';

// Pruning an archived task, traced from
// proposals/draft/taskrunner-owns-sandbox.md (Retire and Prune,
// TaskManager.Run): Run prunes between Poll passes, the archived check runs
// outside tr.mu, Retire destroys the parked sandbox, and the registry drops
// the entry right away because no Poll handler holds the TaskRunner.
export default makeScene2D(function* (view) {
  const a = taskRunnerLayout(view, 'Prune between Poll passes');
  const {links} = a;

  a.registry.putRow('42', 'TaskRunner');
  a.state.putRow('sb', 'c1');
  a.state.putRow('running', 'false');
  a.taskstate.putRow('type', 'docker');
  a.taskstate.putRow('id', 'c1');
  a.taskstate.putRow('version', '3');
  a.taskstate.putRow('seq', '212');
  a.sandbox.subtitle('c1 · parked');
  a.driver.activity('');
  a.wal.putRow('last', '212');
  a.manager.activity('Run: Poll()');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'TaskManager.Run finishes a Poll pass. Poll waited for its handlers, so none holds a *TaskRunner now.',
  );
  yield* links.managerApi.rpc('ListRunnerTasks', '[]', {color: Colors.runner});
  yield* a.manager.updateActivity('Run: prune ticker fired');

  yield* a.caption.show(
    2,
    'Prune walks a snapshot of the registry. For task 42 it calls GetTask without holding tr.mu.',
  );
  yield* a.manager.updateActivity('Prune: stale(42)?');
  yield* links.managerApi.rpc('GetTask(42)', 'archived', {
    color: Colors.runner,
  });

  yield* a.caption.show(
    3,
    'tr.Retire locks tr.mu and destroys the sandbox if idle: no run claimed, and it is not up.',
  );
  yield* links.calls.send('Retire()', {color: Colors.runner});
  yield* a.taskRunner.updateActivity('Retire · tr.mu held');
  yield* a.state.flash('running');
  yield* links.backend.rpc('Status', 'StatusParked', {color: Colors.runner});

  yield* a.caption.show(
    4,
    'It destroys the sandbox and clears the record and sb, then reports true.',
  );
  yield* links.backend.send('Destroy', {color: Colors.runner});
  yield* a.sandbox.opacity(0, 0.5);
  yield* all(
    a.taskstate.dropRow('type'),
    a.taskstate.dropRow('id'),
    a.taskstate.dropRow('version'),
    a.taskstate.dropRow('seq'),
    a.wal.dropRow('last'),
    a.state.setRow('sb', 'nil'),
  );
  yield* links.calls.send('true', {back: true, color: Colors.runner});
  yield* a.taskRunner.updateActivity('');

  yield* a.caption.show(
    5,
    'Prune deletes the entry under m.mu right away. No flag is needed: nothing else can reach this TaskRunner.',
  );
  yield* a.manager.updateActivity('Prune: delete m.tasks[42]');
  yield* a.registry.dropRow('42');

  yield* a.caption.show(
    6,
    'Run waits for a wake, PollInterval or ctx. A slow Prune (GetTask, Destroy) delays this next poll.',
  );
  yield* a.manager.updateActivity('Run: wait');
  yield* waitFor(0.8);

  yield* a.caption.show(
    7,
    'If the task is later unarchived and started, the next pass’s get-or-create builds a fresh TaskRunner.',
  );
  yield* a.manager.updateActivity('Run: Poll() · m.taskRunner(42)');
  yield* a.registry.addRow('42', 'TaskRunner (new)');
  yield* a.state.setRow('running', 'false');
  yield* waitFor(2);
});
