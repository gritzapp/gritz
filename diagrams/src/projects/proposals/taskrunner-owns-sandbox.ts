import {makeProject} from '@motion-canvas/core';

import start from '../../scenes/proposals/taskrunner-start?scene';
import gone from '../../scenes/proposals/taskrunner-gone?scene';
import restore from '../../scenes/proposals/taskrunner-restore?scene';
import prune from '../../scenes/proposals/taskrunner-prune?scene';

export default makeProject({
  scenes: [start, gone, restore, prune],
});
