import {makeProject} from '@motion-canvas/core';

import dockerDriverServerLaunchLeak from '../../scenes/docker-launch-leak?scene';

export default makeProject({
  scenes: [dockerDriverServerLaunchLeak],
});
