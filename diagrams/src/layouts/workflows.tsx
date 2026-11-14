import {View2D} from '@motion-canvas/2d';
import {createRef} from '@motion-canvas/core';
import {Link, Panel, Service, StateTable} from '../components';
import {Colors} from '../theme';
import {Scaffold, linker, scaffold} from './common';

// WorkflowsLayout is the layout of the event-workflows proposal: an external
// source on the left, the server with Router.Publish and the four pieces of
// state it reads and writes (workflow triggers, workflow_instances,
// subscriptions, task_links), and the tasks events reach on the right. Tasks
// are hidden until a scene starts them. Their links carry deliveries from the
// router and, travelling back, the task's own EmitEvent and CreateLink calls.
//
// Table notation:
//   workflows       <workflow> -> <trigger type> · <start>, plus yield_to
//   instances       i/<id> <workflow> -> start key
//   subscriptions   i/<id> · <type> -> <agent> · <start>   (an agent's entry)
//                   <subject> · <type> -> link → <task>     (a link's filter)
//   task_links      <subject> -> <task> [· <claim>]
export interface WorkflowsLayout extends Scaffold {
  source: Service;

  server: Panel;
  router: Service;
  workflows: StateTable;
  instances: StateTable;
  subscriptions: StateTable;
  taskLinks: StateTable;

  tasks: Panel;
  planner: Service;
  implementer: Service;
  responder: Service;

  links: {
    webhook: Link;
    planner: Link;
    implementer: Link;
    responder: Link;
  };
}

export function workflowsLayout(view: View2D, title: string): WorkflowsLayout {
  const frame = scaffold(view, title);
  const source = createRef<Service>();
  const server = createRef<Panel>();
  const router = createRef<Service>();
  const workflows = createRef<StateTable>();
  const instances = createRef<StateTable>();
  const subscriptions = createRef<StateTable>();
  const taskLinks = createRef<StateTable>();
  const tasks = createRef<Panel>();
  const planner = createRef<Service>();
  const implementer = createRef<Service>();
  const responder = createRef<Service>();

  view.add(
    <>
      <Service
        ref={source}
        name={'GitHub'}
        role={'webhook'}
        accent={Colors.client}
        width={180}
        x={-860}
        y={-300}
      />

      <Panel
        ref={server}
        title={'gritz server'}
        accent={Colors.server}
        x={-125}
        y={-20}
        width={1250}
        height={820}
      >
        <Service
          ref={router}
          name={'Router.Publish'}
          role={'one transaction per org'}
          activity={'idle'}
          accent={Colors.server}
          width={300}
          x={460}
          y={-280}
        />
        <StateTable
          ref={workflows}
          title={'workflows · triggers'}
          accent={Colors.store}
          width={360}
          x={-425}
          y={-190}
          offset={[0, -1]}
        />
        <StateTable
          ref={instances}
          title={'workflow_instances'}
          placeholder={'(none open)'}
          accent={Colors.store}
          width={360}
          x={-425}
          y={20}
          offset={[0, -1]}
        />
        <StateTable
          ref={subscriptions}
          title={'subscriptions'}
          accent={Colors.store}
          width={470}
          x={5}
          y={-190}
          offset={[0, -1]}
        />
        <StateTable
          ref={taskLinks}
          title={'task_links'}
          accent={Colors.store}
          width={260}
          x={385}
          y={-190}
          offset={[0, -1]}
        />
      </Panel>

      <Panel
        ref={tasks}
        title={'tasks'}
        accent={Colors.driver}
        x={730}
        y={-20}
        width={420}
        height={820}
      >
        <Service
          ref={planner}
          name={'planner 42'}
          role={'i/7 · key ""'}
          accent={Colors.driver}
          width={280}
          y={-280}
        />
        <Service
          ref={implementer}
          name={'implementer 43'}
          role={'i/7 · key slice-1'}
          accent={Colors.driver}
          width={280}
          y={-40}
        />
        <Service
          ref={responder}
          name={'responder 44'}
          role={'i/8 · key ""'}
          accent={Colors.driver}
          width={280}
          y={220}
        />
      </Panel>
    </>,
  );

  const link = linker(view);

  return {
    ...frame,
    source: source(),
    server: server(),
    router: router(),
    workflows: workflows(),
    instances: instances(),
    subscriptions: subscriptions(),
    taskLinks: taskLinks(),
    tasks: tasks(),
    planner: planner(),
    implementer: implementer(),
    responder: responder(),
    links: {
      webhook: link(source(), router(), 'webhook', 0.12),
      planner: link(router(), planner(), 'gritz.v1'),
      implementer: link(router(), implementer(), ''),
      responder: link(router(), responder(), ''),
    },
  };
}

// hideTask hides a task and its link until a scene starts the task.
export function hideTask(task: Service, link: Link) {
  task.opacity(0);
  link.opacity(0);
}
