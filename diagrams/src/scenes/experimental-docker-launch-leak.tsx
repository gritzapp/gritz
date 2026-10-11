import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {experimentalDockerLayout} from '../layouts/experimental-docker';
import {Colors} from '../theme';

// A Launch that fails after ContainerCreate in the ExperimentalDocker backend
// (gritzapp/gritz#1685). Traced from
// internal/runner/backend/experimentaldocker/experimentaldocker.go (Launch,
// create, waitReady) and internal/runner/runner.go (Poll's start handler,
// Start).
//
// Launch returns an empty handle with every error, so when waitReady times
// out the runner records nothing. The container is left running, and the next
// start has no handle to reuse: create collides with the existing gritz-42.
export default makeScene2D(function* (view) {
  const a = experimentalDockerLayout(view, 'ExperimentalDocker: Launch leaks the container');
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
    'Task 42 has a pending START for v1. There is no taskstate record, so Start launches fresh.',
  );
  yield* a.runner.updateActivity('Poll()');
  yield* links.runnerApi.send('ListRunnerTasks', {color: Colors.runner});
  yield* links.runnerApi.send('[42: START v1]', {
    back: true,
    color: Colors.server,
  });
  yield* all(
    a.runner.updateActivity('Start(42 v1)'),
    a.memory.setRow('sem', '1 / 4'),
    a.taskstate.flash(),
  );
  yield* links.runnerApi.rpc('CreateTaskToken', 'JWT', {color: Colors.runner});

  yield* a.caption.show(
    2,
    'Launch(reuse: nil) creates gritz-42 and copies the driver binary and agent config in.',
  );
  yield* links.runnerBackend.send('Launch(v1, nil)', {color: Colors.runner});
  yield* a.backend.updateActivity('Launch: create');
  yield* links.backendDockerd.send('ContainerCreate', {color: Colors.runner});
  yield* links.dockerdContainer.opacity(1, 0.3);
  a.container.scale(0.94);
  yield* all(
    a.container.opacity(1, 0.5),
    a.container.scale(1, 0.5),
    a.container.subtitle('gritz-42 · created', 0.4),
  );
  yield* links.backendDockerd.send('CopyToContainer', {color: Colors.runner});
  yield* a.config.addRow('type', 'claude');

  yield* a.caption.show(
    3,
    'ContainerStart. The container is up, but the driver server is slow to start listening on :8080.',
  );
  yield* links.backendDockerd.send('ContainerStart', {color: Colors.runner});
  yield* links.dockerdContainer.send('start', {color: Colors.client});
  yield* all(a.container.subtitle('gritz-42 · running', 0.4), a.container.pulse());
  yield* a.driver.updateActivity('booting…');

  yield* a.caption.show(
    4,
    'waitReady polls Status every 250ms. Every call is refused while the driver is still booting.',
  );
  yield* a.backend.updateActivity('Launch: waitReady');
  yield* links.backendDriver.opacity(1, 0.3);
  yield* a.memory.addRow('waitReady', '0s / 30s');
  yield* links.backendDriver.rpc('Status', 'refused', {
    color: Colors.runner,
    duration: 0.6,
  });
  yield* a.memory.setRow('waitReady', '12s / 30s');
  yield* links.backendDriver.rpc('Status', 'refused', {
    color: Colors.runner,
    duration: 0.6,
  });
  yield* a.memory.setRow('waitReady', '30s / 30s');

  yield* a.caption.show(
    5,
    'waitReady times out. Launch returns the error with an empty Handle{}, so the runner records nothing.',
  );
  yield* links.backendDriver.opacity(0.25, 0.3);
  yield* links.runnerBackend.send('Handle{}, err', {
    back: true,
    color: Colors.bad,
  });
  yield* all(
    a.memory.dropRow('waitReady'),
    a.taskstate.flash(),
    a.backend.updateActivity(''),
  );

  yield* a.caption.show(
    6,
    'Start’s error path releases the slot and enqueues failed v1. The task fails.',
  );
  yield* all(
    a.memory.setRow('sem', '0 / 4'),
    a.outbox.addRow('42', 'failed v1'),
  );
  yield* links.runnerApi.send('SubmitRunnerEvents', {color: Colors.runner});
  yield* all(a.outbox.dropRow('42'), a.runner.updateActivity('idle'));

  yield* a.caption.show(
    7,
    'The driver finishes booting and serves with no run. Nothing in taskstate/ points at this container.',
  );
  yield* a.driver.updateActivity('serve (no run)');
  yield* all(a.record.flash('state'), a.container.pulse(), a.taskstate.flash());

  yield* a.caption.show(
    8,
    'The user restarts the task. Still no record, so Start calls Launch(reuse: nil) and creates again.',
  );
  yield* links.runnerApi.send('ListRunnerTasks', {color: Colors.runner});
  yield* links.runnerApi.send('[42: START v2]', {
    back: true,
    color: Colors.server,
  });
  yield* all(
    a.runner.updateActivity('Start(42 v2)'),
    a.memory.setRow('sem', '1 / 4'),
  );
  yield* links.runnerApi.rpc('CreateTaskToken', 'JWT', {color: Colors.runner});
  yield* links.runnerBackend.send('Launch(v2, nil)', {color: Colors.runner});
  yield* a.backend.updateActivity('Launch: create');
  yield* links.backendDockerd.send('ContainerCreate', {color: Colors.runner});
  yield* links.backendDockerd.send('Conflict: gritz-42 in use', {
    back: true,
    color: Colors.bad,
  });

  yield* a.caption.show(
    9,
    'Every retry fails the same way, while the old container keeps running, until gritz-42 is removed by hand.',
  );
  yield* links.runnerBackend.send('Handle{}, err', {
    back: true,
    color: Colors.bad,
  });
  yield* all(
    a.memory.setRow('sem', '0 / 4'),
    a.outbox.addRow('42', 'failed v2'),
    a.backend.updateActivity(''),
  );
  yield* links.runnerApi.send('SubmitRunnerEvents', {color: Colors.runner});
  yield* all(
    a.outbox.dropRow('42'),
    a.runner.updateActivity('idle'),
    a.container.pulse(),
  );
  yield* waitFor(2);
});
