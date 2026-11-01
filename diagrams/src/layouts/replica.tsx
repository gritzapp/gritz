import {View2D} from '@motion-canvas/2d';
import {Vector2, createRef} from '@motion-canvas/core';
import {Link, Panel, Service, StateTable} from '../components';
import {Colors} from '../theme';
import {Scaffold, linker, scaffold} from './common';

// ReplicaLayout is the layout of the runner-event-replica proposal: the
// server's Postgres events log and snapshot, the runner host split into the
// taskevents Log (with its SQLite replica) and the TaskManager / TaskRunner
// side (folded state), and the sandbox's driver with /gritz/events.jsonl and
// its run record. The driver's direct link to the server (SubmitRunnerEvents)
// runs above the panels.
export interface ReplicaLayout extends Scaffold {
  server: Panel;
  api: Service;
  pgEvents: StateTable;
  snapshot: StateTable;

  host: Panel;
  log: Service;
  replica: StateTable;
  manager: Service;
  taskRunner: Service;
  fold: StateTable;

  sandbox: Panel;
  driver: Service;
  eventFile: StateTable;
  run: StateTable;

  links: {
    api: Link;
    tasks: Link;
    slots: Link;
    read: Link;
    driver: Link;
    submit: Link;
  };
}

export function replicaLayout(view: View2D, title: string): ReplicaLayout {
  const frame = scaffold(view, title);
  const server = createRef<Panel>();
  const api = createRef<Service>();
  const pgEvents = createRef<StateTable>();
  const snapshot = createRef<StateTable>();
  const host = createRef<Panel>();
  const log = createRef<Service>();
  const replica = createRef<StateTable>();
  const manager = createRef<Service>();
  const taskRunner = createRef<Service>();
  const fold = createRef<StateTable>();
  const sandbox = createRef<Panel>();
  const driver = createRef<Service>();
  const eventFile = createRef<StateTable>();
  const run = createRef<StateTable>();

  view.add(
    <>
      <Panel
        ref={server}
        title={'gritz server'}
        accent={Colors.server}
        x={-730}
        y={-20}
        width={420}
        height={820}
      >
        <Service
          ref={api}
          name={'API'}
          role={'Connect RPC'}
          accent={Colors.server}
          y={-300}
        />
        <StateTable
          ref={pgEvents}
          title={'events (xid, id)'}
          accent={Colors.store}
          width={380}
          y={-210}
          offset={[0, -1]}
        />
        <StateTable
          ref={snapshot}
          title={'pg_current_snapshot()'}
          accent={Colors.store}
          width={380}
          y={200}
          offset={[0, -1]}
        />
      </Panel>

      <Panel
        ref={host}
        title={'runner host'}
        accent={Colors.runner}
        x={35}
        y={-20}
        width={1050}
        height={820}
      >
        <Service
          ref={log}
          name={'taskevents.Log'}
          role={'replicates the runner’s tasks'}
          activity={'idle'}
          accent={Colors.runner}
          x={-260}
          y={-300}
        />
        <StateTable
          ref={replica}
          title={'runnerstore events (xid, id)'}
          placeholder={'(empty) cursor (0, 0)'}
          accent={Colors.runner}
          width={460}
          x={-260}
          y={-210}
          offset={[0, -1]}
        />
        <Service
          ref={manager}
          name={'TaskManager'}
          role={'TaskRunners · slots'}
          activity={'idle'}
          accent={Colors.runner}
          x={270}
          y={-300}
        />
        <Service
          ref={taskRunner}
          name={'TaskRunner 42'}
          role={'sandbox lifecycle only'}
          activity={''}
          accent={Colors.runner}
          x={270}
          y={-100}
        />
        <StateTable
          ref={fold}
          title={'fold(Stream 42)'}
          placeholder={'(no TaskRunner)'}
          accent={Colors.runner}
          width={400}
          x={270}
          y={-10}
          offset={[0, -1]}
        />
      </Panel>

      <Panel
        ref={sandbox}
        title={'sandbox'}
        accent={Colors.driver}
        x={770}
        y={-20}
        width={340}
        height={820}
      >
        <Service
          ref={driver}
          name={'Driver'}
          role={'owns run behaviour'}
          activity={''}
          accent={Colors.driver}
          y={-300}
        />
        <StateTable
          ref={eventFile}
          title={'/gritz/events.jsonl'}
          accent={Colors.driver}
          width={300}
          y={-210}
          offset={[0, -1]}
        />
        <StateTable
          ref={run}
          title={'/gritz/run.json'}
          placeholder={'(no run)'}
          accent={Colors.driver}
          width={300}
          y={140}
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
    pgEvents: pgEvents(),
    snapshot: snapshot(),
    host: host(),
    log: log(),
    replica: replica(),
    manager: manager(),
    taskRunner: taskRunner(),
    fold: fold(),
    sandbox: sandbox(),
    driver: driver(),
    eventFile: eventFile(),
    run: run(),
    links: {
      api: link(log(), api(), 'gritz.v1'),
      tasks: link(log(), manager(), 'Tasks()'),
      slots: link(manager(), taskRunner(), 'slots'),
      read: link(replica(), taskRunner(), 'Stream.Read', 0.55),
      driver: link(taskRunner(), driver(), 'driver.v1 Run'),
      // Up from the driver, across above the panels, down into the API.
      submit: link(driver(), api(), 'gritz.v1 · task token', 0.18, [
        new Vector2(770, -448),
        new Vector2(-730, -448),
      ]),
    },
  };
}
