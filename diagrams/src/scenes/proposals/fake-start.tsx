import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {fakeBackendLayout} from '../../layouts/fakebackend';
import {Colors} from '../../theme';

// A run on the fake backend, traced from
// proposals/draft/fake-runner-backend.md (State machine, Observations, Driver
// hookup, "a sandbox that fails to start"): the test calls TaskManager.Start,
// the fake records the Spec and takes c1 created -> up -> parked, its driver
// answers only while c1 is up, and the test waits with TaskManager.Wait and
// asserts on Snapshot.
export default makeScene2D(function* (view) {
  const a = fakeBackendLayout(view, 'A run on the fake backend');
  const {links} = a;

  a.snapshot.putRow('Specs', '[]');
  a.snapshot.putRow('Destroyed', '[]');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'The test builds fake.New with a Driver handler from a real driverserver, then calls m.Start(task 42, v1).',
  );
  yield* a.tester.updateActivity('m.Start(42, v1)');
  yield* links.api.send('Start(42, v1)', {color: Colors.client});
  yield* a.manager.updateActivity('Start(42, v1)');
  yield* links.calls.send('Start(spec v1)', {color: Colors.runner});
  yield* a.taskRunner.updateActivity('Start · tr.mu held');

  yield* a.caption.show(
    2,
    'TaskRunner.Start calls Create. The fake keeps a copy of the Spec and adds c1 in state created.',
  );
  yield* links.sandbox.send('Create(spec)', {color: Colors.runner});
  yield* all(
    a.sandboxes.addRow('c1', 'created'),
    a.snapshot.setRow('Specs', '[42 v1]'),
  );

  yield* a.caption.show(
    3,
    'The TaskRunner records the handle {fake, c1} at v1 in the test-owned store, then calls Start: c1 is up.',
  );
  yield* a.taskstate.addRow('type', 'fake');
  yield* all(a.taskstate.addRow('id', 'c1'), a.taskstate.addRow('version', '1'));
  yield* links.sandbox.rpc('Start', 'nil', {color: Colors.runner});
  yield* a.sandboxes.setRow('c1', 'up · starts 1');

  yield* a.caption.show(
    4,
    'Run dials c1. The fake serves the handler on its own httptest server, and answers only while c1 is up.',
  );
  yield* a.taskRunner.updateActivity('Run: startRun v1');
  yield* links.sandbox.send('Dial', {color: Colors.runner});
  yield* a.sandboxes.setRow('c1', 'up · dials 1');
  yield* links.driver.rpc('Run(v1, spec)', 'Accepted', {color: Colors.store});
  yield* a.run.addRow('v1', 'running');
  yield* a.driver.updateActivity('agent running');

  yield* a.caption.show(
    5,
    'The agent finishes and the driver reports. attach gets Finished and parks c1.',
  );
  yield* a.run.setRow('v1', 'finished · reported');
  yield* links.driver.send('Finished', {back: true, color: Colors.driver});
  yield* links.sandbox.rpc('Park', 'nil', {color: Colors.runner});
  yield* a.sandboxes.setRow('c1', 'parked · parks 1');

  yield* a.caption.show(
    6,
    'Leaving up closes the runner’s connections to the driver, the way stopping a container kills it.',
  );
  yield* all(a.backend.pulse(), a.driver.updateActivity('unreachable (503)'));
  yield* a.taskRunner.updateActivity('');

  yield* a.caption.show(
    7,
    'm.Wait returns once the Run goroutine is done: only then is "no event owed" a real assertion.',
  );
  yield* links.api.send('Wait(ctx)', {color: Colors.client});
  yield* a.manager.updateActivity('Run goroutines: 0');
  yield* links.api.send('nil', {back: true, color: Colors.runner});

  yield* a.caption.show(
    8,
    'The test asserts on the store, on m.Runner(42).Record(), and on Snapshot: Specs and c1 parked.',
  );
  yield* a.tester.updateActivity('assert');
  yield* links.controls.send('Snapshot()', {color: Colors.client});
  yield* links.controls.send('{c1 parked, Specs [42 v1]}', {
    back: true,
    color: Colors.store,
  });
  yield* all(a.snapshot.flash(), a.sandboxes.flash('c1'), a.taskstate.flash());
  yield* waitFor(2);
});
