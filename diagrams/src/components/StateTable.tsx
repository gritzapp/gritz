import {
  Layout,
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
  createRef,
} from '@motion-canvas/core';
import {Colors, Fonts} from '../theme';

export interface StateTableProps extends RectProps {
  title?: SignalValue<string>;
  accent?: SignalValue<PossibleColor>;
  // Text shown while the table has no rows.
  placeholder?: string;
  entries?: [string, string][];
}

interface Row {
  rect: Rect;
  value: Txt;
}

// StateTable is a key/value view of some piece of state: a database row, a
// file on disk, an in-memory counter. Changes flash so the eye follows them.
// Anchor it with offset={[0, -1]} so it grows downward as rows are added.
export class StateTable extends Rect {
  @initial('')
  @signal()
  public declare readonly title: SimpleSignal<string, this>;

  @initial(Colors.dim)
  @colorSignal()
  public declare readonly accent: ColorSignal<this>;

  private readonly body = createRef<Layout>();
  private readonly rows = new Map<string, Row>();
  private placeholder: Txt | null = null;

  public constructor({placeholder, entries, ...props}: StateTableProps) {
    super({
      width: 300,
      layout: true,
      direction: 'column',
      padding: [12, 12],
      gap: 8,
      radius: 10,
      fill: Colors.table,
      lineWidth: 1.5,
      ...props,
    });
    this.stroke(() => this.accent().alpha(0.5));
    this.add(
      <Txt
        text={() => this.title()}
        fill={() => this.accent()}
        fontFamily={Fonts.mono}
        fontWeight={700}
        fontSize={19}
        marginLeft={8}
      />,
    );
    this.add(<Rect height={1.5} width={'100%'} fill={Colors.faint} />);
    this.add(<Layout ref={this.body} direction={'column'} gap={2} width={'100%'} />);
    this.placeholder = (
      <Txt
        text={placeholder ?? '(empty)'}
        fill={Colors.dim}
        fontFamily={Fonts.mono}
        fontStyle={'italic'}
        fontSize={17}
        padding={[4, 8]}
      />
    ) as Txt;
    this.body().add(this.placeholder);
    for (const [key, value] of entries ?? []) {
      this.putRow(key, value);
    }
  }

  // putRow adds or replaces a row without animating. Use it to set up the
  // initial state of a scene.
  public putRow(key: string, value: string) {
    const existing = this.rows.get(key);
    if (existing) {
      existing.value.text(value);
      return existing;
    }
    const value_ = createRef<Txt>();
    const rect = (
      <Rect
        direction={'row'}
        justifyContent={'space-between'}
        alignItems={'center'}
        gap={16}
        width={'100%'}
        padding={[4, 8]}
        radius={6}
      >
        <Txt
          text={key}
          fill={Colors.dim}
          fontFamily={Fonts.mono}
          fontSize={17}
        />
        <Txt
          ref={value_}
          text={value}
          fill={Colors.text}
          fontFamily={Fonts.mono}
          fontSize={17}
        />
      </Rect>
    ) as Rect;
    this.placeholder?.remove();
    this.placeholder = null;
    this.body().add(rect);
    const row = {rect, value: value_()};
    this.rows.set(key, row);
    return row;
  }

  // addRow appends a row and fades it in.
  public *addRow(key: string, value: string) {
    const row = this.putRow(key, value);
    row.rect.opacity(0);
    yield* all(row.rect.opacity(1, 0.4), this.flash(key));
  }

  // setRow changes a row's value, adding the row if it does not exist.
  public *setRow(key: string, value: string, duration = 0.5) {
    const row = this.rows.get(key);
    if (!row) {
      yield* this.addRow(key, value);
      return;
    }
    yield* all(row.value.text(value, duration), this.flash(key));
  }

  // flash highlights a row, or the whole table when no key is given.
  public *flash(key?: string, color: PossibleColor = Colors.highlight) {
    const target = key === undefined ? this : this.rows.get(key)?.rect;
    if (!target) {
      return;
    }
    const original = target.fill();
    target.fill(new Color(color).alpha(0.3));
    yield* target.fill(original, 0.9);
  }
}
