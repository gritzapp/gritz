import {Code, LezerHighlighter, Rect, lines} from '@motion-canvas/2d';
import {all, createRef, easeInOutCubic, map} from '@motion-canvas/core';
import {parser} from '@lezer/go';
import {Colors, Fonts} from '../theme';
import {Panel, PanelProps} from './Panel';

const goHighlighter = new LezerHighlighter(parser);

export interface CodeViewProps extends PanelProps {
  code: string;
  fontSize?: number;
  lineHeight?: number;
}

// CodeView is a panel of Go source with a debugger-style current line: the
// line being executed is lit and marked by a bar, the rest is dimmed. Point
// it at a line with goto(), using lineOf() to find lines by their text.
export class CodeView extends Panel {
  private readonly source: string[];
  private readonly code = createRef<Code>();
  private readonly bar = createRef<Rect>();

  public constructor({code, fontSize = 17, lineHeight, ...props}: CodeViewProps) {
    super({accent: Colors.runner, ...props});
    this.source = code.split('\n');
    this.add(
      <Rect
        ref={this.bar}
        x={0}
        width={() => this.width() - 24}
        radius={6}
        fill={Colors.highlight}
        opacity={0}
      />,
    );
    this.add(
      <Code
        ref={this.code}
        y={24}
        code={code}
        highlighter={goHighlighter}
        fontFamily={Fonts.mono}
        fontSize={fontSize}
        lineHeight={lineHeight ?? fontSize * 1.4}
        selection={lines(0, Infinity)}
        drawHooks={{
          token(ctx, text, position, color, selection) {
            ctx.fillStyle = color;
            ctx.globalAlpha *= map(0.35, 1, selection);
            ctx.fillText(text, position.x, position.y);
          },
        }}
      />,
    );
  }

  // lineOf returns the index of the nth line (0-based) containing needle.
  public lineOf(needle: string, nth = 0) {
    let seen = 0;
    for (let i = 0; i < this.source.length; i++) {
      if (this.source[i].includes(needle) && seen++ === nth) {
        return i;
      }
    }
    throw new Error(`no line ${nth} containing ${JSON.stringify(needle)}`);
  }

  // goto makes line the current line.
  public *goto(line: number, duration = 0.3) {
    const [box] = this.code().getSelectionBBox(lines(line));
    const y = this.code().y() + box.center.y;
    if (this.bar().opacity() === 0) {
      this.bar().y(y);
      this.bar().height(box.height + 6);
    }
    yield* all(
      this.code().selection(lines(line), duration),
      this.bar().y(y, duration, easeInOutCubic),
      this.bar().opacity(0.14, duration),
    );
  }
}
