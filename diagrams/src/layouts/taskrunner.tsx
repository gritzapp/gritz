import {View2D} from '@motion-canvas/2d';
import {createRef} from '@motion-canvas/core';
import {Link, Panel, Service, StateTable} from '../components';
import {Colors} from '../theme';
import {Scaffold, linker, scaffold} from './common';

// TaskRunnerLayout is the layout of the taskrunner-owns-sandbox proposal: the
// server, the runner host split into the TaskManager (registry, events
// channel, outbox) and one task's TaskRunner (its locked state and
// taskstate/ record), and the task's sandbox.
export interface TaskRunnerLayout extends Scaffold {
  server: Panel;
  api: Service;

  host: Panel;
  manager: Service;
  events: StateTable;
  registry: StateTable;
  outbox: StateTable;
  taskRunner: Service;
  state: StateTable;
  taskstate: StateTable;

  sandbox: Panel;
  driver: Service;
  wal: StateTable;

  links: {
    managerApi: Link;
    calls: Link;
    events: Link;
    backend: Link;
  };
}

export function taskRunnerLayout(
  view: View2D,
  title: string,
): TaskRunnerLayout {
  const frame = scaffold(view, title);
  const server = createRef<Panel>();
  const api = createRef<Service>();
  const host = createRef<Panel>();
  const manager = createRef<Service>();
  const events = createRef<StateTable>();
  const registry = createRef<StateTable>();
  const outbox = createRef<StateTable>();
  const taskRunner = createRef<Service>();
  const state = createRef<StateTable>();
  const taskstate = createRef<StateTable>();
  const sandbox = createRef<Panel>();
  const driver = createRef<Service>();
  const wal = createRef<StateTable>();

  view.add(
    <>
      <Panel
        ref={server}
        title={'gritz server'}
        accent={Colors.server}
        x={-760}
        y={-220}
        width={320}
        height={300}
      >
        <Service
          ref={api}
          name={'API'}
          role={'Connect RPC'}
          accent={Colors.server}
          y={20}
        />
      </Panel>

      <Panel
        ref={host}
        title={'runner host'}
        accent={Colors.runner}
        x={40}
        y={-20}
        width={1000}
        height={820}
      >
        <Service
          ref={manager}
          name={'TaskManager'}
          role={'policy · spec · slots'}
          activity={'idle'}
          accent={Colors.runner}
          x={-280}
          y={-300}
        />
        <StateTable
          ref={events}
          title={'events (chan TaskEvent)'}
          placeholder={'(empty)'}
          accent={Colors.runner}
          width={360}
          x={-280}
          y={-210}
          offset={[0, -1]}
        />
        <StateTable
          ref={registry}
          title={'registry (m.tasks)'}
          placeholder={'(empty)'}
          accent={Colors.runner}
          width={360}
          x={-280}
          y={-70}
          offset={[0, -1]}
        />
        <StateTable
          ref={outbox}
          title={'outbox/'}
          placeholder={'(empty)'}
          accent={Colors.runner}
          width={360}
          x={-280}
          y={120}
          offset={[0, -1]}
        />
        <Service
          ref={taskRunner}
          name={'TaskRunner 42'}
          role={'owns the sandbox lifecycle'}
          activity={''}
          accent={Colors.runner}
          x={250}
          y={-300}
        />
        <StateTable
          ref={state}
          title={'tr (guarded by tr.mu)'}
          placeholder={'(no TaskRunner)'}
          accent={Colors.runner}
          width={380}
          x={250}
          y={-210}
          offset={[0, -1]}
        />
        <StateTable
          ref={taskstate}
          title={'taskstate/42.json'}
          placeholder={'(no record)'}
          accent={Colors.runner}
          width={380}
          x={250}
          y={50}
          offset={[0, -1]}
        />
      </Panel>

      <Panel
        ref={sandbox}
        title={'sandbox'}
        accent={Colors.driver}
        x={790}
        y={-150}
        width={300}
        height={440}
      >
        <Service
          ref={driver}
          name={'Driver'}
          role={'gritz driver'}
          accent={Colors.driver}
          y={-60}
        />
        <StateTable
          ref={wal}
          title={'/gritz/wal'}
          accent={Colors.driver}
          width={240}
          y={30}
          offset={[0, -1]}
        />
      </Panel>
    </>,
  );

  const link = linker(view);

  return {
    ...frame,
    server: server(),
    api: api(),
    host: host(),
    manager: manager(),
    events: events(),
    registry: registry(),
    outbox: outbox(),
    taskRunner: taskRunner(),
    state: state(),
    taskstate: taskstate(),
    sandbox: sandbox(),
    driver: driver(),
    wal: wal(),
    links: {
      managerApi: link(manager(), api(), 'Connect RPC'),
      calls: link(manager(), taskRunner(), 'method calls'),
      events: link(taskRunner(), events(), 'TaskEvent', 0.6),
      backend: link(taskRunner(), sandbox(), 'backend · driver.v1'),
    },
  };
}
