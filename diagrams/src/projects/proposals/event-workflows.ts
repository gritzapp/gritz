import {makeProject} from '@motion-canvas/core';

import trigger from '../../scenes/proposals/workflows-trigger?scene';
import emit from '../../scenes/proposals/workflows-emit?scene';
import claim from '../../scenes/proposals/workflows-claim?scene';

export default makeProject({
  scenes: [trigger, emit, claim],
});
