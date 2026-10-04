import {makeScene2D} from '@motion-canvas/2d';
import {all, waitFor} from '@motion-canvas/core';
import {microvmLayout} from '../layouts/microvm';
import {Colors} from '../theme';

// First start of a task on the Lambda MicroVM backend, through completion and
// suspend:
//
//   internal/runner/runner.go                          Poll / Start / supervise
//   internal/runner/backend/lambdamicrovm/lambdamicrovm.go   Launch / Wait
//   internal/runner/microvmshim/microvmshim.go         /run, /suspend, SSE
//   internal/runner/agent/driver.go                    Run (started, stopped)
//   proposals/implemented/lambda-microvm-backend.md
export default makeScene2D(function* (view) {
  const a = microvmLayout(view, 'Lambda MicroVM Start');
  const {links} = a;

  a.memory.putRow('sem', '0 / 4');

  // No VM exists until RunMicrovm.
  a.vm.opacity(0);
  a.vm.subtitle('mvm-7c1e');
  links.runnerShim.opacity(0);
  links.lambdaShim.opacity(0);
  links.shimS3.opacity(0);
  links.shimDriver.opacity(0);
  links.driverApi.opacity(0);

  yield* waitFor(0.5);

  yield* a.caption.show(
    1,
    'Task 42 has a pending START command. The runner’s poll returns it.',
  );
  yield* a.runner.updateActivity('Poll()');
  yield* links.runnerApi.send('ListRunnerTasks', {color: Colors.runner});
  yield* links.runnerApi.send('[42: START]', {
    back: true,
    color: Colors.server,
  });

  yield* a.caption.show(
    2,
    'No handle in taskstate, so this is a first start: take a slot and mint the task token.',
  );
  yield* a.runner.updateActivity('Start(42)');
  yield* all(a.memory.setRow('sem', '1 / 4'), a.taskstate.flash());
  yield* links.runnerApi.send('CreateTaskToken', {color: Colors.runner});
  yield* links.runnerApi.send('JWT', {back: true, color: Colors.server});

  yield* a.caption.show(
    3,
    'Stage the spec bundle (driver cmd, env, agent config) in S3. The 16 KB run-hook payload is too small for it.',
  );
  yield* a.runner.updateActivity('launchFresh');
  yield* links.runnerS3.send('PutObject', {color: Colors.runner});
  yield* a.s3.addRow('laptop/42.json', 'bundle');
  yield* links.runnerS3.send('presigned URL', {
    back: true,
    color: Colors.client,
  });

  yield* a.caption.show(
    4,
    'RunMicrovm from the workspace image, with the presigned URL as the run-hook payload.',
  );
  yield* links.runnerLambda.send('RunMicrovm', {color: Colors.runner});
  a.vm.scale(0.92);
  yield* all(
    a.vm.opacity(1, 0.5),
    a.vm.scale(1, 0.5),
    a.guest.addRow('state', 'PENDING'),
  );
  yield* links.runnerLambda.send('{id, endpoint}', {
    back: true,
    color: Colors.client,
  });

  yield* a.caption.show(
    5,
    'Persist the handle to taskstate. Its data carries the endpoint and staging key for later calls.',
  );
  yield* a.taskstate.addRow('42.json', '');
  yield* all(
    a.taskstate.addRow('  type', 'lambda-microvm'),
    a.taskstate.addRow('  id', 'mvm-7c1e'),
    a.taskstate.addRow('  endpoint', 'mvm-7c1e.…'),
  );
  yield* a.runner.updateActivity('supervise: Wait()');

  yield* a.caption.show(
    6,
    'Lambda calls the shim’s /run hook. The shim fetches the bundle and provisions its files once.',
  );
  yield* links.lambdaShim.opacity(1, 0.3);
  yield* links.lambdaShim.send('/run (url)', {color: Colors.client});
  yield* a.shim.updateActivity('fetch bundle');
  yield* links.shimS3.opacity(1, 0.3);
  yield* links.shimS3.rpc('GET', 'bundle', {color: Colors.driver});
  yield* a.shim.updateActivity('provision');
  yield* a.guest.addRow('.provisioned', 'yes');

  yield* a.caption.show(
    7,
    'The shim spawns the driver, supervises it, and returns 200. The VM is RUNNING.',
  );
  yield* links.shimDriver.opacity(1, 0.3);
  yield* links.shimDriver.send('spawn', {color: Colors.driver});
  yield* all(
    a.shim.updateActivity('proc.Wait()'),
    a.driver.updateActivity('booting'),
  );
  yield* links.lambdaShim.send('200', {back: true, color: Colors.driver});
  yield* all(
    a.guest.setRow('state', 'RUNNING'),
    a.vm.subtitle('mvm-7c1e · running', 0.4),
    a.vm.pulse(),
  );

  yield* a.caption.show(
    8,
    'Wait mints a port-scoped proxy token and opens the shim’s lifecycle SSE stream through the proxy.',
  );
  yield* links.runnerLambda.send('CreateMicrovmAuthToken', {
    color: Colors.runner,
  });
  yield* links.runnerLambda.send('token', {back: true, color: Colors.client});
  yield* links.runnerShim.opacity(1, 0.3);
  yield* links.runnerShim.send('GET /gritz/lifecycle', {color: Colors.runner});
  yield* links.runnerShim.send('keep-alive', {
    back: true,
    color: Colors.driver,
  });
  yield* a.runner.updateActivity('Wait: SSE open');

  yield* a.caption.show(
    9,
    'The driver talks to the server directly, not through the shim: GetTask, then it reports started.',
  );
  yield* links.driverApi.opacity(1, 0.3);
  yield* a.driver.updateActivity('GetTask');
  yield* links.driverApi.rpc('GetTask', 'v1', {color: Colors.driver});
  yield* links.driverApi.send('started v1', {color: Colors.driver});

  yield* a.caption.show(
    10,
    'The agent runs. When it finishes, the driver reports stopped and the task is COMPLETED.',
  );
  yield* a.driver.updateActivity('agent running');
  yield* waitFor(1);
  yield* a.driver.updateActivity('report');
  yield* links.driverApi.send('stopped v1', {color: Colors.driver});

  yield* a.caption.show(
    11,
    'The driver exits 0. The shim publishes a sticky driver-exited, replayed if the stream reconnects.',
  );
  yield* a.driver.updateActivity('exit 0');
  yield* links.shimDriver.send('exit 0', {back: true, color: Colors.driver});
  yield* all(
    a.shim.updateActivity('publish'),
    a.guest.addRow('sticky', 'driver-exited{0}'),
  );
  yield* links.runnerShim.send('driver-exited{0}', {
    back: true,
    color: Colors.driver,
  });

  yield* a.caption.show(
    12,
    'The runner suspends the VM: compute stops, the disk is kept. The /suspend hook clears the sticky exit.',
  );
  yield* a.runner.updateActivity('SuspendMicrovm');
  yield* links.runnerLambda.send('SuspendMicrovm', {color: Colors.runner});
  yield* links.lambdaShim.send('/suspend', {color: Colors.client});
  yield* all(a.guest.setRow('sticky', '—'), a.shim.updateActivity('idle'));
  yield* all(
    a.guest.setRow('state', 'SUSPENDED'),
    a.vm.subtitle('mvm-7c1e · suspended', 0.4),
    a.vm.pulse(),
    a.driver.updateActivity(''),
  );

  yield* a.caption.show(
    13,
    'Wait returns exit code 0: the driver already reported, so supervise frees the slot and enqueues nothing.',
  );
  yield* all(
    a.memory.setRow('sem', '0 / 4'),
    a.outbox.flash(),
    a.runner.updateActivity('idle'),
  );

  yield* a.caption.show(
    14,
    'The suspended VM and its record remain. The next run resumes it; archiving the task terminates it.',
  );
  yield* all(a.taskstate.flash(), a.vm.pulse(), a.s3.flash());
  yield* waitFor(2);
});
