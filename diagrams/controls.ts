// Playback controls for the published diagram pages. @motion-canvas/player
// only offers click-to-toggle, so this drives its core Player directly:
// play/pause, restart, frame stepping, a scrubber marked with the scene
// boundaries, speed and loop. It relies on the element's `player` property
// and `setPlaying` method, which are not documented API, so the player
// version is pinned in package.json.

import '@motion-canvas/player';
import type {Player, Scene} from '@motion-canvas/core';

// The parts of the player element used here.
interface PlayerElement extends HTMLElement {
  player: Player | null;
  state: string;
  setPlaying(playing: boolean): void;
}

const SPEEDS = [0.25, 0.5, 1, 1.5, 2];

const style = `
  .mc-controls {
    display: flex; flex-wrap: wrap; align-items: center; gap: 8px;
    margin-top: 8px; font: 14px/1 system-ui, sans-serif; color: #e6e8eb;
  }
  .mc-controls button, .mc-controls select {
    background: #1b1f26; color: inherit; border: 1px solid #2c323c;
    border-radius: 4px; padding: 6px 10px; font: inherit; cursor: pointer;
  }
  .mc-controls button:hover, .mc-controls select:hover { background: #242a33; }
  .mc-controls button[aria-pressed="true"] { border-color: #5aa9e6; color: #5aa9e6; }
  .mc-controls .mc-play { min-width: 64px; }
  .mc-controls .mc-time { font-variant-numeric: tabular-nums; color: #8b93a1; min-width: 104px; }
  .mc-track { position: relative; flex: 1 1 100%; order: -1; height: 20px; }
  .mc-track input { position: absolute; inset: 0; width: 100%; margin: 0; accent-color: #5aa9e6; }
  .mc-tick {
    position: absolute; top: 0; bottom: 0; width: 2px; margin-left: -1px;
    background: #8b93a1; pointer-events: none; opacity: .6;
  }
  .mc-scenes { display: flex; flex-wrap: wrap; gap: 4px; flex-basis: 100%; }
  .mc-scenes button { padding: 4px 8px; font-size: 13px; color: #8b93a1; }
  .mc-scenes button.mc-current { color: #e6e8eb; border-color: #5aa9e6; }
  .mc-hint { color: #8b93a1; font-size: 13px; margin-left: auto; }
`;

const el = <K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props: Record<string, unknown> = {},
  ...children: (Node | string)[]
): HTMLElementTagNameMap[K] => {
  const node = Object.assign(document.createElement(tag), props);
  node.append(...children);
  return node;
};

const formatTime = (seconds: number) => {
  const m = Math.floor(seconds / 60);
  const s = (seconds % 60).toFixed(1).padStart(4, '0');
  return `${m}:${s}`;
};

// Resolves once the element has loaded its project and created its Player.
const ready = (host: PlayerElement) =>
  new Promise<Player>(resolve => {
    const check = () =>
      host.player && host.state === 'ready'
        ? resolve(host.player)
        : requestAnimationFrame(check);
    check();
  });

