import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {replicaLayout} from '../../layouts/replica';
import {Colors} from '../../theme';

// A stop, a park and a wake, traced from
// proposals/draft/runner-event-replica.md (Driver: Run becomes a
// bidirectional stream, What the driver does with each event, Runner events
// still go to the server directly, RunEnded; Runner: TaskRunner acts on its
// stream): the driver stops the run and submits stopped itself, the
// TaskRunner parks on RunEnded, and the next start
// boots the sandbox again and resends only the events after the driver's
// Hello position. Picks up where replica-start ends.
export default makeScene2D(function* (view) {
  const a = replicaLayout(view, 'Stop, park, and resume from the driver’s position');
  const {links} = a;

  a.sandbox.subtitle('c1 · up');

  a.pgEvents.putRow('(900, 9)', '42 created');
  a.pgEvents.putRow('(901, 10)', '42 cmd start v1');
  a.pgEvents.putRow('(902, 11)', '43 created');
  a.pgEvents.putRow('(903, 12)', '42 cmd none v1');
  a.snapshot.putRow('running', '—');
  a.snapshot.putRow('xmin', '904');
  a.replica.putRow('cursor', '«903, 12»');
  a.replica.putRow('1 · 9', '42 created');
  a.replica.putRow('2 · 10', '42 cmd start v1');
  a.replica.putRow('3 · 11', '43 created');
  a.replica.putRow('4 · 12', '42 cmd none v1');
  a.log.activity('idle');
  a.manager.activity('TaskRunners 42, 43');
  a.taskRunner.activity('Read(12) · blocked');
  a.fold.putRow('pos', '12');
  a.fold.putRow('command', 'none v1');
  a.fold.putRow('sandbox', 'c1 · up');
  a.fold.putRow('slot', 'held');
  a.driver.activity('run v1');
  a.eventFile.putRow('9', 'created');
  a.eventFile.putRow('10', 'cmd start v1');
  a.eventFile.putRow('12', 'cmd none v1');
  a.run.putRow('version', '1');
  a.run.putRow('state', 'running');
  a.run.putRow('prompted', '10');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'The user stops task 42. UpdateTask saves command STOP and writes cmd stop v1 in the same transaction.',
  );
  yield* a.snapshot.setRow('running', '904');
  yield* a.pgEvents.addRow('(904, 13)', '42 cmd stop v1 · open');
  yield* all(
    a.pgEvents.setRow('(904, 13)', '42 cmd stop v1'),
    a.snapshot.setRow('running', '—'),
    a.snapshot.setRow('xmin', '905'),
  );

  yield* a.caption.show(
    2,
    'Log replicates it and TaskRunner forwards it on the open stream. A running run is the driver’s to stop.',
  );
  yield* links.api.send('SSE · wake', {back: true, color: Colors.server});
  yield* links.api.rpc('cursor «903, 12»', '[13] · next «904, 13»', {color: Colors.runner});
  yield* a.replica.addRow('5 · 13', '42 cmd stop v1');
  yield* a.replica.setRow('cursor', '«904, 13»');
  yield* links.read.send('[13]', {color: Colors.runner});
  yield* all(a.fold.setRow('pos', '13'), a.fold.setRow('command', 'stop v1'));
  yield* links.driver.send('TaskEvents [13]', {color: Colors.runner});
  yield* a.eventFile.addRow('13', 'cmd stop v1');

  yield* a.caption.show(
    3,
    'The driver stops run v1 with ErrStop and submits stopped v1 to the server itself with SubmitRunnerEvents.',
  );
  yield* a.driver.updateActivity('ErrStop · run v1');
  yield* links.submit.send('SubmitRunnerEvents · stopped v1', {
    color: Colors.driver,
    duration: 2,
  });
  yield* a.snapshot.setRow('running', '905');
  yield* a.pgEvents.addRow('(905, 14)', '42 cmd none v1 · open');
  yield* all(
    a.pgEvents.setRow('(905, 14)', '42 cmd none v1'),
    a.snapshot.setRow('running', '—'),
    a.snapshot.setRow('xmin', '906'),
  );
  yield* all(
    a.run.setRow('state', 'finished'),
    a.run.addRow('reported', 'true'),
  );

  yield* a.caption.show(
    4,
    'It sends RunEnded v1, reported. No newer start is wanted, so TaskRunner closes the stream and parks.',
  );
  yield* links.driver.send('RunEnded v1 · reported', {
    back: true,
    color: Colors.driver,
  });
  yield* a.driver.updateActivity('');
  yield* a.taskRunner.updateActivity('close stream · Park');
  yield* links.driver.opacity(0, 0.3);
  yield* all(
    a.sandbox.subtitle('c1 · parked', 0.4),
    a.sandbox.opacity(0.45, 0.5),
    a.fold.setRow('sandbox', 'c1 · parked'),
  );
  yield* links.slots.send('release', {back: true, color: Colors.runner});
  yield* a.fold.setRow('slot', 'free');

  yield* a.caption.show(
    5,
    'Log replicates the clear and TaskRunner folds it. The sandbox is parked, so nothing is forwarded.',
  );
  yield* links.api.send('SSE · wake', {back: true, color: Colors.server});
  yield* links.api.rpc('cursor «904, 13»', '[14] · next «905, 14»', {color: Colors.runner});
  yield* a.replica.addRow('6 · 14', '42 cmd none v1');
  yield* a.replica.setRow('cursor', '«905, 14»');
  yield* links.read.send('[14]', {color: Colors.runner});
  yield* all(a.fold.setRow('pos', '14'), a.fold.setRow('command', 'none v1'));
  yield* a.taskRunner.updateActivity('Read(14) · blocked');

  yield* a.caption.show(
    6,
    'The user adds an instruction and starts v2. One transaction writes both rows, under xid 906.',
  );
  yield* a.snapshot.setRow('running', '906');
  yield* a.pgEvents.addRow('(906, 15)', '42 instruction · open');
  yield* a.pgEvents.addRow('(906, 16)', '42 cmd start v2 · open');
  yield* all(
    a.pgEvents.setRow('(906, 15)', '42 instruction'),
    a.pgEvents.setRow('(906, 16)', '42 cmd start v2'),
    a.snapshot.setRow('running', '—'),
    a.snapshot.setRow('xmin', '907'),
  );

  yield* a.caption.show(
    7,
    'Log replicates both. TaskRunner folds start v2, which has not ended, takes a slot and boots c1 again.',
  );
  yield* links.api.send('SSE · wake', {back: true, color: Colors.server});
  yield* links.api.rpc('cursor «905, 14»', '[15, 16] · next «906, 16»', {color: Colors.runner});
  yield* a.replica.addRow('7 · 15', '42 instruction');
  yield* a.replica.addRow('8 · 16', '42 cmd start v2');
  yield* a.replica.setRow('cursor', '«906, 16»');
  yield* links.read.send('[15, 16]', {color: Colors.runner});
  yield* all(a.fold.setRow('pos', '16'), a.fold.setRow('command', 'start v2'));
  yield* links.slots.send('acquire', {back: true, color: Colors.runner});
  yield* links.slots.send('slot 1/4', {color: Colors.runner});
  yield* a.fold.setRow('slot', 'held');
  yield* a.taskRunner.updateActivity('Boot');
  yield* all(
    a.sandbox.opacity(1, 0.5),
    a.sandbox.subtitle('c1 · up', 0.4),
    a.fold.setRow('sandbox', 'c1 · up'),
  );

  yield* a.caption.show(
    8,
    'A new Run stream opens with RunSpec. The driver read its position back from the file: Hello, last 13.',
  );
  yield* links.driver.opacity(1, 0.3);
  yield* a.taskRunner.updateActivity('Run stream open');
  yield* links.driver.send('RunSpec', {color: Colors.runner});
  yield* a.eventFile.flash('13');
  yield* links.driver.send('Hello · last 13', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    9,
    'TaskRunner reads its Stream after 13 and sends only 14, 15 and 16, the rows the file lacks.',
  );
  yield* a.taskRunner.updateActivity('Stream.Read(13)');
  yield* links.read.send('[14, 15, 16]', {color: Colors.runner});
  yield* links.driver.send('TaskEvents [14..16]', {color: Colors.runner});
  yield* a.eventFile.addRow('14', 'cmd none v1');
  yield* a.eventFile.addRow('15', 'instruction');
  yield* a.eventFile.addRow('16', 'cmd start v2');

  yield* a.caption.show(
    10,
    'none v1 is behind run.json and ignored. start v2 starts run v2, prompted with instruction 15.',
  );
  yield* a.driver.updateActivity('run v2');
  yield* all(
    a.run.setRow('version', '2'),
    a.run.setRow('state', 'running'),
    a.run.setRow('prompted', '16'),
    a.eventFile.flash('15'),
  );
  yield* a.run.dropRow('reported');
  yield* waitFor(2);
});
