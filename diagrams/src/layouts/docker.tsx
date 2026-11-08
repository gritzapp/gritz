import {View2D} from '@motion-canvas/2d';
import {createRef} from '@motion-canvas/core';
import {Link, Panel, Service, StateTable} from '../components';
import {Colors} from '../theme';
import {Scaffold, linker, scaffold} from './common';

// DockerLayout is the gritz layout for the Docker backend: client, server,
// postgres, and a runner host with its runner.db and one task container,
// where the driver server runs the agent. Flows start from it, set the
// initial state they need, and script the messages.
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
  slots: StateTable;
  runnerdb: StateTable;

  container: Panel;
  driver: Service;
  agent: Service;
  files: StateTable;
  config: StateTable;

  links: {
    clientApi: Link;
    apiPostgres: Link;
    apiPubsub: Link;
    pubsubRunner: Link;
    runnerApi: Link;
    runnerContainer: Link;
    runnerDriver: Link;
    driverApi: Link;
    driverAgent: Link;
    agentApi: Link;
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
  const slots = createRef<StateTable>();
  const runnerdb = createRef<StateTable>();
  const container = createRef<Panel>();
  const driver = createRef<Service>();
  const agent = createRef<Service>();
  const files = createRef<StateTable>();
  const config = createRef<StateTable>();

  view.add(
    <>
      <Panel
        ref={postgres}
        title={'Postgres'}
        accent={Colors.store}
        x={-680}
        y={-170}
        width={440}
        height={540}
      >
        <StateTable
          ref={tasks}
          title={'tasks'}
          accent={Colors.store}
          placeholder={'(no row)'}
          width={390}
          y={-210}
          offset={[0, -1]}
        />
        <StateTable
          ref={events}
          title={'task_events'}
          accent={Colors.store}
          width={390}
          y={10}
          offset={[0, -1]}
        />
      </Panel>

      <Service
        ref={client}
        name={'Client'}
        role={'web UI / CLI / MCP'}
        accent={Colors.client}
        x={-680}
        y={300}
      />

      <Panel
        ref={server}
        title={'gritz server'}
        accent={Colors.server}
        x={-170}
        y={-80}
        width={300}
        height={640}
      >
        <Service
          ref={pubsub}
          name={'PubSub'}
          role={'notifyserver · SSE'}
          accent={Colors.server}
          y={-160}
        />
        <Service
          ref={api}
          name={'API'}
          role={'apiserver · Connect RPC'}
          accent={Colors.server}
          y={150}
        />
      </Panel>

      <Panel
        ref={host}
        title={'runner host'}
        accent={Colors.runner}
        x={530}
        y={-10}
        width={800}
        height={860}
      >
        <Service
          ref={runner}
          name={'Runner'}
          role={'TaskManager · TaskRunner'}
          activity={'idle'}
          accent={Colors.runner}
          width={300}
          x={-200}
          y={-320}
        />
        <StateTable
          ref={slots}
          title={'slots'}
          accent={Colors.runner}
          width={340}
          x={190}
          y={-390}
          offset={[0, -1]}
        />
        <StateTable
          ref={runnerdb}
          title={'runner.db'}
          accent={Colors.runner}
          width={340}
          x={190}
          y={-290}
          offset={[0, -1]}
        />
        <Panel
          ref={container}
          title={'container'}
          accent={Colors.driver}
          y={215}
          width={740}
          height={390}
        >
          <Service
            ref={driver}
            name={'Driver'}
            role={'gritz driver · driver.sock'}
            accent={Colors.driver}
            width={300}
            x={-200}
            y={-55}
          />
          <Service
            ref={agent}
            name={'Agent'}
            role={'claude · codex · …'}
            accent={Colors.driver}
            width={300}
            x={-200}
            y={105}
          />
          <StateTable
            ref={files}
            title={'/gritz/'}
            accent={Colors.driver}
            width={330}
            x={175}
            y={-150}
            offset={[0, -1]}
          />
          <StateTable
            ref={config}
            title={'/tmp/gritz/42.json'}
            accent={Colors.driver}
            width={330}
            x={175}
            y={20}
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
    slots: slots(),
    runnerdb: runnerdb(),
    container: container(),
    driver: driver(),
    agent: agent(),
    files: files(),
    config: config(),
    links: {
      clientApi: link(client(), api(), 'Connect RPC'),
      apiPostgres: link(api(), postgres(), 'SQL'),
      apiPubsub: link(api(), pubsub(), 'publish'),
      pubsubRunner: link(pubsub(), runner(), 'SSE'),
      runnerApi: link(runner(), api(), 'Connect RPC · runner key'),
      runnerContainer: link(runner(), container(), 'docker API', 0.4),
      runnerDriver: link(runner(), driver(), 'driver.v1 Run · exec', 0.75),
      driverApi: link(driver(), api(), 'Connect RPC · task token'),
      driverAgent: link(driver(), agent(), 'exec'),
      agentApi: link(agent(), api(), 'gritz MCP · task token', 0.4),
    },
  };
}
