import {View2D} from '@motion-canvas/2d';
import {createRef} from '@motion-canvas/core';
import {Link, Panel, Service, StateTable} from '../components';
import {Colors} from '../theme';
import {Scaffold, linker, scaffold} from './common';

// MicrovmDriverServerLayout is the Lambda MicroVM layout under the driver
// server proposal (proposals/draft/driver-server.md). There is no shim and no
// S3 staging: the VM's main process is `gritz driver --serve`, and the runner's
// DriverBackend talks to it over the managed proxy.
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
  disk: StateTable;

  links: {
    runnerApi: Link;
    runnerBackend: Link;
    backendLambda: Link;
    backendDriver: Link;
    lambdaDriver: Link;
    driverApi: Link;
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
  const disk = createRef<StateTable>();

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
        y={-240}
        width={1200}
        height={400}
      >
        <Service
          ref={runner}
          name={'Runner'}
          role={'runner.go (unchanged)'}
          activity={'idle'}
          accent={Colors.runner}
          x={-440}
          y={-75}
        />
        <Service
          ref={backend}
          name={'DriverBackend'}
          role={'backend.Backend'}
          accent={Colors.runner}
          x={-440}
          y={95}
        />
        <StateTable
          ref={memory}
          title={'memory'}
          accent={Colors.runner}
          width={250}
          x={-130}
          y={-140}
          offset={[0, -1]}
        />
        <StateTable
          ref={outbox}
          title={'outbox/'}
          accent={Colors.runner}
          width={180}
          x={100}
          y={-140}
          offset={[0, -1]}
        />
        <StateTable
          ref={taskstate}
          title={'taskstate/'}
          accent={Colors.runner}
          width={360}
          x={390}
          y={-140}
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
          x={-500}
          y={-20}
        />
        <Panel
          ref={vm}
          title={'microvm'}
          accent={Colors.driver}
          x={330}
          y={15}
          width={1060}
          height={340}
        >
          <Service
            ref={driver}
            name={'Driver'}
            role={'gritz driver --serve'}
            accent={Colors.driver}
            x={-360}
            y={25}
          />
          <StateTable
            ref={disk}
            title={'disk'}
            accent={Colors.driver}
            width={380}
            x={150}
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
    disk: disk(),
    links: {
      runnerApi: link(runner(), api(), 'Connect RPC'),
      runnerBackend: link(runner(), backend(), 'in-process'),
      backendLambda: link(backend(), lambda(), 'SigV4', 0.3),
      backendDriver: link(backend(), driver(), 'managed proxy :8080', 0.45),
      lambdaDriver: link(lambda(), driver(), 'hooks :9000 (ack)'),
      driverApi: link(driver(), api(), 'Connect RPC', 0.8),
    },
  };
}
