import {makeProject} from '@motion-canvas/core';

import start from '../../scenes/proposals/fake-start?scene';
import gone from '../../scenes/proposals/fake-gone?scene';
import restart from '../../scenes/proposals/fake-restart?scene';

export default makeProject({
  scenes: [start, gone, restart],
});
