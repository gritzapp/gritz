import {makeProject} from '@motion-canvas/core';

import watermark from '../../scenes/proposals/replica-watermark?scene';
import start from '../../scenes/proposals/replica-start?scene';
import stopWake from '../../scenes/proposals/replica-stop-wake?scene';

export default makeProject({
  scenes: [watermark, start, stopWake],
});
