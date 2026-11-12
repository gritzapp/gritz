import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {hideTask, workflowsLayout} from '../../layouts/workflows';
import {Colors} from '../../theme';

// Tasks reach each other on the instance bus, traced from
// docs/src/content/docs/proposals/draft/event-workflows.md (Start, Links,
// Publish steps 4-8, EmitEvent under Starting and closing an instance): the
// planner's emit starts an implementer keyed by the subject, the implementer
// links its pull request and holds a claim on it, and events come back to the
// planner through its own entries. Picks up where workflows-trigger ends.
export default makeScene2D(function* (view) {
  const a = workflowsLayout(view, 'Tasks reach each other on the instance bus');
  const {links} = a;

  a.workflows.putRow('jira-feature', 'label · unique');
  a.workflows.putRow('responder', 'mention · unique');
  a.workflows.putRow('responder yield_to', 'pr-owner');
  a.instances.putRow('i/7 jira-feature', 'PROJ-1');
  a.subscriptions.putRow('i/7 · workflow.trigger', 'planner · once');
  a.subscriptions.putRow('i/7 · slice.merged', 'planner · never');
  a.subscriptions.putRow('i/7 · slice.ready', 'implementer · unique');
  a.subscriptions.putRow('PROJ-1 · comment_created', 'link → 42');
  a.taskLinks.putRow('PROJ-1', '42');
  a.planner.activity('running');
  hideTask(a.implementer, links.implementer);
  hideTask(a.responder, links.responder);

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'The planner calls emit with type slice.ready and subject slice-1. EmitEvent stamps source task and bus i/7.',
  );
  yield* links.planner.send('emit slice.ready · slice-1', {back: true, color: Colors.driver});
  yield* a.router.updateActivity('Publish · ns i/7');

  yield* a.caption.show(
    2,
    'An event on an instance bus skips triggers. Phase 2 finds the implementer entry for slice.ready on i/7.',
  );
  yield* a.router.updateActivity('find subscriptions');
  yield* a.subscriptions.flash('i/7 · slice.ready');

  yield* a.caption.show(
    3,
    'start: unique resolves to the implementer task keyed slice-1. None is live, so task 43 starts with that key.',
  );
  yield* a.router.updateActivity('lock · find slice-1');
  yield* all(a.implementer.opacity(1, 0.5), links.implementer.opacity(1, 0.5));
  yield* links.implementer.send('slice.ready', {color: Colors.server});
  yield* a.implementer.updateActivity('running');

  yield* a.caption.show(
    4,
    'link: slice gives task 43 a subscribed link to slice-1, subscribing it to slice.revised for that subject.',
  );
  yield* a.router.updateActivity('link slice-1');
  yield* a.taskLinks.addRow('slice-1', '43');
  yield* a.subscriptions.addRow('slice-1 · slice.revised', 'link → 43');

  yield* a.caption.show(
    5,
    'emit returns what the event did: started [43], delivered []. The planner sees that it reached someone.',
  );
  yield* links.planner.send('started [43]', {color: Colors.server});
  yield* a.router.updateActivity('idle');

  yield* a.caption.show(
    6,
    'Task 43 opens PR 10 and calls create_link with subscribe. Its github.pr link type names the claim pr-owner.',
  );
  yield* links.implementer.send('create_link PR 10', {back: true, color: Colors.driver});
  yield* a.taskLinks.addRow('PR 10', '43 · pr-owner');
  yield* a.subscriptions.addRow('PR 10 · issue_comment', 'link → 43');
  yield* a.subscriptions.addRow('PR 10 · pr_review', 'link → 43');

  yield* a.caption.show(
    7,
    'The planner emits slice.revised for slice-1. Only a link subscription with that subject matches: task 43.',
  );
  yield* links.planner.send('emit slice.revised · slice-1', {back: true, color: Colors.driver});
  yield* a.router.updateActivity('Publish · ns i/7');
  yield* a.subscriptions.flash('slice-1 · slice.revised');
  yield* links.implementer.send('slice.revised', {color: Colors.server});
  yield* a.implementer.updateActivity('woken');

  yield* a.caption.show(
    8,
    'PR 10 merges and task 43 emits slice.merged. The planner entry for it has the default start: never.',
  );
  yield* links.implementer.send('emit slice.merged · slice-1', {back: true, color: Colors.driver});
  yield* a.subscriptions.flash('i/7 · slice.merged');

  yield* a.caption.show(
    9,
    'never starts nothing and delivers to every live planner task: task 42 gets one event row and is woken.',
  );
  yield* a.router.updateActivity('deliver · wake');
  yield* links.planner.send('slice.merged', {color: Colors.server});
  yield* a.planner.updateActivity('woken');
  yield* a.router.updateActivity('idle');
  yield* waitFor(2);
});
