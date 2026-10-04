import {makeProject} from '@motion-canvas/core';

import createTask from './scenes/create-task?scene';
import microvmStart from './scenes/microvm-start?scene';

export default makeProject({
  scenes: [createTask, microvmStart],
});