function mount(host: PlayerElement, player: Player) {
  const play = el('button', {className: 'mc-play', title: 'Play/pause (space)'});
  const restart = el('button', {textContent: 'Restart', title: 'Restart (home)'});
  const prev = el('button', {textContent: '<', title: 'Previous frame (left)'});
  const next = el('button', {textContent: '>', title: 'Next frame (right)'});
  const time = el('span', {className: 'mc-time'});
  const loop = el('button', {textContent: 'Loop', title: 'Loop (l)'});
  const speed = el(
    'select',
    {title: 'Speed'},
    ...SPEEDS.map(s => el('option', {value: String(s), textContent: `${s}x`})),
  );
  const range = el('input', {type: 'range', min: '0', step: '1'});
  const track = el('div', {className: 'mc-track'}, range);
  const scenes = el('div', {className: 'mc-scenes'});
  const hint = el('span', {
    className: 'mc-hint',
    textContent: 'space play · arrows step · shift+arrows scene',
  });

  const controls = el(
    'div',
    {className: 'mc-controls'},
    track,
    play,
    restart,
    prev,
    next,
    time,
    speed,
    loop,
    hint,
    scenes,
  );
  host.after(controls);

  const fps = () => player.status.fps;
  let sceneList: Scene[] = [];
  let duration = 0;
  let scrubbing = false;
  let resume = false;

  const currentScene = (frame: number) =>
    sceneList.findLast(s => s.firstFrame <= frame) ?? sceneList[0];

  const renderTime = (frame: number) => {
    time.textContent = `${formatTime(frame / fps())} / ${formatTime(duration / fps())}`;
    if (!scrubbing) range.value = String(frame);
    const current = currentScene(frame);
    for (const button of scenes.children) {
      button.classList.toggle('mc-current', button.textContent === current?.name);
    }
  };

  const renderScenes = () => {
    track.querySelectorAll('.mc-tick').forEach(t => t.remove());
    scenes.replaceChildren(
      ...sceneList.map(scene =>
        el('button', {
          textContent: scene.name,
          title: `Jump to ${scene.name}`,
          onclick: () => player.requestSeek(scene.firstFrame),
        }),
      ),
    );
    for (const scene of sceneList.slice(1)) {
      const tick = el('div', {className: 'mc-tick'});
      tick.style.left = `${(scene.firstFrame / Math.max(duration, 1)) * 100}%`;
      track.append(tick);
    }
  };

  player.onDurationChanged.subscribe(value => {
    duration = value;
    range.max = String(value);
    renderScenes();
  });
  player.playback.onScenesRecalculated.subscribe(value => {
    sceneList = value;
    renderScenes();
  });
  player.onFrameChanged.subscribe(renderTime);
  player.onStateChanged.subscribe(state => {
    play.textContent = state.paused ? 'Play' : 'Pause';
    loop.setAttribute('aria-pressed', String(state.loop));
    speed.value = String(state.speed);
  });

  // Play/pause goes through the element so its overlay stays in sync.
  const togglePlaying = () =>
    host.setPlaying(player.onStateChanged.current.paused);
  const stepScene = (dir: number) => {
    const frame = player.status.frame;
    const index = sceneList.indexOf(currentScene(frame));
    const target =
      dir < 0 && frame > sceneList[index].firstFrame
        ? sceneList[index]
        : sceneList[index + dir];
    if (target) player.requestSeek(target.firstFrame);
  };

  play.onclick = togglePlaying;
  restart.onclick = () => player.requestReset();
  prev.onclick = () => player.requestPreviousFrame();
  next.onclick = () => player.requestNextFrame();
  loop.onclick = () => player.toggleLoop();
  speed.onchange = () => player.setSpeed(Number(speed.value));

  // Pause while dragging the scrubber and pick playback back up after.
  range.onpointerdown = () => {
    scrubbing = true;
    resume = !player.onStateChanged.current.paused;
    host.setPlaying(false);
  };
  range.oninput = () => player.requestSeek(Number(range.value));
  range.onchange = () => {
    scrubbing = false;
    if (resume) host.setPlaying(true);
  };

  document.addEventListener('keydown', event => {
    const target = event.target as HTMLElement;
    if (target.matches('input, select, textarea')) return;
    switch (event.key) {
      case ' ':
        togglePlaying();
        break;
      case 'ArrowLeft':
        if (event.shiftKey) stepScene(-1);
        else player.requestPreviousFrame();
        break;
      case 'ArrowRight':
        if (event.shiftKey) stepScene(1);
        else player.requestNextFrame();
        break;
      case 'Home':
        player.requestReset();
        break;
      case 'l':
        player.toggleLoop();
        break;
      default:
        return;
    }
    event.preventDefault();
  });
}

document.head.append(el('style', {textContent: style}));

for (const host of document.querySelectorAll<PlayerElement>('motion-canvas-player')) {
  // The player resolves `src` against its own module URL, so hand it an
  // absolute URL built from the page's location.
  host.setAttribute('src', new URL(host.dataset.src!, location.href).href);
  ready(host).then(player => {
    mount(host, player);
    host.setPlaying(true);
  });
}
