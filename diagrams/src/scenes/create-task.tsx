import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {architecture} from '../architecture';
import {Colors} from '../theme';

// Happy path from CreateTask to the task completing:
//
//   internal/server/apiserver/task.go     CreateTask
//   internal/command/runner.go            SSE wake + poll loop
//   internal/runner/runner.go             Poll / Start / spec / supervise
//   internal/runner/agent/driver.go       Run (started, stopped)
//   internal/model/task.go                ApplyRunnerEvent
export default makeScene2D(function* (view) {
  const a = architecture(view, 'Create Task');
  const {links} = a;

  // No sandbox exists until the runner launches one.
  a.container.opacity(0);
  a.container.subtitle('gritz-42');
  links.runnerContainer.opacity(0);
  links.driverApi.opacity(0);
  a.memory.putRow('sem', '0 / 4');
  a.driver.activity('');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'A client calls CreateTask with a runner, workspace, and instructions.',
  );
  yield* a.client.pulse();
  yield* links.clientApi.send('CreateTask', {color: Colors.client});

  yield* a.caption.show(
    2,
    'One transaction inserts the task row (PENDING, START, v1) plus a Created event and the instructions.',
  );
  yield* links.apiPostgres.send('BEGIN … COMMIT', {color: Colors.server});
  yield* a.tasks.addRow('id', '42');
  yield* all(
    a.tasks.addRow('status', 'PENDING'),
    a.tasks.addRow('command', 'START'),
    a.tasks.addRow('version', '1'),
    a.tasks.addRow('runner', 'laptop'),
  );
  yield* a.events.addRow('#101', 'lifecycle CREATED');
  yield* a.events.addRow('#102', 'instruction');
  yield* links.clientApi.send('Task{id: 42}', {
    back: true,
    color: Colors.server,
  });

  yield* a.caption.show(
    3,
    'The server publishes a change notification; the runner’s SSE stream wakes its poll loop.',
  );
  yield* links.apiPubsub.send('publish', {color: Colors.server});
  yield* links.pubsubRunner.send('change: task 42', {color: Colors.server});
  yield* a.runner.updateActivity('woken');

  yield* a.caption.show(
    4,
    'Poll: ListRunnerTasks returns every task on this runner with a pending command.',
  );
  yield* a.runner.updateActivity('Poll()');
  yield* links.runnerApi.send('ListRunnerTasks', {color: Colors.runner});
  yield* links.apiPostgres.rpc('SELECT', 'rows', {color: Colors.server});
  yield* links.runnerApi.send('[42: START]', {
    back: true,
    color: Colors.server,
  });

  yield* a.caption.show(
    5,
    'START with no live sandbox: take a concurrency slot and check taskstate for an existing handle.',
  );
  yield* a.runner.updateActivity('Start(42)');
  yield* a.memory.setRow('sem', '1 / 4');
  yield* a.taskstate.flash();

  yield* a.caption.show(
    6,
    'Build the spec: the server mints a task-scoped JWT for the driver and its MCP server.',
  );
  yield* links.runnerApi.send('CreateTaskToken', {color: Colors.runner});
  yield* links.runnerApi.send('JWT', {back: true, color: Colors.server});

  yield* a.caption.show(
    7,
    'Launch the container running “gritz driver --task 42” with the agent config written in.',
  );
  yield* a.runner.updateActivity('backend.Launch');
  yield* links.runnerContainer.opacity(1, 0.3);
  yield* links.runnerContainer.send('Launch(spec)', {color: Colors.runner});
  a.container.scale(0.92);
  yield* all(a.container.opacity(1, 0.5), a.container.scale(1, 0.5));
  yield* all(
    a.container.subtitle('gritz-42 · running', 0.4),
    a.container.pulse(),
    a.config.addRow('Started', 'false'),
    a.config.addRow('setup', '0 / 2'),
    a.config.addRow('cursor', '""'),
  );

  yield* a.caption.show(
    8,
    'Persist the handle to taskstate before supervising, so a restarted runner can re-adopt it.',
  );
  yield* a.taskstate.addRow('42.json', '');
  yield* all(
    a.taskstate.addRow('  version', '1'),
    a.taskstate.addRow('  type', 'docker'),
    a.taskstate.addRow('  id', '3f9a2c'),
  );
  yield* a.runner.updateActivity('supervise: Wait()');

  yield* a.caption.show(
    9,
    'The driver reads its task (version 1) and reports started. Fold: PENDING + START → RUNNING.',
  );
  yield* links.driverApi.opacity(1, 0.3);
  yield* a.driver.updateActivity('GetTask');
  yield* links.driverApi.rpc('GetTask', 'v1', {color: Colors.driver});
  yield* links.driverApi.send('started v1', {color: Colors.driver});
  yield* links.apiPostgres.send('UPDATE', {color: Colors.server});
  yield* all(a.tasks.setRow('status', 'RUNNING'), a.tasks.setRow('command', 'NONE'));
  yield* a.events.addRow('#103', 'SANDBOX_STARTED');

  yield* a.caption.show(
    10,
    'Run setup commands, drain instruction events from the cursor, then prompt the agent.',
  );
  yield* a.driver.updateActivity('setup');
  yield* a.config.setRow('setup', '2 / 2');
  yield* a.driver.updateActivity('drainEvents');
  yield* links.driverApi.rpc('ListEventsByTask', 'instructions', {
    color: Colors.driver,
  });
  yield* a.driver.updateActivity('agent running');
  yield* waitFor(1);

  yield* a.caption.show(
    11,
    'The agent finishes: config saved, then the driver reports stopped. Fold: RUNNING + NONE → COMPLETED.',
  );
  yield* all(a.config.setRow('Started', 'true'), a.config.setRow('cursor', '"…103"'));
  yield* a.driver.updateActivity('report');
  yield* links.driverApi.send('stopped v1', {color: Colors.driver});
  yield* links.apiPostgres.send('UPDATE', {color: Colors.server});
  yield* a.tasks.setRow('status', 'COMPLETED');
  yield* a.events.addRow('#104', 'SANDBOX_EXITED');

  yield* a.caption.show(
    12,
    'The driver exits 0, so the outcome is already recorded. supervise releases the slot and enqueues nothing.',
  );
  yield* all(
    a.driver.updateActivity('exit 0'),
    a.container.subtitle('gritz-42 · exited', 0.4),
  );
  yield* links.runnerContainer.send('exit 0', {
    back: true,
    color: Colors.driver,
  });
  yield* all(
    a.memory.setRow('sem', '0 / 4'),
    a.outbox.flash(),
    a.runner.updateActivity('idle'),
  );

  yield* a.caption.show(
    13,
    'The exited container and its taskstate record remain until the task is archived and pruned.',
  );
  yield* all(a.taskstate.flash(), a.container.pulse());
  yield* waitFor(2);
});
