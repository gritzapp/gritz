import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {microvmDriverServerLayout} from '../layouts/microvm-driver-server';
import {Colors} from '../theme';

// First start of a task on the Lambda MicroVM backend as proposed in
// proposals/draft/driver-server.md: the driver is a long-lived Connect server,
// DriverBackend implements backend.Backend on top of a Lambda Sandbox, and the
// VM is parked by an idle timer instead of on driver exit.
export default makeScene2D(function* (view) {
  const a = microvmDriverServerLayout(
    view,
    'Lambda MicroVM Start (driver server)',
  );
  const {links} = a;

  a.memory.putRow('sem', '0 / 4');
  a.memory.putRow('lock', '—');
  a.memory.putRow('idle timer', '—');

  // No VM exists until Ensure runs it.
  a.vm.opacity(0);
  a.vm.subtitle('mvm-7c1e');
  a.disk.putRow('.provisioned', 'no');
  a.disk.putRow('run.json', 'absent');
  links.backendDriver.opacity(0);
  links.lambdaDriver.opacity(0);
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
    'No handle in taskstate: take a slot and mint the task token. runner.go is unchanged.',
  );
  yield* a.runner.updateActivity('Start(42)');
  yield* all(a.memory.setRow('sem', '1 / 4'), a.taskstate.flash());
  yield* links.runnerApi.send('CreateTaskToken', {color: Colors.runner});
  yield* links.runnerApi.send('JWT', {back: true, color: Colors.server});

  yield* a.caption.show(
    3,
    'backend.Launch reaches DriverBackend, which takes the per-sandbox lock before touching the VM.',
  );
  yield* links.runnerBackend.send('Launch(spec v1)', {color: Colors.runner});
  yield* all(
    a.memory.setRow('lock', 'held'),
    a.backend.updateActivity('Ensure'),
  );

  yield* a.caption.show(
    4,
    'Ensure: RunMicrovm with no run-hook payload. There is nothing to stage in S3.',
  );
  yield* links.backendLambda.send('RunMicrovm', {color: Colors.runner});
  a.vm.scale(0.92);
  yield* all(
    a.vm.opacity(1, 0.5),
    a.vm.scale(1, 0.5),
    a.vm.subtitle('mvm-7c1e · pending', 0.4),
  );
  yield* links.backendLambda.send('{id, endpoint}', {
    back: true,
    color: Colors.client,
  });

  yield* a.caption.show(
    5,
    'The VM boots gritz driver --serve. It finds no run.json, serves, and acks the /run hook without doing any work.',
  );
  yield* a.driver.updateActivity('boot');
  yield* a.disk.flash('run.json');
  yield* a.driver.updateActivity('serve');
  yield* links.lambdaDriver.opacity(1, 0.3);
  yield* links.lambdaDriver.rpc('/run', '200', {color: Colors.client});
  yield* links.backendLambda.rpc('GetMicrovm', 'RUNNING', {
    color: Colors.runner,
  });
  yield* all(a.vm.subtitle('mvm-7c1e · running', 0.4), a.vm.pulse());

  yield* a.caption.show(
    6,
    'Dial: mint a port-scoped proxy token, then poll Health until the driver answers.',
  );
  yield* a.backend.updateActivity('Dial + Health');
  yield* links.backendLambda.send('CreateMicrovmAuthToken', {
    color: Colors.runner,
  });
  yield* links.backendLambda.send('token', {back: true, color: Colors.client});
  yield* links.backendDriver.opacity(1, 0.3);
  yield* links.backendDriver.rpc('Health', '{run: none}', {
    color: Colors.runner,
  });

  yield* a.caption.show(
    7,
    'Run(v1, spec) carries the spec over the proxy. The driver records the run, provisions files, and accepts.',
  );
  yield* a.backend.updateActivity('Run(v1, spec)');
  yield* links.backendDriver.send('Run(v1, spec)', {color: Colors.runner});
  yield* a.disk.setRow('run.json', '');
  yield* all(
    a.disk.addRow('  version', '1'),
    a.disk.addRow('  state', 'RUNNING'),
  );
  yield* all(
    a.disk.setRow('.provisioned', 'yes'),
    a.driver.updateActivity('run v1'),
  );
  yield* links.backendDriver.send('Accepted', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    8,
    'Launch closes the stream and unlocks. The runner persists the handle; its data carries the run version.',
  );
  yield* all(
    a.memory.setRow('lock', '—'),
    a.backend.updateActivity(''),
  );
  yield* links.runnerBackend.send('handle', {back: true, color: Colors.runner});
  yield* a.taskstate.addRow('42.json', '');
  yield* all(
    a.taskstate.addRow('  type', 'lambda-microvm'),
    a.taskstate.addRow('  id', 'mvm-7c1e'),
    a.taskstate.addRow('  version', '1'),
  );

  yield* a.caption.show(
    9,
    'The driver talks to the server as before: GetTask, then it reports started.',
  );
  yield* links.driverApi.opacity(1, 0.3);
  yield* links.driverApi.rpc('GetTask', 'v1', {color: Colors.driver});
  yield* links.driverApi.send('started v1', {color: Colors.driver});

  yield* a.caption.show(
    10,
    'supervise calls Wait, which attaches with Run(v1) and no spec. Keep-alives flow while the agent runs.',
  );
  yield* a.runner.updateActivity('supervise: Wait()');
  yield* links.runnerBackend.send('Wait', {color: Colors.runner});
  yield* a.backend.updateActivity('Wait');
  yield* links.backendDriver.send('Run(v1)', {color: Colors.runner});
  yield* links.backendDriver.send('Accepted{attached}', {
    back: true,
    color: Colors.driver,
  });
  yield* a.driver.updateActivity('agent running');
  yield* links.backendDriver.send('KeepAlive', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    11,
    'The agent finishes. The driver reports stopped, writes the finished record, then sends Finished{reported}.',
  );
  yield* a.driver.updateActivity('report');
  yield* links.driverApi.send('stopped v1', {color: Colors.driver});
  yield* a.disk.setRow('  state', 'FINISHED');
  yield* all(
    a.disk.addRow('  reported', 'true'),
    a.disk.addRow('  finished_at', '12:04:31'),
  );
  yield* links.backendDriver.send('Finished{reported}', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    12,
    'Wait arms the idle timer and returns 0. The slot is freed and nothing is enqueued. The VM stays warm.',
  );
  yield* all(
    a.memory.setRow('idle timer', '5m'),
    a.backend.updateActivity('idle timer'),
    a.driver.updateActivity('idle'),
  );
  yield* links.runnerBackend.send('0', {back: true, color: Colors.runner});
  yield* all(
    a.memory.setRow('sem', '0 / 4'),
    a.outbox.flash(),
    a.runner.updateActivity('idle'),
  );

  yield* a.caption.show(
    13,
    'The timer fires. Under the lock, Health confirms the run finished and the VM has been idle long enough.',
  );
  yield* waitFor(0.5);
  yield* all(
    a.memory.setRow('idle timer', 'fired'),
    a.memory.setRow('lock', 'held'),
    a.backend.updateActivity('Park'),
  );
  yield* links.backendDriver.rpc('Health', '{v1 finished}', {
    color: Colors.runner,
  });

  yield* a.caption.show(
    14,
    'Park: SuspendMicrovm, then poll until SUSPENDED. The /suspend hook is only acknowledged.',
  );
  yield* links.backendLambda.send('SuspendMicrovm', {color: Colors.runner});
  yield* links.lambdaDriver.rpc('/suspend', '200', {color: Colors.client});
  yield* links.backendLambda.rpc('GetMicrovm', 'SUSPENDED', {
    color: Colors.runner,
  });
  yield* all(
    a.vm.subtitle('mvm-7c1e · suspended', 0.4),
    a.vm.pulse(),
    a.driver.updateActivity(''),
  );
  yield* all(
    a.memory.setRow('lock', '—'),
    a.memory.setRow('idle timer', '—'),
    a.backend.updateActivity(''),
  );

  yield* a.caption.show(
    15,
    'run.json and the handle survive. The next run resumes the VM and calls Run(v2, spec).',
  );
  yield* all(a.taskstate.flash(), a.disk.flash(), a.vm.pulse());
  yield* waitFor(2);
});
