import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {replicaLayout} from '../../layouts/replica';
import {Colors} from '../../theme';

// A start reaching the driver, traced from
// proposals/draft/runner-event-replica.md (Runner: TaskRunner acts on its
// stream; Driver: Run becomes a bidirectional stream; Server: command
// events): the TaskRunner folds start v1, boots the sandbox, opens Run with
// the RunSpec, forwards from the driver's Hello position, and the driver
// starts the run and submits started to the server itself, whose clear
// replicates back down. Picks up where replica-watermark ends.
export default makeScene2D(function* (view) {
  const a = replicaLayout(view, 'A start reaches the driver over Run');
  const {links} = a;

  a.sandbox.opacity(0);
  a.sandbox.subtitle('c1');
  links.driver.opacity(0);

  a.pgEvents.putRow('(900, 9)', '42 created');
  a.pgEvents.putRow('(901, 10)', '42 cmd start v1');
  a.pgEvents.putRow('(902, 11)', '43 created');
  a.snapshot.putRow('running', '—');
  a.snapshot.putRow('xmin', '903');
  a.replica.putRow('(900, 9)', '42 created');
  a.replica.putRow('(901, 10)', '42 cmd start v1');
  a.replica.putRow('(902, 11)', '43 created');
  a.log.activity('idle');
  a.manager.activity('TaskRunners 42, 43');
  a.taskRunner.activity('Read(9) → [10]');
  a.fold.putRow('pos', '10');
  a.fold.putRow('command', 'start v1');
  a.fold.putRow('sandbox', 'none');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'TaskRunner 42 folds its stream: the latest command is start v1 and run v1 has not ended.',
  );
  yield* all(a.taskRunner.updateActivity('want sandbox'), a.fold.flash());

  yield* a.caption.show(
    2,
    'It asks TaskManager for a slot and waits until it gets one, instead of looping through ErrRunning.',
  );
  yield* links.slots.send('acquire', {back: true, color: Colors.runner});
  yield* links.slots.send('slot 1/4', {color: Colors.runner});
  yield* a.fold.addRow('slot', 'held');

  yield* a.caption.show(
    3,
    'The backend creates and boots the sandbox. The driver starts with an empty /gritz/events.jsonl.',
  );
  yield* a.taskRunner.updateActivity('Create · Boot');
  a.sandbox.scale(0.92);
  yield* all(a.sandbox.opacity(1, 0.5), a.sandbox.scale(1, 0.5));
  yield* all(a.sandbox.subtitle('c1 · up', 0.4), a.fold.setRow('sandbox', 'c1 · up'));
  yield* links.driver.opacity(1, 0.3);

  yield* a.caption.show(
    4,
    'TaskRunner opens the Run bidi stream and sends RunSpec first: env, secrets, files and the task token.',
  );
  yield* a.taskRunner.updateActivity('Run stream open');
  yield* links.driver.send('RunSpec', {color: Colors.runner});
  yield* a.driver.updateActivity('spec in memory');

  yield* a.caption.show(
    5,
    'The driver answers with Hello: last_event_id 0, since it has stored nothing, and no run record.',
  );
  yield* links.driver.send('Hello · last 0', {back: true, color: Colors.driver});

  yield* a.caption.show(
    6,
    'TaskRunner reads its Stream from 0 and forwards what the driver lacks. Task 43’s row is not in it.',
  );
  yield* a.taskRunner.updateActivity('Stream.Read(0)');
  yield* links.read.send('[9, 10]', {color: Colors.runner});
  yield* links.driver.send('TaskEvents [9, 10]', {color: Colors.runner});

  yield* a.caption.show(
    7,
    'The driver appends each event to /gritz/events.jsonl and fsyncs before acting on it.',
  );
  yield* a.eventFile.addRow('9', 'created');
  yield* a.eventFile.addRow('10', 'cmd start v1');

  yield* a.caption.show(
    8,
    'start v1: there is no run record and nothing is running, so the driver starts run v1.',
  );
  yield* a.driver.updateActivity('run v1');
  yield* all(
    a.run.addRow('version', '1'),
    a.run.addRow('state', 'running'),
    a.run.addRow('prompted', '10'),
  );

  yield* a.caption.show(
    9,
    'The driver submits started v1 to the server itself with SubmitRunnerEvents and its task token, as today.',
  );
  yield* links.submit.send('SubmitRunnerEvents · started v1', {
    color: Colors.driver,
    duration: 2,
  });

  yield* a.caption.show(
    10,
    'ApplyRunnerEvent consumes the command and, in the same transaction, writes cmd none v1.',
  );
  yield* a.snapshot.setRow('running', '903');
  yield* a.pgEvents.addRow('(903, 12)', '42 cmd none v1 · open');
  yield* all(
    a.pgEvents.setRow('(903, 12)', '42 cmd none v1'),
    a.snapshot.setRow('running', '—'),
    a.snapshot.setRow('xmin', '904'),
  );

  yield* a.caption.show(
    11,
    'The clear replicates down both hops. The run is not over, so the sandbox stays up until RunEnded.',
  );
  yield* links.api.send('SSE · wake', {back: true, color: Colors.server});
  yield* links.api.rpc('after (902, 11)', '[12]', {color: Colors.runner});
  yield* a.replica.addRow('(903, 12)', '42 cmd none v1');
  yield* links.read.send('[12]', {color: Colors.runner});
  yield* all(a.fold.setRow('pos', '12'), a.fold.setRow('command', 'none v1'));
  yield* links.driver.send('TaskEvents [12]', {color: Colors.runner});
  yield* a.eventFile.addRow('12', 'cmd none v1');
  yield* a.taskRunner.updateActivity('Read(12) · blocked');
  yield* waitFor(2);
});
