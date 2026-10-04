import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {experimentalDockerLayout} from '../layouts/experimental-docker';
import {Colors} from '../theme';

// Phase A of proposals/draft/driver-server.md: the first start of a task on
// the opt-in ExperimentalDocker backend. The container runs `gritz driver
// --serve`; the backend speaks the driver protocol behind today's
// backend.Backend interface (Launch, Wait, Probe, Signal, Destroy), so
// runner.go only sets Spec.Version. The driver still reports to the server
// directly, a dropped Run stream is reattached, and Wait stops the container
// after Finished so it is stopped between runs as today.
export default makeScene2D(function* (view) {
  const a = experimentalDockerLayout(view, 'ExperimentalDocker (phase A)');
  const {links} = a;

  a.memory.putRow('sem', '0 / 4');

  // No container exists until Launch creates it.
  a.container.opacity(0);
  a.container.subtitle('gritz-42');
  a.record.putRow('state', 'absent');
  links.dockerdContainer.opacity(0);
  links.backendDriver.opacity(0);
  links.driverApi.opacity(0);

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'Task 42 has a pending START command. The runner’s poll returns it.',
  );
  yield* a.runner.updateActivity('Poll()');
  yield* links.runnerApi.send('ListRunnerTasks', {color: Colors.runner});
  yield* links.runnerApi.send('[42: START]', {
    back: true,
    color: Colors.server,
  });

  yield* a.caption.show(
    2,
    'Start takes a slot and mints the task token. The driver will use it to talk to the server itself.',
  );
  yield* all(
    a.runner.updateActivity('Start(42)'),
    a.memory.setRow('sem', '1 / 4'),
    a.taskstate.flash(),
  );
  yield* links.runnerApi.rpc('CreateTaskToken', 'JWT', {color: Colors.runner});

  yield* a.caption.show(
    3,
    'Launch: ContainerCreate runs gritz driver --serve. The binary and agent config are copied in as today.',
  );
  yield* links.runnerBackend.send('Launch(spec v1)', {color: Colors.runner});
  yield* a.backend.updateActivity('Launch');
  yield* links.backendDockerd.send('ContainerCreate', {color: Colors.runner});
  yield* links.dockerdContainer.opacity(1, 0.3);
  a.container.scale(0.94);
  yield* all(
    a.container.opacity(1, 0.5),
    a.container.scale(1, 0.5),
    a.container.subtitle('gritz-42 · created', 0.4),
  );
  yield* links.backendDockerd.send('CopyToContainer', {color: Colors.runner});
  yield* all(
    a.config.addRow('type', 'claude'),
    a.config.addRow('started', 'false'),
  );

  yield* a.caption.show(
    4,
    'ContainerStart. The driver boots, finds no run.json, and serves on :8080.',
  );
  yield* links.backendDockerd.send('ContainerStart', {color: Colors.runner});
  yield* links.dockerdContainer.send('start', {color: Colors.client});
  yield* all(a.container.subtitle('gritz-42 · running', 0.4), a.container.pulse());
  yield* a.driver.updateActivity('boot');
  yield* a.record.flash('state');
  yield* a.driver.updateActivity('serve');

  yield* a.caption.show(
    5,
    'Launch dials the container IP and polls Status until the driver answers. No run yet.',
  );
  yield* links.backendDriver.opacity(1, 0.3);
  yield* links.backendDriver.rpc('Status', '{run: none}', {
    color: Colors.runner,
  });

  yield* a.caption.show(
    6,
    'Run(v1, spec) carries the task id, server URL, token, env and secrets. The driver records the run.',
  );
  yield* links.backendDriver.send('Run(v1, spec)', {color: Colors.runner});
  yield* a.record.setRow('state', 'RUNNING');
  yield* a.record.addRow('version', '1');
  yield* a.driver.updateActivity('run v1');
  yield* links.backendDriver.send('Accepted', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    7,
    'Launch closes the stream and returns the handle. runner.go persists it, exactly as today.',
  );
  yield* links.runnerBackend.send('handle', {back: true, color: Colors.runner});
  yield* a.taskstate.addRow('42.json', '');
  yield* all(
    a.taskstate.addRow('  type', 'experimental-docker'),
    a.taskstate.addRow('  id', 'c0ffee12'),
    a.taskstate.addRow('  version', '1'),
  );

  yield* a.caption.show(
    8,
    'supervise calls Wait, which attaches with Run(v1). The driver fetches the task and reports started.',
  );
  yield* a.runner.updateActivity('supervise: Wait()');
  yield* links.runnerBackend.send('Wait', {color: Colors.runner});
  yield* a.backend.updateActivity('Wait');
  yield* links.backendDriver.send('Run(v1)', {color: Colors.runner});
  yield* links.backendDriver.send('Accepted{attached}', {
    back: true,
    color: Colors.driver,
  });
  yield* links.driverApi.opacity(1, 0.3);
  yield* links.driverApi.rpc('GetTask', 'v1', {color: Colors.driver});
  yield* links.driverApi.send('started v1', {color: Colors.driver});

  yield* a.caption.show(
    9,
    'The agent runs. The driver ships logs to the server itself; the stream only carries keep-alives.',
  );
  yield* a.driver.updateActivity('agent running');
  yield* links.driverApi.send('AppendLogChunk', {color: Colors.driver});
  yield* links.backendDriver.send('KeepAlive', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    10,
    'The stream drops. Wait sees the container still running and attaches again. The run never noticed.',
  );
  yield* links.backendDriver.opacity(0.25, 0.3);
  yield* a.backend.updateActivity('reconnect');
  yield* links.backendDockerd.rpc('ContainerInspect', 'running', {
    color: Colors.runner,
  });
  yield* links.backendDriver.opacity(1, 0.3);
  yield* links.backendDriver.send('Run(v1)', {color: Colors.runner});
  yield* links.backendDriver.send('Accepted{attached}', {
    back: true,
    color: Colors.driver,
  });
  yield* a.backend.updateActivity('Wait');

  yield* a.caption.show(
    11,
    'The agent finishes. The driver reports stopped, records the ack, then sends Finished{reported}.',
  );
  yield* a.driver.updateActivity('report');
  yield* links.driverApi.send('stopped v1', {color: Colors.driver});
  yield* a.config.setRow('started', 'true');
  yield* a.record.setRow('state', 'FINISHED');
  yield* a.record.addRow('reported', 'true');
  yield* links.backendDriver.send('Finished{reported}', {
    back: true,
    color: Colors.driver,
  });
  yield* a.driver.updateActivity('idle');

  yield* a.caption.show(
    12,
    'Wait stops the container, so it is stopped between runs as today, and returns 0. No backstop is owed.',
  );
  yield* links.backendDockerd.send('ContainerStop', {color: Colors.runner});
  yield* links.dockerdContainer.send('SIGTERM', {color: Colors.client});
  yield* a.driver.updateActivity('shutdown');
  yield* all(
    a.container.subtitle('gritz-42 · exited', 0.4),
    a.container.pulse(),
    a.driver.updateActivity(''),
  );
  yield* links.runnerBackend.send('0', {back: true, color: Colors.runner});
  yield* all(
    a.memory.setRow('sem', '0 / 4'),
    a.outbox.flash(),
    a.backend.updateActivity(''),
    a.runner.updateActivity('idle'),
  );

  yield* a.caption.show(
    13,
    'Next start: Probe sees the container exited, so Launch starts it again and calls Run(v2, spec).',
  );
  yield* all(a.taskstate.flash(), a.record.flash(), a.container.pulse());
  yield* waitFor(2);
});
