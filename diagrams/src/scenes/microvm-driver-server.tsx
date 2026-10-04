import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {microvmDriverServerLayout} from '../layouts/microvm-driver-server';
import {Colors} from '../theme';

// First start of a task on the Lambda MicroVM backend as proposed in
// proposals/draft/driver-server.md: the driver is a long-lived Connect server
// that talks only to the runner, reporting through a durable response log that
// Run streams from the runner's position (the seq in its taskstate record). The Backend only manages the VM; Start returns the
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
    a.taskstate.addRow('  seq', '0'),
  );

  yield* a.caption.show(
    5,
    'The runner signs a JWT with its own key. Dial adds a proxy token; Status shows no run, empty cursor.',
  );
  yield* a.runner.updateActivity('sign JWT');
  yield* links.runnerBackend.send('Dial(jwt)', {color: Colors.runner});
  yield* a.backend.updateActivity('Dial');
  yield* links.backendLambda.rpc('CreateMicrovmAuthToken', 'token', {
    color: Colors.runner,
  });
  yield* links.runnerBackend.send('client', {back: true, color: Colors.runner});
  yield* a.backend.updateActivity('');
  yield* links.runnerDriver.opacity(1, 0.3);
  yield* links.runnerDriver.send('Status', {color: Colors.runner});
  yield* a.config.flash('next_event_token');
  yield* links.runnerDriver.send('{run: none, cursor: ""}', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    6,
    'The runner fetches events after that cursor and links, plus a task token for the agent’s MCP server.',
  );
  yield* links.runnerApi.rpc('ListEventsByTask(cursor: "")', '[events], next: e118', {
    color: Colors.runner,
  });
  yield* links.runnerApi.rpc('ListLinks', '[links]', {color: Colors.runner});
  yield* links.runnerApi.rpc('CreateTaskToken', 'task token', {color: Colors.runner});

  yield* a.caption.show(
    7,
    'Run(v1, spec) carries the task, events, cursor e118, links and files. The driver records the run.',
  );
  yield* a.runner.updateActivity('Run(v1, spec)');
  yield* links.runnerDriver.send('Run(v1, spec, after_seq: 0)', {color: Colors.runner});
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
    'The driver reports only through its response log. #1, started v1, streams to the runner.',
  );
  yield* a.driverLog.addRow('#1', 'started v1');
  yield* links.runnerDriver.send('#1 started v1', {back: true, color: Colors.driver});

  yield* a.caption.show(
    9,
    'The runner enqueues it on its own outbox, then records seq 1 in taskstate. Nothing goes back.',
  );
  yield* a.outbox.addRow('started', 'v1');
  yield* a.taskstate.setRow('  seq', '1');
  yield* links.runnerApi.send('SubmitRunnerEvents', {color: Colors.runner});
  yield* a.outbox.dropRow('started');

  yield* a.caption.show(
    10,
    'Log chunks take the same path, already masked: #2 becomes AppendLogChunk, then seq 2.',
  );
  yield* a.driver.updateActivity('agent running');
  yield* a.driverLog.addRow('#2', 'log chunk');
  yield* links.runnerDriver.send('#2 log chunk', {back: true, color: Colors.driver});
  yield* links.runnerApi.send('AppendLogChunk', {color: Colors.runner});
  yield* a.taskstate.setRow('  seq', '2');

  yield* a.caption.show(
    11,
    'The agent finishes. The driver saves cursor e118, marks the run finished, and logs stopped, Finished.',
  );
  yield* a.driver.updateActivity('report');
  yield* a.config.setRow('next_event_token', 'e118');
  yield* a.driverLog.addRow('#3', 'stopped v1');
  yield* a.record.setRow('state', 'FINISHED');
  yield* a.record.addRow('finished_at', '12:04:31');
  yield* a.driverLog.addRow('#4', 'Finished v1');
  yield* a.driver.updateActivity('idle');

  yield* a.caption.show(
    12,
    'Both stream live. The runner enqueues stopped v1 and records seq 4. The log stays until the next run.',
  );
  yield* links.runnerDriver.send('#3 stopped v1', {back: true, color: Colors.driver});
  yield* a.outbox.addRow('stopped', 'v1');
  yield* links.runnerDriver.send('#4 Finished v1', {back: true, color: Colors.driver});
  yield* a.taskstate.setRow('  seq', '4');
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
    'The timer fires. Under the lock, Status shows v1 finished, last_seq 4 = taskstate seq, 5m elapsed.',
  );
  yield* waitFor(0.5);
  yield* all(
    a.memory.setRow('idle timer', 'fired'),
    a.memory.setRow('lock', 'held'),
    a.runner.updateActivity('park'),
  );
  yield* links.runnerDriver.rpc('Status', '{v1 finished, last_seq: 4}', {
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
    'The handle with seq 4, run.json and cursor e118 survive. The next run sends after_seq: 4.',
  );
  yield* all(
    a.taskstate.flash('  seq'),
    a.driverLog.flash(),
    a.record.flash(),
    a.config.flash('next_event_token'),
    a.vm.pulse(),
  );
  yield* waitFor(2);
});
