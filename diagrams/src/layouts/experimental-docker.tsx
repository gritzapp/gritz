import {View2D} from '@motion-canvas/2d';
import {createRef} from '@motion-canvas/core';
import {Link, Panel, Service, StateTable} from '../components';
import {Colors} from '../theme';
import {Scaffold, linker, scaffold} from './common';

// ExperimentalDockerLayout is phase A of proposals/draft/driver-server.md: the
// container runs `gritz driver --serve`, the ExperimentalDocker backend speaks
// the driver protocol behind today's backend.Backend interface, and the driver
// still talks to the server directly.
export interface ExperimentalDockerLayout extends Scaffold {
  server: Panel;
  api: Service;

  host: Panel;
  runner: Service;
  backend: Service;
  dockerd: Service;
  memory: StateTable;
  outbox: StateTable;
  taskstate: StateTable;

  container: Panel;
  driver: Service;
  record: StateTable;
  config: StateTable;

  links: {
    runnerApi: Link;
    runnerBackend: Link;
    backendDockerd: Link;
    dockerdContainer: Link;
    backendDriver: Link;
    driverApi: Link;
  };
}

export function experimentalDockerLayout(
  view: View2D,
  title: string,
): ExperimentalDockerLayout {
  const frame = scaffold(view, title);
  const server = createRef<Panel>();
  const api = createRef<Service>();
  const host = createRef<Panel>();
  const runner = createRef<Service>();
  const backend = createRef<Service>();
  const dockerd = createRef<Service>();
  const memory = createRef<StateTable>();
  const outbox = createRef<StateTable>();
  const taskstate = createRef<StateTable>();
  const container = createRef<Panel>();
  const driver = createRef<Service>();
  const record = createRef<StateTable>();
  const config = createRef<StateTable>();

  view.add(
    <>
      <Panel
        ref={server}
        title={'gritz server'}
        accent={Colors.server}
        x={-700}
        y={-240}
        width={280}
        height={320}
      >
        <Service
          ref={api}
          name={'API'}
          role={'Connect RPC'}
          accent={Colors.server}
          width={220}
          y={10}
        />
      </Panel>

      <Panel
        ref={host}
        title={'runner host'}
        accent={Colors.runner}
        x={250}
        y={-20}
        width={1300}
        height={840}
      >
        <StateTable
          ref={memory}
          title={'memory'}
          accent={Colors.runner}
          width={260}
          x={-470}
          y={-370}
          offset={[0, -1]}
        />
        <StateTable
          ref={outbox}
          title={'outbox/'}
          accent={Colors.runner}
          width={240}
          x={-130}
          y={-370}
          offset={[0, -1]}
        />
        <StateTable
          ref={taskstate}
          title={'taskstate/'}
          accent={Colors.runner}
          width={380}
          x={330}
          y={-370}
          offset={[0, -1]}
        />
        <Service
          ref={runner}
          name={'Runner'}
          role={'runner.go (unchanged)'}
          activity={'idle'}
          accent={Colors.runner}
          x={-470}
          y={-110}
        />
        <Service
          ref={backend}
          name={'ExperimentalDocker'}
          role={'backend.Backend'}
          accent={Colors.runner}
          width={320}
          x={-40}
          y={-110}
        />
        <Service
          ref={dockerd}
          name={'dockerd'}
          role={'Docker Engine API'}
          accent={Colors.client}
          x={420}
          y={-110}
        />
        <Panel
          ref={container}
          title={'container'}
          accent={Colors.driver}
          y={215}
          width={1240}
          height={350}
        >
          <Service
            ref={driver}
            name={'Driver'}
            role={'gritz driver --serve'}
            accent={Colors.driver}
            x={-460}
            y={40}
          />
          <StateTable
            ref={record}
            title={'/gritz/run.json'}
            accent={Colors.driver}
            width={330}
            x={60}
            y={-110}
            offset={[0, -1]}
          />
          <StateTable
            ref={config}
            title={'/tmp/gritz/42.json'}
            accent={Colors.driver}
            width={330}
            x={430}
            y={-110}
            offset={[0, -1]}
          />
        </Panel>
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
    backend: backend(),
    dockerd: dockerd(),
    memory: memory(),
    outbox: outbox(),
    taskstate: taskstate(),
    container: container(),
    driver: driver(),
    record: record(),
    config: config(),
    links: {
      runnerApi: link(runner(), api(), 'Connect RPC'),
      runnerBackend: link(runner(), backend(), 'in-process'),
      backendDockerd: link(backend(), dockerd(), 'Engine API'),
      dockerdContainer: link(dockerd(), container(), 'create / start / stop'),
      backendDriver: link(backend(), driver(), 'container IP :8080', 0.35),
      driverApi: link(driver(), api(), 'direct RPC', 0.55),
    },
  };
}
