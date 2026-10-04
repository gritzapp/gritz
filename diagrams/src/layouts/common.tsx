import {Layout, Node, Txt, View2D} from '@motion-canvas/2d';
import {createRef} from '@motion-canvas/core';
import {Caption, Link} from '../components';
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
