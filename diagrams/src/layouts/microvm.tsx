import {View2D} from '@motion-canvas/2d';
import {createRef} from '@motion-canvas/core';
import {Link, Panel, Service, StateTable} from '../components';
import {Colors} from '../theme';
import {Scaffold, linker, scaffold} from './common';

// MicrovmLayout is the gritz layout for the Lambda MicroVM backend. The
// sandbox lives in AWS rather than on the runner host: the runner stages the
// spec in S3, drives the VM through the Lambda control plane, and watches the
// in-VM shim over AWS's managed proxy. The shim runs the driver.
export interface MicrovmLayout extends Scaffold {
  server: Panel;
  api: Service;

  host: Panel;
  runner: Service;
  memory: StateTable;
  outbox: StateTable;
  taskstate: StateTable;

  aws: Panel;
  s3: StateTable;
  lambda: Service;

  vm: Panel;
  shim: Service;
  driver: Service;
  guest: StateTable;

  links: {
    runnerApi: Link;
    runnerS3: Link;
    runnerLambda: Link;
    runnerShim: Link;
    lambdaShim: Link;
    shimS3: Link;
    shimDriver: Link;
    driverApi: Link;
  };
}

export function microvmLayout(view: View2D, title: string): MicrovmLayout {
  const frame = scaffold(view, title);
  const server = createRef<Panel>();
  const api = createRef<Service>();
  const host = createRef<Panel>();
  const runner = createRef<Service>();
  const memory = createRef<StateTable>();
  const outbox = createRef<StateTable>();
  const taskstate = createRef<StateTable>();
  const aws = createRef<Panel>();
  const s3 = createRef<StateTable>();
  const lambda = createRef<Service>();
  const vm = createRef<Panel>();
  const shim = createRef<Service>();
  const driver = createRef<Service>();
  const guest = createRef<StateTable>();

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
          role={'gritz runner'}
          activity={'idle'}
          accent={Colors.runner}
          x={-440}
          y={5}
        />
        <StateTable
          ref={memory}
          title={'memory'}
          accent={Colors.runner}
          width={200}
          x={-150}
          y={-140}
          offset={[0, -1]}
        />
        <StateTable
          ref={outbox}
          title={'outbox/'}
          accent={Colors.runner}
          width={220}
          x={85}
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
        <StateTable
          ref={s3}
          title={'s3://gritz-staging/'}
          accent={Colors.client}
          width={320}
          x={-725}
          y={-20}
          offset={[0, -1]}
        />
        <Service
          ref={lambda}
          name={'Lambda'}
          role={'MicroVMs control plane'}
          accent={Colors.client}
          x={-295}
          y={-80}
        />
        <Panel
          ref={vm}
          title={'microvm'}
          accent={Colors.driver}
          x={425}
          y={15}
          width={940}
          height={340}
        >
          <Service
            ref={shim}
            name={'Shim'}
            role={'gritz tool microvm-shim'}
            accent={Colors.driver}
            x={-300}
            y={25}
          />
          <Service
            ref={driver}
            name={'Driver'}
            role={'gritz driver'}
            accent={Colors.driver}
            x={20}
            y={25}
          />
          <StateTable
            ref={guest}
            title={'guest'}
            accent={Colors.driver}
            width={250}
            x={320}
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
    memory: memory(),
    outbox: outbox(),
    taskstate: taskstate(),
    aws: aws(),
    s3: s3(),
    lambda: lambda(),
    vm: vm(),
    shim: shim(),
    driver: driver(),
    guest: guest(),
    links: {
      runnerApi: link(runner(), api(), 'Connect RPC'),
      runnerS3: link(runner(), s3(), 'S3'),
      runnerLambda: link(runner(), lambda(), 'SigV4'),
      runnerShim: link(runner(), shim(), 'managed proxy :8080', 0.45),
      lambdaShim: link(lambda(), shim(), 'hooks :9000'),
      shimS3: link(shim(), s3(), 'presigned GET', 0.35),
      shimDriver: link(shim(), driver(), 'exec'),
      driverApi: link(driver(), api(), 'Connect RPC', 0.85),
    },
  };
}
