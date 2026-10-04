import {View2D} from '@motion-canvas/2d';
import {createRef} from '@motion-canvas/core';
import {Link, Panel, Service, StateTable} from '../components';
import {Colors} from '../theme';
import {Scaffold, linker, scaffold} from './common';

// MicrovmDriverServerLayout is the Lambda MicroVM layout under the driver
// server proposal (proposals/draft/driver-server.md). There is no shim and no
// S3 staging: the VM's main process is `gritz driver --serve`. The runner
// speaks the driver protocol itself over the managed proxy, the Backend only
// manages the VM, and the driver never talks to the server.
export interface MicrovmDriverServerLayout extends Scaffold {
  server: Panel;
  api: Service;

  host: Panel;
  runner: Service;
  backend: Service;
  memory: StateTable;
  outbox: StateTable;
  taskstate: StateTable;

  aws: Panel;
  lambda: Service;

  vm: Panel;
  driver: Service;
  record: StateTable;
  config: StateTable;
  driverOutbox: StateTable;

  links: {
    runnerApi: Link;
    runnerBackend: Link;
    backendLambda: Link;
    runnerDriver: Link;
    lambdaDriver: Link;
  };
}

export function microvmDriverServerLayout(
  view: View2D,
  title: string,
): MicrovmDriverServerLayout {
  const frame = scaffold(view, title);
  const server = createRef<Panel>();
  const api = createRef<Service>();
  const host = createRef<Panel>();
  const runner = createRef<Service>();
  const backend = createRef<Service>();
  const memory = createRef<StateTable>();
  const outbox = createRef<StateTable>();
  const taskstate = createRef<StateTable>();
  const aws = createRef<Panel>();
  const lambda = createRef<Service>();
  const vm = createRef<Panel>();
  const driver = createRef<Service>();
  const record = createRef<StateTable>();
  const config = createRef<StateTable>();
  const driverOutbox = createRef<StateTable>();

  view.add(
    <>
      <Panel
        ref={server}
        title={'gritz server'}
        accent={Colors.server}
        x={-640}
        y={-240}
        width={240}
        height={320}
      >
        <Service
          ref={api}
          name={'API'}
          role={'Connect RPC'}
          accent={Colors.server}
          width={200}
          y={10}
        />
      </Panel>

      <Panel
        ref={host}
        title={'runner host'}
        accent={Colors.runner}
        x={330}
        y={-245}
        width={1200}
        height={410}
      >
        <Service
          ref={runner}
          name={'Runner'}
          role={'runner.go'}
          activity={'idle'}
          accent={Colors.runner}
          x={-440}
          y={100}
        />
        <Service
          ref={backend}
          name={'Backend'}
          role={'sandbox lifecycle'}
          accent={Colors.runner}
          x={0}
          y={100}
        />
        <StateTable
          ref={memory}
          title={'memory'}
          accent={Colors.runner}
          width={260}
          x={-440}
          y={-160}
          offset={[0, -1]}
        />
        <StateTable
          ref={outbox}
          title={'outbox/'}
          accent={Colors.runner}
          width={240}
          x={-120}
          y={-160}
          offset={[0, -1]}
        />
        <StateTable
          ref={taskstate}
          title={'taskstate/'}
          accent={Colors.runner}
          width={360}
          x={340}
          y={-160}
          offset={[0, -1]}
        />
      </Panel>

      <Panel
        ref={aws}
        title={'AWS'}
        accent={Colors.client}
        x={15}
        y={210}
        width={1830}
        height={400}
      >
        <Service
          ref={lambda}
          name={'Lambda'}
          role={'MicroVMs control plane'}
          accent={Colors.client}
          x={700}
          y={-20}
        />
        <Panel
          ref={vm}
          title={'microvm'}
          accent={Colors.driver}
          x={-235}
          y={15}
          width={1320}
          height={340}
        >
          <Service
            ref={driver}
            name={'Driver'}
            role={'gritz driver --serve'}
            accent={Colors.driver}
            x={320}
            y={30}
          />
          <StateTable
            ref={record}
            title={'/gritz/run.json'}
            accent={Colors.driver}
            width={330}
            x={-430}
            y={-120}
            offset={[0, -1]}
          />
          <StateTable
            ref={config}
            title={'/tmp/gritz/42.json'}
            accent={Colors.driver}
            width={330}
            x={-430}
            y={60}
            offset={[0, -1]}
          />
          <StateTable
            ref={driverOutbox}
            title={'/gritz/outbox/'}
            accent={Colors.driver}
            width={340}
            x={-40}
            y={-120}
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
    memory: memory(),
    outbox: outbox(),
    taskstate: taskstate(),
    aws: aws(),
    lambda: lambda(),
    vm: vm(),
    driver: driver(),
    record: record(),
    config: config(),
    driverOutbox: driverOutbox(),
    links: {
      runnerApi: link(runner(), api(), 'Connect RPC'),
      runnerBackend: link(runner(), backend(), 'in-process'),
      backendLambda: link(backend(), lambda(), 'SigV4', 0.3),
      runnerDriver: link(runner(), driver(), 'managed proxy :8080', 0.22),
      lambdaDriver: link(lambda(), driver(), 'hooks :9000 (ack)', 0.3),
    },
  };
}
