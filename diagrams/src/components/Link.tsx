import {
  Layout,
  Line,
  LineProps,
  Rect,
  Txt,
  colorSignal,
  initial,
  signal,
} from '@motion-canvas/2d';
import {
  Color,
  ColorSignal,
  PossibleColor,
  SimpleSignal,
  Vector2,
  all,
  createSignal,
  easeInOutCubic,
} from '@motion-canvas/core';
import {Colors, Fonts} from '../theme';

export interface LinkProps extends Omit<LineProps, 'points'> {
  from: Layout;
  to: Layout;
  // Protocol or channel name drawn at the midpoint, e.g. "Connect RPC".
  label?: string;
  // Gap between the box edge and the line end.
  gap?: number;
}

export interface SendOptions {
  // Travel from `to` back to `from`.
  back?: boolean;
  color?: PossibleColor;
  duration?: number;
}

// Link is a standing connection between two components. It stays dim until
// a message travels along it. Endpoints track the components, so moving or
// resizing them re-routes the link.
export class Link extends Line {
  @initial(0)
  @signal()
  public declare readonly glow: SimpleSignal<number, this>;

  @initial(Colors.text)
  @colorSignal()
  public declare readonly activeColor: ColorSignal<this>;

  public constructor({from, to, label, gap = 8, ...props}: LinkProps) {
    super({
      lineWidth: 2,
      lineDash: [8, 8],
      ...props,
      points: [],
    });
    this.stroke(() =>
      Color.lerp(Colors.link, this.activeColor(), this.glow()),
    );
    this.points(() => {
      const a = this.boxOf(from);
      const b = this.boxOf(to);
      return [clip(a, b.center, gap), clip(b, a.center, gap)];
    });
    if (label) {
      this.add(
        <Txt
          text={label}
          position={() => this.labelPosition()}
          fill={Colors.dim}
          fontFamily={Fonts.mono}
          fontSize={14}
        />,
      );
    }
  }

  // send moves a labelled packet along the link.
  public *send(text: string, {back, color, duration = 1}: SendOptions = {}) {
    const fill = new Color(color ?? Colors.text);
    const progress = createSignal(0);
    const packet = (
      <Rect
        layout
        padding={[6, 14]}
        radius={20}
        fill={fill}
        scale={0}
        position={() =>
          this.getPointAtPercentage(back ? 1 - progress() : progress())
            .position
        }
      >
        <Txt
          text={text}
          fill={Colors.background}
          fontFamily={Fonts.mono}
          fontWeight={700}
          fontSize={18}
        />
      </Rect>
    ) as Rect;
    this.add(packet);
    this.activeColor(fill);
    yield* all(this.glow(1, 0.2), packet.scale(1, 0.2));
    yield* progress(1, duration, easeInOutCubic);
    yield* all(packet.scale(0, 0.2), this.glow(0, 0.4));
    packet.remove();
  }

  // rpc sends a request and its response.
  public *rpc(
    request: string,
    response: string,
    {back, color, duration}: SendOptions = {},
  ) {
    yield* this.send(request, {back, color, duration});
    yield* this.send(response, {back: !back, color, duration});
  }

  private labelPosition() {
    const [a, b] = this.parsedPoints();
    if (!a || !b) {
      return Vector2.zero;
    }
    const normal = b.sub(a).normalized.perpendicular;
    // Keep the label above the line regardless of direction.
    const side = normal.y > 0 ? -1 : 1;
    return a.add(b).scale(0.5).add(normal.scale(16 * side));
  }

  // boxOf returns the node's center and half extents in this line's space.
  private boxOf(node: Layout) {
    const size = node.size();
    const offset = node.offset();
    const center = new Vector2(
      (-offset.x * size.x) / 2,
      (-offset.y * size.y) / 2,
    )
      .transformAsPoint(node.localToWorld())
      .transformAsPoint(this.worldToLocal());
    return {center, half: size.scale(0.5)};
  }
}

// clip returns the point where the ray from the box center toward `target`
// leaves the box, pushed out by `gap`.
function clip(
  box: {center: Vector2; half: Vector2},
  target: Vector2,
  gap: number,
) {
  const d = target.sub(box.center);
  const tx = d.x === 0 ? Infinity : box.half.x / Math.abs(d.x);
  const ty = d.y === 0 ? Infinity : box.half.y / Math.abs(d.y);
  const t = Math.min(tx, ty);
  return box.center.add(d.scale(t)).add(d.normalized.scale(gap));
}
