import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {taskRunnerLayout} from '../../layouts/taskrunner';
import {Colors} from '../../theme';

// A START for a task with no sandbox, traced from
// proposals/draft/taskrunner-owns-sandbox.md (Start, Registry and the manager,
// Run): get-or-create the TaskRunner, build the spec, then Start creates the
// sandbox, records it and claims the run under tr.mu.
export default makeScene2D(function* (view) {
  const a = taskRunnerLayout(view, 'TaskRunner.Start creates its sandbox');
  const {links} = a;

  a.sandbox.opacity(0);
  links.backend.opacity(0);
  a.sandbox.subtitle('c1');
  a.driver.activity('');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'Poll returns task 42 with START v1. The manager takes a concurrency slot.',
  );
  yield* a.manager.updateActivity('Poll()');
  yield* links.managerApi.rpc('ListRunnerTasks', '[42: START v1]', {
    color: Colors.runner,
  });

  yield* a.caption.show(
    2,
    'm.taskRunner(42) gets or creates the TaskRunner under m.mu. It has no sandbox yet and writes nothing.',
  );
  yield* a.manager.updateActivity('m.taskRunner(42)');
  yield* a.registry.addRow('42', 'TaskRunner');
  yield* all(
    a.state.addRow('sb', 'nil'),
    a.state.addRow('running', 'false'),
  );

  yield* a.caption.show(
    3,
    'The manager builds backend.Spec: workspace, agent config and a task token minted by the server.',
  );
  yield* a.manager.updateActivity('m.spec(task)');
  yield* links.managerApi.rpc('CreateTaskToken', 'JWT', {
    color: Colors.runner,
  });

  yield* a.caption.show(
    4,
    'tr.Start(ctx, spec) locks tr.mu. There is no sandbox, so it calls backend.Create(&spec).',
  );
  yield* links.calls.send('Start(spec)', {color: Colors.runner});
  yield* a.taskRunner.updateActivity('Start · tr.mu held');
  yield* links.backend.opacity(1, 0.3);
  yield* links.backend.send('Create(spec)', {color: Colors.runner});
  a.sandbox.scale(0.92);
  yield* all(a.sandbox.opacity(1, 0.5), a.sandbox.scale(1, 0.5));
  yield* a.state.setRow('sb', 'c1');

  yield* a.caption.show(
    5,
    'Still under the lock, it writes the record stamped with v1. A new sandbox starts at Seq 0.',
  );
  yield* a.taskstate.addRow('type', 'docker');
  yield* all(
    a.taskstate.addRow('id', 'c1'),
    a.taskstate.addRow('version', '1'),
    a.taskstate.addRow('seq', '0'),
  );

  yield* a.caption.show(
    6,
    'sb.Start boots it, then Start claims the TaskRunner (running = true) and unlocks.',
  );
  yield* links.backend.send('Start', {color: Colors.runner});
  yield* a.sandbox.subtitle('c1 · up', 0.4);
  yield* a.state.setRow('running', 'true');
  yield* a.taskRunner.updateActivity('claimed');

  yield* a.caption.show(
    7,
    'A second START now gets ErrRunning before any Create. Nothing else can create task 42’s sandbox.',
  );
  yield* links.calls.send('Start(spec)', {color: Colors.runner});
  yield* links.calls.send('ErrRunning', {back: true, color: Colors.runner});

  yield* a.caption.show(
    8,
    'm.run calls tr.Run on its own goroutine: wait for the driver, start run v1, then attach and follow it.',
  );
  yield* a.manager.updateActivity('go tr.Run(ctx, runSpec)');
  yield* a.taskRunner.updateActivity('Run: attach v1');
  yield* links.backend.rpc('Run(v1, spec)', 'Accepted', {
    color: Colors.runner,
  });
  yield* all(a.driver.updateActivity('run v1'), a.wal.addRow('last', '1'));
  yield* waitFor(2);
});
