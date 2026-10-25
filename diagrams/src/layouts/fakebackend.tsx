import {View2D} from '@motion-canvas/2d';
import {createRef} from '@motion-canvas/core';
import {Link, Panel, Service, StateTable} from '../components';
import {Colors} from '../theme';
import {Scaffold, linker, scaffold} from './common';

// FakeBackendLayout is the layout of the fake-runner-backend proposal: a
// runner_test test on top, driving the runner through its exported API and
// the fake through its controls; the runner (TaskManager, one TaskRunner and
// its taskstate/ record); the fake backend with its sandbox table and the
// snapshot tests assert on; and the in-process driverserver the fake serves.
export interface FakeBackendLayout extends Scaffold {
  test: Panel;
  tester: Service;

  runner: Panel;
  manager: Service;
  taskRunner: Service;
  taskstate: StateTable;

  fake: Panel;
  backend: Service;
  sandboxes: StateTable;
  snapshot: StateTable;

  driverPanel: Panel;
  driver: Service;
  run: StateTable;

  links: {
    api: Link;
    controls: Link;
    calls: Link;
    sandbox: Link;
    driver: Link;
  };
}

export function fakeBackendLayout(
  view: View2D,
  title: string,
): FakeBackendLayout {
  const frame = scaffold(view, title);
  const test = createRef<Panel>();
  const tester = createRef<Service>();
  const runner = createRef<Panel>();
  const manager = createRef<Service>();
  const taskRunner = createRef<Service>();
  const taskstate = createRef<StateTable>();
  const fake = createRef<Panel>();
  const backend = createRef<Service>();
  const sandboxes = createRef<StateTable>();
  const snapshot = createRef<StateTable>();
  const driverPanel = createRef<Panel>();
  const driver = createRef<Service>();
  const run = createRef<StateTable>();

  view.add(
    <>
      <Panel
        ref={test}
        title={'go test'}
        subtitle={'package runner_test'}
        accent={Colors.client}
        x={-140}
        y={-345}
        width={1300}
        height={150}
      >
        <Service
          ref={tester}
          name={'Test'}
          role={'exported API only'}
          activity={''}
          accent={Colors.client}
          y={10}
          height={110}
          width={340}
        />
      </Panel>

      <Panel
        ref={runner}
        title={'internal/runner'}
        accent={Colors.runner}
        x={-580}
        y={120}
        width={680}
        height={600}
      >
        <Service
          ref={manager}
          name={'TaskManager'}
          role={'Load · Start · Poll · Wait'}
          activity={''}
          accent={Colors.runner}
          x={-170}
          y={-190}
        />
        <Service
          ref={taskRunner}
          name={'TaskRunner 42'}
          role={'Start · Restore · Run'}
          activity={''}
          accent={Colors.runner}
          x={170}
          y={-190}
        />
        <StateTable
          ref={taskstate}
          title={'taskstate/42.json (test-owned store)'}
          placeholder={'(no record)'}
          accent={Colors.runner}
          width={520}
          y={-80}
          offset={[0, -1]}
        />
      </Panel>

      <Panel
        ref={fake}
        title={'backend/fake'}
        subtitle={'in memory'}
        accent={Colors.store}
        x={150}
        y={120}
        width={720}
        height={600}
      >
        <Service
          ref={backend}
          name={'fake.Backend'}
          role={'backend.Sandbox state machine'}
          activity={''}
          accent={Colors.store}
          y={-190}
          width={320}
        />
        <StateTable
          ref={sandboxes}
          title={'sandboxes (by ID)'}
          placeholder={'(none)'}
          accent={Colors.store}
          width={320}
          x={-180}
          y={-100}
          offset={[0, -1]}
        />
        <StateTable
          ref={snapshot}
          title={'Snapshot()'}
          accent={Colors.store}
          width={320}
          x={180}
          y={-100}
          offset={[0, -1]}
        />
      </Panel>

      <Panel
        ref={driverPanel}
        title={'driver'}
        subtitle={'http.Handler'}
        accent={Colors.driver}
        x={730}
        y={120}
        width={340}
        height={600}
      >
        <Service
          ref={driver}
          name={'driverserver'}
          role={'in-process, test-built'}
          activity={''}
          accent={Colors.driver}
          y={-190}
          width={280}
        />
        <StateTable
          ref={run}
          title={'run'}
          placeholder={'(never run)'}
          accent={Colors.driver}
          width={280}
          y={-100}
          offset={[0, -1]}
        />
      </Panel>
    </>,
  );

  const link = linker(view);

  return {
    ...frame,
    test: test(),
    tester: tester(),
    runner: runner(),
    manager: manager(),
    taskRunner: taskRunner(),
    taskstate: taskstate(),
    fake: fake(),
    backend: backend(),
    sandboxes: sandboxes(),
    snapshot: snapshot(),
    driverPanel: driverPanel(),
    driver: driver(),
    run: run(),
    links: {
      api: link(tester(), manager(), 'exported API', 0.4),
      controls: link(tester(), backend(), 'Add · Set · FailNext · Wait', 0.6),
      calls: link(manager(), taskRunner(), 'calls'),
      sandbox: link(taskRunner(), backend(), 'backend.Sandbox'),
      driver: link(backend(), driver(), 'httptest · up only'),
    },
  };
}
