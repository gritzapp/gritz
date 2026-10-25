import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {fakeBackendLayout} from '../../layouts/fakebackend';
import {Colors} from '../../theme';

// Two runner processes over one fake, traced from
// proposals/draft/fake-runner-backend.md (State machine: handles share state;
// TaskManager.Wait; "a runner restart mid-run"): the test Adds an up c1 with
// its record, the first manager's Load reattaches and then shuts down, and
// the second manager's Load opens the same c1 and follows the run to its end.
export default makeScene2D(function* (view) {
  const a = fakeBackendLayout(view, 'Two runner processes, one fake');
  const {links} = a;

  a.snapshot.putRow('Specs', '[]');
  a.snapshot.putRow('Destroyed', '[]');
  a.run.putRow('v3', 'running');
  a.driver.activity('agent running');
  a.manager.name('TaskManager #1');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'Arrange as a previous process would have left it: Add(c1, Up) in the fake and a v3 record in the store.',
  );
  yield* a.tester.updateActivity('be.Add("c1", fake.Up)');
  yield* links.controls.send('Add(c1, Up)', {color: Colors.client});
  yield* a.sandboxes.addRow('c1', 'up');
  yield* a.taskstate.addRow('type', 'fake');
  yield* all(a.taskstate.addRow('id', 'c1'), a.taskstate.addRow('version', '3'));

  yield* a.caption.show(
    2,
    'first.Load: Restore opens {fake, c1}, Status says up, so the TaskRunner is claimed and Run reattaches.',
  );
  yield* a.tester.updateActivity('first.Load(ctx)');
  yield* links.api.send('Load(ctx)', {color: Colors.client});
  yield* links.calls.send('Restore(rec v3)', {color: Colors.runner});
  yield* links.sandbox.rpc('Open · Status', 'StatusUp', {color: Colors.runner});
  yield* a.taskRunner.updateActivity('Run: attach v3');
  yield* links.sandbox.send('Dial', {color: Colors.runner});
  yield* a.sandboxes.setRow('c1', 'up · dials 1');
  yield* links.driver.rpc('Run(v3)', 'Accepted', {color: Colors.store});

  yield* a.caption.show(
    3,
    'The test cancels ctx and calls first.Wait: the Run goroutine returns without parking c1.',
  );
  yield* a.tester.updateActivity('cancel(); first.Wait()');
  yield* a.taskRunner.updateActivity('');
  yield* links.api.send('Wait(ctx)', {color: Colors.client});
  yield* links.api.send('nil', {back: true, color: Colors.runner});
  yield* a.sandboxes.flash('c1');

  yield* a.caption.show(
    4,
    'A second manager on the same fake loads the record. Its Open of c1 shares state with the first one.',
  );
  yield* all(
    a.manager.updateActivity('fresh registry'),
    a.taskRunner.updateActivity(''),
  );
  a.manager.name('TaskManager #2');
  yield* a.tester.updateActivity('m.Load(ctx)');
  yield* links.api.send('Load(ctx)', {color: Colors.client});
  yield* links.calls.send('Restore(rec v3)', {color: Colors.runner});
  yield* links.sandbox.rpc('Open · Status', 'StatusUp', {color: Colors.runner});
  yield* a.taskRunner.updateActivity('Run: attach v3');
  yield* links.sandbox.send('Dial', {color: Colors.runner});
  yield* a.sandboxes.setRow('c1', 'up · dials 2');
  yield* links.driver.rpc('Run(v3)', 'Accepted', {color: Colors.store});

  yield* a.caption.show(
    5,
    'The test stops the run over its own driver client. attach gets Finished and parks c1.',
  );
  yield* a.tester.updateActivity('stopRun(client, 3)');
  yield* a.run.setRow('v3', 'finished · reported');
  yield* links.driver.send('Finished', {back: true, color: Colors.driver});
  yield* links.sandbox.rpc('Park', 'nil', {color: Colors.runner});
  yield* a.sandboxes.setRow('c1', 'parked · parks 1');
  yield* a.taskRunner.updateActivity('');

  yield* a.caption.show(
    6,
    'm.Wait returns, and the test asserts c1 parked and the outbox empty: the driver reported the run.',
  );
  yield* a.tester.updateActivity('m.Wait(); assert');
  yield* links.api.send('Wait(ctx)', {color: Colors.client});
  yield* links.api.send('nil', {back: true, color: Colors.runner});
  yield* links.controls.send('Snapshot()', {color: Colors.client});
  yield* a.sandboxes.flash('c1');
  yield* waitFor(2);
});
