import {makeProject} from '@motion-canvas/core';

import createTask from './scenes/create-task?scene';

export default makeProject({
  scenes: [createTask],
});
