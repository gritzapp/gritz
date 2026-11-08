import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {dockerLayout} from '../layouts/docker';
import {Colors} from '../theme';

// Happy path from CreateTask to the end of the task's first run, on Docker:
//
//   internal/server/apiserver/task.go           CreateTask
//   internal/runner/serve.go                    SSE notifications wake the log
//   internal/runner/taskevents/taskevents.go    ListRunnerEvents into runner.db
//   internal/runner/taskmanager.go              Run, specs (CreateTaskToken)
//   internal/runner/taskrunner.go               RunLoop, boot, session, busy
//   internal/runner/backend/docker/sandbox.go   Create, Boot, Dial, Suspend
//   internal/runner/driverserver/               Run stream, loop, Executor
//   internal/runner/agent/driver.go             Run (started / stopped)
//   internal/server/apiserver/runner.go         SubmitRunnerEvents
//   internal/model/task.go                      ApplyRunnerEvent
export default makeScene2D(function* (view) {
  const a = dockerLayout(view, 'Create Task');
  const {links} = a;

  // No sandbox exists until the runner creates one.
  a.container.opacity(0);
  a.container.subtitle('gritz-42');
  links.runnerContainer.opacity(0);
  links.runnerDriver.opacity(0);
  links.driverApi.opacity(0);
  links.driverAgent.opacity(0);
  links.agentApi.opacity(0);
  a.slots.putRow('taken', '0 / 4');
  a.runnerdb.putRow('cursor', '#100');
  a.driver.activity('');
  a.agent.activity('');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'A client calls CreateTask with runner laptop, a workspace and the instructions.',
  );
  yield* a.client.pulse();
  yield* links.clientApi.send('CreateTask', {color: Colors.client});

  yield* a.caption.show(
    2,
    'One transaction inserts task 42 (PENDING, START, v1) with a Created event, its command and the instruction.',
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
  yield* a.events.addRow('#102', 'command START v1');
  yield* a.events.addRow('#103', 'instruction');
  yield* links.clientApi.send('Task{id: 42}', {
    back: true,
    color: Colors.server,
  });

  yield* a.caption.show(
    3,
    'The server publishes a change for runner laptop; the runner’s SSE stream wakes its event log.',
  );
  yield* links.apiPubsub.send('publish', {color: Colors.server});
  yield* links.pubsubRunner.send('change: task 42', {color: Colors.server});
  yield* a.runner.updateActivity('Wake()');

  yield* a.caption.show(
    4,
    'ListRunnerEvents returns the events after the cursor; runner.db stores them and task 42’s latest command.',
  );
  yield* a.runner.updateActivity('taskevents sync');
  yield* links.runnerApi.send('ListRunnerEvents', {color: Colors.runner});
  yield* links.apiPostgres.rpc('SELECT', 'rows', {color: Colors.server});
  yield* links.runnerApi.send('#101–#103', {
    back: true,
    color: Colors.server,
  });
  yield* all(
    a.runnerdb.setRow('cursor', '#103'),
    a.runnerdb.addRow('events', '#101–#103'),
    a.runnerdb.addRow('tasks.42', 'START v1'),
  );

  yield* a.caption.show(
    5,
    'The TaskManager starts a TaskRunner for task 42. START v1 is newer than any run it drove: take a slot.',
  );
  yield* a.runner.updateActivity('RunLoop(42)');
  yield* a.slots.setRow('taken', '1 / 4');

  yield* a.caption.show(
    6,
    'Build the specs: the server mints a task token; the RunSpec carries it with the env, secrets and files.',
  );
  yield* a.runner.updateActivity('specs(42)');
  yield* links.runnerApi.rpc('CreateTaskToken', 'JWT', {
    color: Colors.runner,
  });

  yield* a.caption.show(
    7,
    'backend.Create makes container gritz-42 running “gritz driver”, and its handle is recorded in runner.db.',
  );
  yield* a.runner.updateActivity('backend.Create');
  yield* links.runnerContainer.opacity(1, 0.3);
  yield* links.runnerContainer.send('ContainerCreate', {color: Colors.runner});
  a.container.scale(0.92);
  yield* all(a.container.opacity(1, 0.5), a.container.scale(1, 0.5));
  yield* a.container.subtitle('gritz-42 · created', 0.4);
  yield* a.runnerdb.addRow('sandboxes.42', 'docker 3f9a2c v1');

  yield* a.caption.show(
    8,
    'Boot starts it. The runner opens a Run stream with the RunSpec through a docker exec; the driver says Hello.',
  );
  yield* a.runner.updateActivity('Boot');
  yield* links.runnerContainer.send('ContainerStart', {color: Colors.runner});
  yield* all(
    a.container.subtitle('gritz-42 · running', 0.4),
    a.container.pulse(),
    a.driver.updateActivity('listening'),
  );
  yield* a.runner.updateActivity('session: Run');
  yield* links.runnerDriver.opacity(1, 0.3);
  yield* links.runnerDriver.rpc('Run(RunSpec)', 'Hello{last: 0}', {
    color: Colors.runner,
  });

  yield* a.caption.show(
    9,
    'The runner forwards events #101–#103. The driver stores them, and the START command starts run v1.',
  );
  yield* links.runnerDriver.send('#101–#103', {color: Colors.runner});
  yield* a.files.addRow('events.jsonl', '#101–#103');
  yield* all(
    a.driver.updateActivity('start v1'),
    a.files.addRow('run.json', 'v1 running'),
  );

  yield* a.caption.show(
    10,
    'The run writes the RunSpec files and reports started v1. Fold: PENDING + START → RUNNING.',
  );
  yield* all(
    a.config.addRow('Started', 'false'),
    a.config.addRow('setup', '0 / 2'),
    a.config.addRow('cursor', '0'),
  );
  yield* links.driverApi.opacity(1, 0.3);
  yield* a.driver.updateActivity('GetTask');
  yield* links.driverApi.rpc('GetTask', 'v1', {color: Colors.driver});
  yield* links.driverApi.send('started v1', {color: Colors.driver});
  yield* links.apiPostgres.send('UPDATE', {color: Colors.server});
  yield* all(
    a.tasks.setRow('status', 'RUNNING'),
    a.tasks.setRow('command', 'NONE'),
  );
  yield* a.events.addRow('#104', 'SANDBOX_STARTED');
  yield* a.events.addRow('#105', 'command NONE v1');

  yield* a.caption.show(
    11,
    'Setup commands run, the instruction is rendered into the prompt, and the agent starts. Logs ship masked.',
  );
  yield* a.driver.updateActivity('setup');
  yield* a.config.setRow('setup', '2 / 2');
  yield* links.driverAgent.opacity(1, 0.3);
  yield* a.driver.updateActivity('agent running');
  yield* links.driverAgent.send('prompt', {color: Colors.driver});
  yield* a.agent.updateActivity('working');
  yield* links.driverApi.send('AppendLogChunk', {color: Colors.driver});

  yield* a.caption.show(
    12,
    'The agent calls the injected gritz MCP tools, which reach the server directly with the task token.',
  );
  yield* links.agentApi.opacity(1, 0.3);
  yield* links.agentApi.send('report', {color: Colors.driver});
  yield* waitFor(0.5);

  yield* a.caption.show(
    13,
    'The agent exits; the config is saved, then the driver reports stopped v1. Fold: RUNNING + NONE → COMPLETED.',
  );
  yield* a.agent.updateActivity('exit 0');
  yield* all(
    a.config.setRow('Started', 'true'),
    a.config.setRow('cursor', '#103'),
  );
  yield* a.driver.updateActivity('report');
  yield* links.driverApi.send('stopped v1', {color: Colors.driver});
  yield* links.apiPostgres.send('UPDATE', {color: Colors.server});
  yield* a.tasks.setRow('status', 'COMPLETED');
  yield* a.events.addRow('#106', 'SANDBOX_EXITED');
  yield* a.files.setRow('run.json', 'v1 finished, reported');

  yield* a.caption.show(
    14,
    'The driver is idle and sends Idle{v1, reported}. The runner releases its slot and stops the container.',
  );
  yield* a.driver.updateActivity('idle');
  yield* links.runnerDriver.send('Idle{v1, reported}', {
    back: true,
    color: Colors.driver,
  });
  yield* all(
    a.slots.setRow('taken', '0 / 4'),
    a.runner.updateActivity('Suspend'),
  );
  yield* links.runnerContainer.send('ContainerStop', {color: Colors.runner});
  yield* all(
    a.container.subtitle('gritz-42 · exited', 0.4),
    a.driver.updateActivity(''),
    a.agent.updateActivity(''),
    a.runner.updateActivity('idle'),
  );

  yield* a.caption.show(
    15,
    'The container and its record stay: the next instruction boots it again, and archiving destroys it.',
  );
  yield* all(a.runnerdb.flash('sandboxes.42'), a.container.pulse());
  yield* waitFor(2);
});
