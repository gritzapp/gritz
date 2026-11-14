import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {hideTask, workflowsLayout} from '../../layouts/workflows';
import {Colors} from '../../theme';

// An external event resolves a workflow instance and starts its first task,
// traced from docs/src/content/docs/proposals/draft/event-workflows.md (Start,
// Storage, Events, Links, Publish steps 1-8). The org has two workflows:
// jira-feature (a trigger on the gritz label, a planner and an implementer)
// and responder (a trigger on bot mentions that yields to pr-owner). The
// planner's task.failed entry is left out of the subscriptions table.
export default makeScene2D(function* (view) {
  const a = workflowsLayout(view, 'A trigger resolves an instance and starts a task');
  const {links} = a;

  a.source.name('Jira');
  a.workflows.putRow('jira-feature', 'label · unique');
  a.workflows.putRow('responder', 'mention · unique');
  a.workflows.putRow('responder yield_to', 'pr-owner');
  hideTask(a.planner, links.planner);
  hideTask(a.implementer, links.implementer);
  hideTask(a.responder, links.responder);

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'Jira reports the gritz label on PROJ-1. External events have no namespace; the router publishes in the org.',
  );
  yield* links.webhook.send('label_added · PROJ-1', {color: Colors.client, duration: 1.6});
  yield* a.router.updateActivity('Publish · ns ""');

  yield* a.caption.show(
    2,
    'Phase 1: the router loads every workflow and matches its triggers. Only jira-feature has one on this event.',
  );
  yield* a.router.updateActivity('match triggers');
  yield* a.workflows.flash('jira-feature');

  yield* a.caption.show(
    3,
    'start: unique looks for an open instance keyed by the subject, under the key lock. None, so it creates i/7.',
  );
  yield* a.router.updateActivity('lock · find PROJ-1');
  yield* a.instances.addRow('i/7 jira-feature', 'PROJ-1');

  yield* a.caption.show(
    4,
    'The instance snapshots the definition and writes a subscriptions row per agent entry, before any task exists.',
  );
  yield* a.subscriptions.addRow('i/7 · workflow.trigger', 'planner · once');
  yield* a.subscriptions.addRow('i/7 · slice.merged', 'planner · never');
  yield* a.subscriptions.addRow('i/7 · slice.ready', 'implementer · unique');

  yield* a.caption.show(
    5,
    'The trigger publishes the system event workflow.trigger on bus i/7 with the subject and body: a nested Publish.',
  );
  yield* a.router.updateActivity('Publish · ns i/7');

  yield* a.caption.show(
    6,
    'Phase 2: select subscriptions by source, type and namespace. The planner entry matches workflow.trigger.',
  );
  yield* a.router.updateActivity('find subscriptions');
  yield* a.subscriptions.flash('i/7 · workflow.trigger');

  yield* a.caption.show(
    7,
    'start: once resolves to the planner task with the empty key. None is live, so task 42 starts with the event.',
  );
  yield* a.router.updateActivity('start planner');
  yield* all(a.planner.opacity(1, 0.5), links.planner.opacity(1, 0.5));
  yield* links.planner.send('workflow.trigger', {color: Colors.server});
  yield* a.planner.updateActivity('running');

  yield* a.caption.show(
    8,
    'The entry has link: jira.issue, so task 42 gets a subscribed link to PROJ-1 and that type’s subscriptions.',
  );
  yield* a.router.updateActivity('link PROJ-1');
  yield* a.taskLinks.addRow('PROJ-1', '42');
  yield* a.subscriptions.addRow('PROJ-1 · comment_created', 'link → 42');

  yield* a.caption.show(
    9,
    'Phase 2 also runs for label_added itself. No subscription is on it, so it reaches no task and is not stored.',
  );
  yield* a.router.updateActivity('Publish · ns ""');
  yield* a.subscriptions.flash();
  yield* a.router.updateActivity('idle');
  yield* waitFor(2);
});
