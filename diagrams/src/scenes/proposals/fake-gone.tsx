import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {fakeBackendLayout} from '../../layouts/fakebackend';
import {Colors} from '../../theme';

// A sandbox the test makes gone, traced from
// proposals/draft/fake-runner-backend.md (Controls, Driver hookup, State
// machine, "Gone at Start, then a fresh sandbox"): Set(c1, Gone) drops the
// attached stream, attach finds StatusGone and the run is lost; the next
// Start finds c1 gone through the stale-handle rule; the one after creates
// c2. Destroyed stays empty: the runner never destroyed c1.
export default makeScene2D(function* (view) {
  const a = fakeBackendLayout(view, 'A sandbox the test makes gone');
  const {links} = a;

  a.sandboxes.putRow('c1', 'up');
  a.snapshot.putRow('Specs', '[]');
  a.snapshot.putRow('Destroyed', '[]');
  a.taskstate.putRow('type', 'fake');
  a.taskstate.putRow('id', 'c1');
  a.taskstate.putRow('version', '1');
  a.taskRunner.activity('Run: attach v1');
  a.run.putRow('v1', 'running');
  a.driver.activity('agent running');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'Run v1 is attached to c1. The test removes the sandbox behind the runner’s back with Set(c1, Gone).',
  );
  yield* a.tester.updateActivity('be.Set("c1", fake.Gone)');
  yield* links.controls.send('Set(c1, Gone)', {color: Colors.client});
  yield* a.sandboxes.setRow('c1', 'gone');

  yield* a.caption.show(
    2,
    'Leaving up closes the runner’s connections to c1’s driver, so the attached Run stream drops.',
  );
  yield* all(a.backend.pulse(), a.driver.updateActivity('unreachable (503)'));

  yield* a.caption.show(
    3,
    'attach checks Status and gets StatusGone. The run is lost and the TaskRunner keeps the record.',
  );
  yield* links.sandbox.rpc('Status', 'StatusGone', {color: Colors.runner});
  yield* links.calls.send('TaskLostEvent v1', {back: true, color: Colors.runner});
  yield* a.manager.updateActivity('outbox: failed v1');
  yield* a.taskRunner.updateActivity('');

  yield* a.caption.show(
    4,
    'm.Start(v2): Start restamps the record, then the gone c1 returns ErrGone. Start clears the record.',
  );
  yield* a.tester.updateActivity('m.Start(42, v2)');
  yield* links.api.send('Start(42, v2)', {color: Colors.client});
  yield* links.calls.send('Start(spec v2)', {color: Colors.runner});
  yield* a.taskstate.setRow('version', '2');
  yield* links.sandbox.rpc('Start', 'ErrGone', {color: Colors.runner});
  yield* all(
    a.taskstate.dropRow('type'),
    a.taskstate.dropRow('id'),
    a.taskstate.dropRow('version'),
  );
  yield* links.api.send('ErrGone', {back: true, color: Colors.runner});

  yield* a.caption.show(
    5,
    'm.Start(v3): the TaskRunner has no sandbox, so it creates one. IDs are never reused, so this is c2.',
  );
  yield* a.tester.updateActivity('m.Start(42, v3)');
  yield* links.api.send('Start(42, v3)', {color: Colors.client});
  yield* links.calls.send('Start(spec v3)', {color: Colors.runner});
  yield* links.sandbox.send('Create(spec)', {color: Colors.runner});
  yield* all(
    a.sandboxes.addRow('c2', 'created'),
    a.snapshot.setRow('Specs', '[42 v3]'),
  );
  yield* a.taskstate.addRow('type', 'fake');
  yield* all(a.taskstate.addRow('id', 'c2'), a.taskstate.addRow('version', '3'));
  yield* links.sandbox.rpc('Start', 'nil', {color: Colors.runner});
  yield* a.sandboxes.setRow('c2', 'up');
  yield* a.driver.updateActivity('');

  yield* a.caption.show(
    6,
    'Destroyed is still empty: a Set to Gone is not a Destroy, so the test can tell vanished from removed.',
  );
  yield* a.tester.updateActivity('assert');
  yield* links.controls.send('Snapshot()', {color: Colors.client});
  yield* all(a.snapshot.flash('Destroyed'), a.sandboxes.flash('c1'));
  yield* waitFor(2);
});
