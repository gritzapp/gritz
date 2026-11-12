import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {hideTask, workflowsLayout} from '../../layouts/workflows';
import {Colors} from '../../theme';

// A trigger yields to a claim, traced from
// docs/src/content/docs/proposals/draft/event-workflows.md (Claims, Publish
// steps 1-7, CloseWorkflowInstance under Starting and closing an instance): a
// bot mention on a pull request an implementer holds goes to the implementer
// alone, a mention on an unowned pull request starts a responder, and closing
// the responder's instance ends its routing. Picks up where workflows-emit
// ends.
export default makeScene2D(function* (view) {
  const a = workflowsLayout(view, 'A trigger yields to the task that holds a claim');
  const {links} = a;

  a.workflows.putRow('jira-feature', 'label · unique');
  a.workflows.putRow('responder', 'mention · unique');
  a.workflows.putRow('responder yield_to', 'pr-owner');
  a.instances.putRow('i/7 jira-feature', 'PROJ-1');
  a.subscriptions.putRow('i/7 · workflow.trigger', 'planner · once');
  a.subscriptions.putRow('i/7 · slice.merged', 'planner · never');
  a.subscriptions.putRow('i/7 · slice.ready', 'implementer · unique');
  a.subscriptions.putRow('PROJ-1 · comment_created', 'link → 42');
  a.subscriptions.putRow('slice-1 · slice.revised', 'link → 43');
  a.subscriptions.putRow('PR 10 · issue_comment', 'link → 43');
  a.subscriptions.putRow('PR 10 · pr_review', 'link → 43');
  a.taskLinks.putRow('PROJ-1', '42');
  a.taskLinks.putRow('slice-1', '43');
  a.taskLinks.putRow('PR 10', '43 · pr-owner');
  hideTask(a.responder, links.responder);

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'Someone mentions the bot on PR 10, which task 43 linked. GitHub sends issue_comment with that subject.',
  );
  yield* links.webhook.send('issue_comment · PR 10', {color: Colors.client, duration: 1.6});
  yield* a.router.updateActivity('Publish · ns ""');

  yield* a.caption.show(
    2,
    'Phase 1: responder’s trigger matches. start: unique finds no open responder instance keyed PR 10.',
  );
  yield* a.router.updateActivity('lock · find PR 10');
  yield* a.workflows.flash('responder');
  yield* a.instances.flash();

  yield* a.caption.show(
    3,
    'Before creating one, yield_to looks for a live holder of pr-owner on PR 10. Task 43 is one: nothing starts.',
  );
  yield* a.workflows.flash('responder yield_to');
  yield* a.taskLinks.flash('PR 10');
  yield* a.router.updateActivity('yield · holder 43');

  yield* a.caption.show(
    4,
    'Phase 2 runs regardless. Task 43’s link subscribes to issue_comment on PR 10, so it alone gets the mention.',
  );
  yield* a.router.updateActivity('find subscriptions');
  yield* a.subscriptions.flash('PR 10 · issue_comment');
  yield* links.implementer.send('issue_comment', {color: Colors.server});
  yield* a.implementer.updateActivity('woken');

  yield* a.caption.show(
    5,
    'A mention on PR 11, which no task links. The trigger matches, finds no instance and no holder of pr-owner.',
  );
  yield* links.webhook.send('issue_comment · PR 11', {color: Colors.client, duration: 1.6});
  yield* a.router.updateActivity('lock · find PR 11');
  yield* a.taskLinks.flash();

  yield* a.caption.show(
    6,
    'It creates instance i/8 keyed PR 11, with a subscriptions row for the responder’s workflow.trigger entry.',
  );
  yield* a.instances.addRow('i/8 responder', 'PR 11');
  yield* a.subscriptions.addRow('i/8 · workflow.trigger', 'responder · once');

  yield* a.caption.show(
    7,
    'workflow.trigger on i/8 starts responder 44, and the entry’s link: github.pr links PR 11 to it.',
  );
  yield* a.router.updateActivity('Publish · ns i/8');
  yield* all(a.responder.opacity(1, 0.5), links.responder.opacity(1, 0.5));
  yield* links.responder.send('workflow.trigger', {color: Colors.server});
  yield* a.responder.updateActivity('running');
  yield* a.taskLinks.addRow('PR 11', '44');

  yield* a.caption.show(
    8,
    'A second mention on PR 11 finds i/8 by key. Its workflow.trigger goes to task 44, which once finds live.',
  );
  yield* links.webhook.send('issue_comment · PR 11', {color: Colors.client, duration: 1.6});
  yield* a.instances.flash('i/8 responder');
  yield* a.subscriptions.flash('i/8 · workflow.trigger');
  yield* links.responder.send('workflow.trigger', {color: Colors.server});
  yield* a.responder.updateActivity('woken');

  yield* a.caption.show(
    9,
    'A person closes i/8. Triggers no longer find it and its subscriptions stop matching: task 44 hears nothing.',
  );
  yield* a.instances.setRow('i/8 responder', 'closed');
  yield* all(
    a.responder.opacity(0.4, 0.5),
    links.responder.opacity(0.4, 0.5),
    a.responder.activity('', 0.4),
  );
  yield* a.router.updateActivity('idle');
  yield* waitFor(2);
});
