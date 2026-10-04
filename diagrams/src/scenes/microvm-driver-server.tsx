import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {microvmDriverServerLayout} from '../layouts/microvm-driver-server';
import {Colors} from '../theme';

// First start of a task on the Lambda MicroVM backend as proposed in
// proposals/draft/driver-server.md: the driver is a long-lived Connect server
// that talks only to the runner, reporting through a durable outbox that Run
// streams and Ack trims. The Backend only manages the VM; Start returns the
// handle alongside any error, and the runner persists any handle it gets back
// before checking the error. The VM is parked by an idle timer.
export default makeScene2D(function* (view) {
  const a = microvmDriverServerLayout(
    view,
    'Lambda MicroVM Start (driver server)',
  );
  const {links} = a;

  a.memory.putRow('sem', '0 / 4');
  a.memory.putRow('lock', '—');
  a.memory.putRow('idle timer', '—');

  // No VM exists until Start creates it.
  a.vm.opacity(0);
  a.vm.subtitle('mvm-7c1e');
  a.record.putRow('state', 'absent');
  a.config.putRow('next_event_token', 'absent');
  links.runnerDriver.opacity(0);
  links.lambdaDriver.opacity(0);

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
    'No taskstate record: take a slot and the task lock, then Start(spec) asks Lambda for a VM.',
  );
  yield* all(
    a.runner.updateActivity('Start(42)'),
    a.memory.setRow('sem', '1 / 4'),
    a.memory.setRow('lock', 'held'),
    a.taskstate.flash(),
  );
  yield* links.runnerBackend.send('Start(spec)', {color: Colors.runner});
  yield* a.backend.updateActivity('Start');
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
    3,
    'The VM boots gritz driver --serve, which acks the /run hook. Start polls until RUNNING.',
  );
  yield* a.driver.updateActivity('boot');
  yield* a.record.flash('state');
  yield* a.driver.updateActivity('serve');
  yield* links.lambdaDriver.opacity(1, 0.3);
  yield* links.lambdaDriver.rpc('/run', '200', {color: Colors.client});
  yield* links.backendLambda.rpc('GetMicrovm', 'RUNNING', {
    color: Colors.runner,
  });
  yield* all(a.vm.subtitle('mvm-7c1e · running', 0.4), a.vm.pulse());

  yield* a.caption.show(
    4,
    'Start returns (handle, err). Any returned handle is persisted before the error is checked.',
  );
  yield* links.runnerBackend.send('handle, nil', {
    back: true,
    color: Colors.runner,
  });
  yield* a.taskstate.addRow('42.json', '');
  yield* all(
    a.taskstate.addRow('  type', 'lambda-microvm'),
    a.taskstate.addRow('  id', 'mvm-7c1e'),
    a.taskstate.addRow('  version', '1'),
  );

  yield* a.caption.show(
    5,
    'Dial mints a port-scoped proxy token. Health reports no run and an empty event cursor.',
  );
  yield* links.runnerBackend.send('Dial', {color: Colors.runner});
  yield* a.backend.updateActivity('Dial');
  yield* links.backendLambda.rpc('CreateMicrovmAuthToken', 'token', {
    color: Colors.runner,
  });
  yield* links.runnerBackend.send('client', {back: true, color: Colors.runner});
  yield* a.backend.updateActivity('');
  yield* links.runnerDriver.opacity(1, 0.3);
  yield* links.runnerDriver.send('Health', {color: Colors.runner});
  yield* a.config.flash('next_event_token');
  yield* links.runnerDriver.send('{run: none, cursor: ""}', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    6,
    'The runner fetches what the driver used to: events after that cursor, links, and a task token.',
  );
  yield* links.runnerApi.rpc('ListEventsByTask(cursor: "")', '[events], next: e118', {
    color: Colors.runner,
  });
  yield* links.runnerApi.rpc('ListLinks', '[links]', {color: Colors.runner});
  yield* links.runnerApi.rpc('CreateTaskToken', 'JWT', {color: Colors.runner});

  yield* a.caption.show(
    7,
    'Run(v1, spec) carries the task, events, cursor e118, links and files. The driver records the run.',
  );
  yield* a.runner.updateActivity('Run(v1, spec)');
  yield* links.runnerDriver.send('Run(v1, spec)', {color: Colors.runner});
  yield* a.record.setRow('state', 'RUNNING');
  yield* a.record.addRow('version', '1');
  yield* a.config.setRow('next_event_token', '""');
  yield* a.driver.updateActivity('run v1');
  yield* links.runnerDriver.send('Accepted', {
    back: true,
    color: Colors.driver,
  });
  yield* all(
    a.memory.setRow('lock', '—'),
    a.runner.updateActivity('supervise'),
  );

  yield* a.caption.show(
    8,
    'The driver reports only through its outbox. Entry 1, started v1, is streamed to the runner.',
  );
  yield* a.driverOutbox.addRow('1', 'started v1');
  yield* links.runnerDriver.send('Entry 1', {back: true, color: Colors.driver});

  yield* a.caption.show(
    9,
    'The runner enqueues it on its own outbox and acks. Its outbox delivers it to the server.',
  );
  yield* a.outbox.addRow('started', 'v1');
  yield* links.runnerDriver.send('Ack(1)', {color: Colors.runner});
  yield* a.driverOutbox.dropRow('1');
  yield* links.runnerApi.send('SubmitRunnerEvents', {color: Colors.runner});
  yield* a.outbox.dropRow('started');

  yield* a.caption.show(
    10,
    'Log chunks take the same path, already masked: entry 2 becomes AppendLogChunk, then Ack(2).',
  );
  yield* a.driver.updateActivity('agent running');
  yield* a.driverOutbox.addRow('2', 'log chunk');
  yield* links.runnerDriver.send('Entry 2', {back: true, color: Colors.driver});
  yield* links.runnerApi.send('AppendLogChunk', {color: Colors.runner});
  yield* links.runnerDriver.send('Ack(2)', {color: Colors.runner});
  yield* a.driverOutbox.dropRow('2');

  yield* a.caption.show(
    11,
    'The agent finishes. The driver saves cursor e118, marks the run finished, and appends stopped, Finished.',
  );
  yield* a.driver.updateActivity('report');
  yield* a.config.setRow('next_event_token', 'e118');
  yield* a.driverOutbox.addRow('3', 'stopped v1');
  yield* a.record.setRow('state', 'FINISHED');
  yield* a.record.addRow('finished_at', '12:04:31');
  yield* a.driverOutbox.addRow('4', 'Finished v1');
  yield* a.driver.updateActivity('idle');

  yield* a.caption.show(
    12,
    'Both entries stream live. The runner enqueues stopped v1 and acks; its outbox delivers it.',
  );
  yield* links.runnerDriver.send('Entry 3', {back: true, color: Colors.driver});
  yield* a.outbox.addRow('stopped', 'v1');
  yield* links.runnerDriver.send('Entry 4', {back: true, color: Colors.driver});
  yield* links.runnerDriver.send('Ack(4)', {color: Colors.runner});
  yield* all(a.driverOutbox.dropRow('3'), a.driverOutbox.dropRow('4'));
  yield* links.runnerApi.send('SubmitRunnerEvents', {color: Colors.runner});
  yield* a.outbox.dropRow('stopped');

  yield* a.caption.show(
    13,
    'Finished v1 frees the slot and arms the idle timer. The VM stays warm for a quick follow-up.',
  );
  yield* all(
    a.memory.setRow('sem', '0 / 4'),
    a.memory.setRow('idle timer', '5m'),
    a.runner.updateActivity('idle'),
  );

  yield* a.caption.show(
    14,
    'The timer fires. Under the lock, Health shows v1 finished, the outbox drained, and 5m elapsed.',
  );
  yield* waitFor(0.5);
  yield* all(
    a.memory.setRow('idle timer', 'fired'),
    a.memory.setRow('lock', 'held'),
    a.runner.updateActivity('park'),
  );
  yield* links.runnerDriver.rpc('Health', '{v1 finished, cursor: e118}', {
    color: Colors.runner,
  });

  yield* a.caption.show(
    15,
    'Park: SuspendMicrovm, then poll until SUSPENDED. The /suspend hook is only acknowledged.',
  );
  yield* links.runnerBackend.send('Park', {color: Colors.runner});
  yield* a.backend.updateActivity('Park');
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
    a.runner.updateActivity('idle'),
  );

  yield* a.caption.show(
    16,
    'run.json, the cursor and the handle survive. The next run’s Health returns e118 to fetch from.',
  );
  yield* all(
    a.taskstate.flash(),
    a.record.flash(),
    a.config.flash('next_event_token'),
    a.vm.pulse(),
  );
  yield* waitFor(2);
});
