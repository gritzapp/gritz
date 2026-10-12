import {View2D} from '@motion-canvas/2d';
import {createRef} from '@motion-canvas/core';
import {Link, Panel, Service, StateTable} from '../components';
import {Colors} from '../theme';
import {Scaffold, linker, scaffold} from './common';

// DriverWALLayout is the layout of proposals/draft/driver-wal.md: the gritz
// server's API, the runner with its taskstate/ record and event outbox, and a
// sandbox whose driver keeps the WAL (/gritz/wal/) and the run record
// (/gritz/run.json). The agent stands for everything teed into the run's log.
export interface DriverWALLayout extends Scaffold {
  server: Panel;
  api: Service;

  host: Panel;
  runner: Service;
  taskstate: StateTable;
  outbox: StateTable;

  sandbox: Panel;
  driver: Service;
  agent: Service;
  wal: StateTable;
  record: StateTable;

  links: {
    runnerApi: Link;
    runnerDriver: Link;
    agentDriver: Link;
    driverWal: Link;
    driverRecord: Link;
  };
}

export function driverWALLayout(view: View2D, title: string): DriverWALLayout {
  const frame = scaffold(view, title);
  const server = createRef<Panel>();
  const api = createRef<Service>();
  const host = createRef<Panel>();
  const runner = createRef<Service>();
  const taskstate = createRef<StateTable>();
  const outbox = createRef<StateTable>();
  const sandbox = createRef<Panel>();
  const driver = createRef<Service>();
  const agent = createRef<Service>();
  const wal = createRef<StateTable>();
  const record = createRef<StateTable>();

  view.add(
    <>
      <Panel
        ref={server}
        title={'gritz server'}
        accent={Colors.server}
        x={-770}
        y={-150}
        width={300}
        height={260}
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
        x={-250}
        y={-30}
        width={560}
        height={780}
      >
        <Service
          ref={runner}
          name={'Runner'}
          role={'TaskRunner.attach'}
          activity={'idle'}
          accent={Colors.runner}
          y={-250}
        />
        <StateTable
          ref={taskstate}
          title={'taskstate/42.json'}
          accent={Colors.runner}
          width={340}
          y={-130}
          offset={[0, -1]}
        />
        <StateTable
          ref={outbox}
          title={'outbox/'}
          accent={Colors.runner}
          width={340}
          y={110}
          offset={[0, -1]}
        />
      </Panel>

      <Panel
        ref={sandbox}
        title={'sandbox'}
        subtitle={'gritz-42 · running'}
        accent={Colors.driver}
        x={520}
        y={-30}
        width={760}
        height={780}
      >
        <Service
          ref={driver}
          name={'Driver'}
          role={'gritz driver · walClient'}
          accent={Colors.driver}
          x={-200}
          y={-250}
        />
        <Service
          ref={agent}
          name={'Agent'}
          role={'output teed into logship'}
          accent={Colors.driver}
          x={-200}
          y={170}
        />
        <StateTable
          ref={wal}
          title={'/gritz/wal/'}
          accent={Colors.driver}
          placeholder={'(no entries)'}
          width={350}
          x={180}
          y={-330}
          offset={[0, -1]}
        />
        <StateTable
          ref={record}
          title={'/gritz/run.json'}
          accent={Colors.driver}
          width={350}
          x={180}
          y={150}
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
    runner: runner(),
    taskstate: taskstate(),
    outbox: outbox(),
    sandbox: sandbox(),
    driver: driver(),
    agent: agent(),
    wal: wal(),
    record: record(),
    links: {
      runnerApi: link(runner(), api(), 'Connect RPC'),
      runnerDriver: link(runner(), driver(), 'driver.v1', 0.3),
      agentDriver: link(agent(), driver(), 'log tee'),
      driverWal: link(driver(), wal(), 'Append · fsync'),
      driverRecord: link(driver(), record(), 'atomicio'),
    },
  };
}
