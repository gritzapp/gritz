import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {dockerDriverServerLayout} from '../layouts/docker-driver-server';
import {Colors} from '../theme';

// The stopAfter race in the Docker backend (gritzapp/gritz#1684).
// Traced from internal/runner/backend/docker/docker.go
// (Wait, stopAfter, Probe, Launch), internal/runner/runner.go (Start,
// supervise) and internal/runner/driverserver/driverserver.go (Shutdown).
//
// supervise's Wait for v1 sees Finished and calls stopAfter, which checks
// Status and then calls ContainerStop. Nothing serializes that against
// Runner.Start for v2: Probe reports the finished v1 as exited, Launch reuses
// the still-running container and starts v2, and the stale stop then SIGTERMs
// the driver, whose Shutdown stops v2.
export default makeScene2D(function* (view) {
  const a = dockerDriverServerLayout(view, 'Docker: stopAfter race');
  const {links} = a;

  // Run v1 is in flight: the container is up and supervise's Wait is attached.
  a.memory.putRow('sem', '1 / 4');
  a.taskstate.putRow('42.json', '');
  a.taskstate.putRow('  type', 'docker');
  a.taskstate.putRow('  id', 'c0ffee12');
  a.taskstate.putRow('  version', '1');
  a.container.subtitle('gritz-42 · running');
  a.record.putRow('version', '1');
  a.record.putRow('state', 'RUNNING');
  a.config.putRow('type', 'claude');
  a.config.putRow('started', 'true');
  a.runner.activity('supervise v1: Wait()');
  a.backend.activity('Wait v1');
  a.driver.activity('agent running v1');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'Run v1 is in flight. supervise’s Wait holds a Run(v1) stream to the driver.',
  );
  yield* links.backendDriver.send('KeepAlive', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    2,
    'The agent finishes. The driver reports stopped v1 to the server first, then sends Finished.',
  );
  yield* a.driver.updateActivity('report v1');
  yield* links.driverApi.send('stopped v1', {color: Colors.driver});
  yield* all(
    a.record.setRow('state', 'FINISHED'),
    a.record.addRow('reported', 'true'),
  );
  yield* links.backendDriver.send('Finished{v1}', {
    back: true,
    color: Colors.driver,
  });
  yield* a.driver.updateActivity('idle');

  yield* a.caption.show(
    3,
    'Wait calls stopAfter. Status still says v1, so stopAfter decides to stop the container...',
  );
  yield* a.backend.updateActivity('stopAfter v1: check');
  yield* links.backendDriver.rpc('Status', '{v1 FINISHED}', {
    color: Colors.runner,
  });
  yield* all(a.record.flash('version'), a.backend.updateActivity('stopAfter v1: about to stop'));

  yield* a.caption.show(
    4,
    '...but the server already has v1 stopped, so a follow-up for v2 comes back on the next poll.',
  );
  yield* links.runnerApi.send('ListRunnerTasks', {color: Colors.runner});
  yield* links.runnerApi.send('[42: START v2]', {
    back: true,
    color: Colors.server,
  });

  yield* a.caption.show(
    5,
    'Start(42) probes the handle. The container is running but v1 is finished, so Probe says exited.',
  );
  yield* a.runner.updateActivity('Start(42 v2)');
  yield* links.runnerBackend.send('Probe', {color: Colors.runner});
  yield* links.backendDriver.rpc('Status', '{v1 FINISHED}', {
    color: Colors.runner,
  });
  yield* links.runnerBackend.send('StateExited', {
    back: true,
    color: Colors.runner,
  });

  yield* a.caption.show(
    6,
    'Launch reuses the container: ContainerStart is a no-op, and Run(v2, spec) starts the next run.',
  );
  yield* links.runnerApi.rpc('CreateTaskToken', 'JWT', {color: Colors.runner});
  yield* links.runnerBackend.send('Launch(v2, reuse)', {color: Colors.runner});
  yield* a.backend.updateActivity('Launch v2');
  yield* links.backendDockerd.send('ContainerStart', {color: Colors.runner});
  yield* links.backendDriver.send('Run(v2, spec)', {color: Colors.runner});
  yield* all(
    a.record.setRow('version', '2'),
    a.record.setRow('state', 'RUNNING'),
    a.record.dropRow('reported'),
  );
  yield* a.driver.updateActivity('agent running v2');
  yield* links.backendDriver.send('Accepted', {
    back: true,
    color: Colors.driver,
  });
  yield* links.runnerBackend.send('handle v2', {back: true, color: Colors.runner});
  yield* a.taskstate.setRow('  version', '2');
  yield* links.driverApi.send('started v2', {color: Colors.driver});

  yield* a.caption.show(
    7,
    'stopAfter resumes on its stale check and calls ContainerStop. Nothing locked the task in between.',
  );
  yield* a.backend.updateActivity('stopAfter v1: stop');
  yield* links.backendDockerd.send('ContainerStop', {color: Colors.bad});
  yield* links.dockerdContainer.send('SIGTERM', {color: Colors.bad});
  yield* all(a.container.pulse(), a.driver.updateActivity('Shutdown'));

  yield* a.caption.show(
    8,
    'The driver’s Shutdown stops the active run, v2, and reports stopped v2. Nobody asked for that.',
  );
  yield* links.driverApi.send('stopped v2', {color: Colors.bad});
  yield* all(
    a.record.setRow('state', 'FINISHED'),
    a.container.subtitle('gritz-42 · exited', 0.4),
    a.driver.updateActivity(''),
  );

  yield* a.caption.show(
    9,
    'Wait for v1 returns 0, and v2’s Wait sees Finished. The follow-up run was killed by v1’s cleanup.',
  );
  yield* links.runnerBackend.send('0', {back: true, color: Colors.runner});
  yield* all(
    a.memory.flash('sem'),
    a.backend.updateActivity(''),
    a.runner.updateActivity('v2: stopped'),
    a.record.flash(),
  );
  yield* waitFor(2);
});
