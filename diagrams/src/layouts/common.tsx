import {Layout, Node, Txt, View2D} from '@motion-canvas/2d';
import {all, beginSlide, createRef, waitFor} from '@motion-canvas/core';
import {Caption, CodeView, Link} from '../components';
import {Colors, Fonts} from '../theme';

// Scaffold is the frame every layout shares: background, title, and caption.
export interface Scaffold {
  title: Txt;
  caption: Caption;
}

export function scaffold(view: View2D, title: string): Scaffold {
  view.fill(Colors.background);
  const t = createRef<Txt>();
  const caption = createRef<Caption>();
  view.add(
    <>
      <Txt
        ref={t}
        text={title}
        x={-900}
        y={-488}
        offset={[-1, 0]}
        fill={Colors.text}
        fontFamily={Fonts.sans}
        fontWeight={800}
        fontSize={40}
      />
      <Caption ref={caption} y={478} />
    </>,
  );
  return {title: t(), caption: caption()};
}

// linker adds a layer on top of everything already in the view and returns
// a function that creates links in it, so links are never hidden behind a
// panel fill. Call it after the components have been added.
export function linker(view: View2D) {
  const layer = createRef<Node>();
  view.add(<Node ref={layer} />);
  return (from: Layout, to: Layout, label: string, labelAt?: number) => {
    const link = new Link({from, to, label, labelAt});
    layer().add(link);
    return link;
  };
}

// stepper returns a function that steps a CodeView like a debugger: each call
// is a slide (Space or → advances it in presentation mode) that moves the
// current line to the first line containing needle (or its nth match) and
// shows the next numbered caption. Animate the line's effects after it.
export function stepper(source: CodeView, caption: Caption) {
  let n = 0;
  return function* step(needle: string, text: string, nth?: number) {
    yield* waitFor(0.6);
    n++;
    yield* beginSlide(`step ${n}`);
    yield* all(source.goto(source.lineOf(needle, nth)), caption.show(n, text));
  };
}
