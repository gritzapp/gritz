import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {replicaLayout} from '../../layouts/replica';
import {Colors} from '../../theme';

// The commit-safe read, traced from proposals/draft/runner-event-replica.md
// (Problem; Server: a commit-safe read with an xid watermark; Server: an
// opaque cursor; Inside taskevents: Replication): two transactions commit out of id order, the
// watermark holds the later id back until the earlier transaction commits,
// and Log stores the page and wakes the tasks' streams.
export default makeScene2D(function* (view) {
  const a = replicaLayout(view, 'ListRunnerEvents reads below the xid watermark');
  const {links} = a;

  a.sandbox.opacity(0);
  links.driver.opacity(0);

  a.pgEvents.putRow('(900, 9)', '42 created');
  a.snapshot.putRow('running', '—');
  a.snapshot.putRow('xmin', '901');
  a.replica.putRow('cursor', '«900, 9»');
  a.replica.putRow('1 · 9', '42 created');
  a.manager.activity('TaskRunner 42');
  a.taskRunner.activity('Read(9) · blocked');
  a.fold.putRow('pos', '9');
  a.fold.putRow('command', 'none');
  a.fold.putRow('sandbox', 'none');

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'Tx A starts task 42. UpdateTask writes a command event in the same transaction: id 10, xid 901.',
  );
  yield* all(
    a.snapshot.setRow('running', '901'),
    a.pgEvents.addRow('(901, 10)', '42 cmd start v1 · open'),
  );

  yield* a.caption.show(
    2,
    'Tx B creates task 43 on runner laptop. Its created event gets id 11 under xid 902.',
  );
  yield* all(
    a.snapshot.setRow('running', '901, 902'),
    a.pgEvents.addRow('(902, 11)', '43 created · open'),
  );

  yield* a.caption.show(
    3,
    'Tx B commits first. Row 11 is now visible and row 10 is not: commit order differs from id order.',
  );
  yield* all(
    a.pgEvents.setRow('(902, 11)', '43 created'),
    a.snapshot.setRow('running', '901'),
  );

  yield* a.caption.show(
    4,
    'Log passes back its opaque cursor. Row 11’s xid 902 is not below xmin 901, so the page is empty.',
  );
  yield* a.log.updateActivity('ListRunnerEvents');
  yield* links.api.send('cursor «900, 9»', {color: Colors.runner});
  yield* all(a.snapshot.flash('xmin'), a.pgEvents.flash('(902, 11)'));
  yield* links.api.send('[]', {back: true, color: Colors.server});
  yield* a.log.updateActivity('page empty · wait');

  yield* a.caption.show(
    5,
    'Tx A commits. No writing transaction is running any more, so xmin moves past both: 903.',
  );
  yield* all(
    a.pgEvents.setRow('(901, 10)', '42 cmd start v1'),
    a.snapshot.setRow('running', '—'),
    a.snapshot.setRow('xmin', '903'),
  );

  yield* a.caption.show(
    6,
    'The runner SSE notification calls Log.Wake, and the poll now returns both rows in (xid, id) order.',
  );
  yield* links.api.send('SSE · wake', {back: true, color: Colors.server});
  yield* a.log.updateActivity('ListRunnerEvents');
  yield* links.api.rpc('cursor «900, 9»', '[10, 11] · next «902, 11»', {color: Colors.runner});

  yield* a.caption.show(
    7,
    'Log stores the page at seq 2 and 3 with next_cursor, in one transaction. It never decodes a cursor.',
  );
  yield* a.log.updateActivity('AppendEvents');
  yield* a.replica.addRow('2 · 10', '42 cmd start v1');
  yield* a.replica.addRow('3 · 11', '43 created');
  yield* a.replica.setRow('cursor', '«902, 11»');

  yield* a.caption.show(
    8,
    'Log reports 43 on Tasks() as new and active, and wakes the blocked Read of task 42.',
  );
  yield* links.tasks.send('{43, active}', {color: Colors.runner});
  yield* a.manager.updateActivity('TaskRunners 42, 43');
  yield* links.read.send('[10]', {color: Colors.runner});
  yield* a.taskRunner.updateActivity('Read(9) → [10]');
  yield* all(a.fold.setRow('pos', '10'), a.fold.setRow('command', 'start v1'));

  yield* a.caption.show(
    9,
    'Row 10 reached the runner although it committed after row 11. Nothing can commit behind the cursor.',
  );
  yield* a.replica.flash('2 · 10');
  yield* waitFor(2);
});
