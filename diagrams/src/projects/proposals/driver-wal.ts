import {makeProject} from '@motion-canvas/core';

import append from '../../scenes/driver-wal-append?scene';
import replay from '../../scenes/driver-wal-replay?scene';
import crash from '../../scenes/driver-wal-crash?scene';

export default makeProject({
  scenes: [append, replay, crash],
});
