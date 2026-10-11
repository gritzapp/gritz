import {makeProject} from '@motion-canvas/core';

import dockerDriverServerStopAfter from '../../scenes/docker-stopafter?scene';

export default makeProject({
  scenes: [dockerDriverServerStopAfter],
});
