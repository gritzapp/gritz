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
  all,
} from '@motion-canvas/core';
import {Colors, Fonts} from '../theme';

export interface ServiceProps extends RectProps {
  name?: SignalValue<string>;
  role?: SignalValue<string>;
  activity?: SignalValue<string>;
  accent?: SignalValue<PossibleColor>;
}

// Service is an active component: something that runs code and sends
// messages. The activity line shows what it is doing right now.
export class Service extends Rect {
  @initial('')
  @signal()
  public declare readonly name: SimpleSignal<string, this>;

  @initial('')
  @signal()
  public declare readonly role: SimpleSignal<string, this>;

  @initial('')
  @signal()
  public declare readonly activity: SimpleSignal<string, this>;

  @initial(Colors.dim)
  @colorSignal()
  public declare readonly accent: ColorSignal<this>;

  @initial(0)
  @signal()
  public declare readonly glow: SimpleSignal<number, this>;

  public constructor(props: ServiceProps) {
    super({
      width: 260,
      height: 120,
      radius: 12,
      lineWidth: 2,
      layout: true,
      direction: 'column',
      alignItems: 'center',
      justifyContent: 'center',
      gap: 4,
      ...props,
    });
    this.fill(() =>
      Color.lerp(Colors.table, this.accent(), 0.12 + this.glow() * 0.15),
    );
    this.stroke(() =>
      Color.lerp(this.accent().alpha(0.6), this.accent(), this.glow()),
    );
    this.shadowColor(() => this.accent());
    this.shadowBlur(() => this.glow() * 30);
    this.add(
      <Txt
        text={() => this.name()}
        fill={Colors.text}
        fontFamily={Fonts.sans}
        fontWeight={700}
        fontSize={28}
      />,
    );
    this.add(
      <Txt
        text={() => this.role()}
        fill={Colors.dim}
        fontFamily={Fonts.sans}
        fontSize={16}
      />,
    );
    this.add(
      <Txt
        text={() => this.activity()}
        fill={() => this.accent()}
        fontFamily={Fonts.mono}
        fontSize={17}
      />,
    );
  }

  public *pulse(duration = 0.6) {
    yield* this.glow(1, 0.15).to(0, duration);
  }

  // updateActivity changes the activity line and pulses the service. It must
  // not be named setActivity: Motion Canvas treats set<Signal> as the signal's
  // custom setter.
  public *updateActivity(text: string, duration = 0.4) {
    yield* all(this.activity(text, duration), this.pulse());
  }
}
