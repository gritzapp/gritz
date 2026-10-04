import {View2D} from '@motion-canvas/2d';
import {createRef} from '@motion-canvas/core';
import {Link, Panel, Service, StateTable} from '../components';
import {Colors} from '../theme';
import {Scaffold, linker, scaffold} from './common';

// DockerLayout is the gritz layout for the Docker backend: client, server,
// postgres, and a runner host with one task container. Flows start from it,
// set the initial state they need, and script the messages.
export interface DockerLayout extends Scaffold {
  client: Service;

  server: Panel;
  api: Service;
  pubsub: Service;

  postgres: Panel;
  tasks: StateTable;
  events: StateTable;

  host: Panel;
  runner: Service;
  memory: StateTable;
  outbox: StateTable;
  taskstate: StateTable;

  container: Panel;
  driver: Service;
  config: StateTable;

  links: {
    clientApi: Link;
    apiPostgres: Link;
    apiPubsub: Link;
    pubsubRunner: Link;
    runnerApi: Link;
    runnerContainer: Link;
    driverApi: Link;
  };
}

export function dockerLayout(view: View2D, title: string): DockerLayout {
  const frame = scaffold(view, title);
  const client = createRef<Service>();
  const server = createRef<Panel>();
  const api = createRef<Service>();
  const pubsub = createRef<Service>();
  const postgres = createRef<Panel>();
  const tasks = createRef<StateTable>();
  const events = createRef<StateTable>();
  const host = createRef<Panel>();
  const runner = createRef<Service>();
  const memory = createRef<StateTable>();
  const outbox = createRef<StateTable>();
  const taskstate = createRef<StateTable>();
  const container = createRef<Panel>();
  const driver = createRef<Service>();
  const config = createRef<StateTable>();

  view.add(
    <>
      <Panel
        ref={postgres}
        title={'Postgres'}
        accent={Colors.store}
        x={-640}
        y={-140}
        width={500}
        height={560}
      >
        <StateTable
          ref={tasks}
          title={'tasks'}
          accent={Colors.store}
          placeholder={'(no row)'}
          width={440}
          y={-220}
          offset={[0, -1]}
        />
        <StateTable
          ref={events}
          title={'task_events'}
          accent={Colors.store}
          width={440}
          y={30}
          offset={[0, -1]}
        />
      </Panel>

      <Service
        ref={client}
        name={'Client'}
        role={'web UI / CLI / MCP'}
        accent={Colors.client}
        x={-640}
        y={300}
      />

      <Panel
        ref={server}
        title={'gritz server'}
        accent={Colors.server}
        x={-110}
        y={-60}
        width={330}
        height={620}
      >
        <Service
          ref={pubsub}
          name={'PubSub'}
          role={'notifyserver'}
          accent={Colors.server}
          y={-150}
        />
        <Service
          ref={api}
          name={'API'}
          role={'apiserver · Connect RPC'}
          accent={Colors.server}
          y={170}
        />
      </Panel>

      <Panel
        ref={host}
        title={'runner host'}
        accent={Colors.runner}
        x={560}
        y={-10}
        width={740}
        height={820}
      >
        <Service
          ref={runner}
          name={'Runner'}
          role={'gritz runner'}
          activity={'idle'}
          accent={Colors.runner}
          x={-190}
          y={-250}
        />
        <StateTable
          ref={memory}
          title={'memory'}
          accent={Colors.runner}
          x={180}
          y={-350}
          offset={[0, -1]}
        />
        <StateTable
          ref={outbox}
          title={'outbox/'}
          accent={Colors.runner}
          x={180}
          y={-245}
          offset={[0, -1]}
        />
        <StateTable
          ref={taskstate}
          title={'taskstate/'}
          accent={Colors.runner}
          x={180}
          y={-140}
          offset={[0, -1]}
        />
        <Panel
          ref={container}
          title={'container'}
          accent={Colors.driver}
          y={230}
          width={680}
          height={320}
        >
          <Service
            ref={driver}
            name={'Driver'}
            role={'gritz driver'}
            accent={Colors.driver}
            x={-165}
            y={20}
          />
          <StateTable
            ref={config}
            title={'/tmp/gritz/<id>.json'}
            accent={Colors.driver}
            x={165}
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
    client: client(),
    server: server(),
    api: api(),
    pubsub: pubsub(),
    postgres: postgres(),
    tasks: tasks(),
    events: events(),
    host: host(),
    runner: runner(),
    memory: memory(),
    outbox: outbox(),
    taskstate: taskstate(),
    container: container(),
    driver: driver(),
    config: config(),
    links: {
      clientApi: link(client(), api(), 'Connect RPC'),
      apiPostgres: link(api(), postgres(), 'SQL'),
      apiPubsub: link(api(), pubsub(), 'publish'),
      pubsubRunner: link(pubsub(), runner(), 'SSE'),
      runnerApi: link(runner(), api(), 'Connect RPC'),
      runnerContainer: link(runner(), container(), 'docker'),
      driverApi: link(driver(), api(), 'Connect RPC'),
    },
  };
}
