import {
  Rect,
  RectProps,
  Txt,
  colorSignal,
  initial,
  signal,
} from '@motion-canvas/2d';
import {
  Color,
  ColorSignal,
  PossibleColor,
  SignalValue,
  SimpleSignal,
} from '@motion-canvas/core';
import {Colors, Fonts} from '../theme';

export interface PanelProps extends RectProps {
  title?: SignalValue<string>;
  subtitle?: SignalValue<string>;
  accent?: SignalValue<PossibleColor>;
}

// Panel is a titled region that groups components: a host, a process
// boundary, a container. Children are positioned relative to its center.
export class Panel extends Rect {
  @initial('')
  @signal()
  public declare readonly title: SimpleSignal<string, this>;

  @initial('')
  @signal()
  public declare readonly subtitle: SimpleSignal<string, this>;

  @initial(Colors.dim)
  @colorSignal()
  public declare readonly accent: ColorSignal<this>;

  @initial(0)
  @signal()
  public declare readonly glow: SimpleSignal<number, this>;

  public constructor(props: PanelProps) {
    super({
      radius: 16,
      fill: Colors.panel,
      lineWidth: 2,
      ...props,
    });
    this.stroke(() =>
      Color.lerp(this.accent().alpha(0.35), this.accent(), this.glow()),
    );
    this.shadowColor(() => this.accent());
    this.shadowBlur(() => this.glow() * 30);
    this.add(
      <Txt
        text={() => this.title()}
        x={() => -this.width() / 2 + 24}
        y={() => -this.height() / 2 + 30}
        offset={[-1, 0]}
        fill={() => this.accent()}
        fontFamily={Fonts.sans}
        fontWeight={700}
        fontSize={22}
      />,
    );
    this.add(
      <Txt
        text={() => this.subtitle()}
        x={() => this.width() / 2 - 24}
        y={() => -this.height() / 2 + 30}
        offset={[1, 0]}
        fill={Colors.dim}
        fontFamily={Fonts.mono}
        fontSize={17}
      />,
    );
  }

  public *pulse(duration = 0.6) {
    yield* this.glow(1, 0.15).to(0, duration);
  }
}
