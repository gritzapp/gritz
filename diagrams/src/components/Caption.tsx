import {Circle, Rect, RectProps, Txt} from '@motion-canvas/2d';
import {all, createRef} from '@motion-canvas/core';
import {Colors, Fonts} from '../theme';

// Caption narrates the current step of a flow.
export class Caption extends Rect {
  private readonly badge = createRef<Circle>();
  private readonly number = createRef<Txt>();
  private readonly text = createRef<Txt>();

  public constructor(props: RectProps) {
    super({
      width: 1840,
      height: 84,
      layout: true,
      direction: 'row',
      alignItems: 'center',
      gap: 24,
      padding: [0, 28],
      radius: 14,
      fill: Colors.panel,
      stroke: Colors.faint,
      lineWidth: 1.5,
      ...props,
    });
    this.add(
      <Circle
        ref={this.badge}
        size={48}
        fill={Colors.highlight}
        layout
        alignItems={'center'}
        justifyContent={'center'}
        opacity={0}
      >
        <Txt
          ref={this.number}
          fill={Colors.background}
          fontFamily={Fonts.sans}
          fontWeight={800}
          fontSize={24}
        />
      </Circle>,
    );
    this.add(
      <Txt
        ref={this.text}
        fill={Colors.text}
        fontFamily={Fonts.sans}
        fontSize={26}
      />,
    );
  }

  public *show(step: number, text: string) {
    yield* all(this.text().opacity(0, 0.15), this.badge().scale(0.8, 0.15));
    this.number().text(String(step));
    this.text().text(text);
    yield* all(
      this.text().opacity(1, 0.25),
      this.badge().opacity(1, 0.25),
      this.badge().scale(1, 0.25),
    );
  }
}
