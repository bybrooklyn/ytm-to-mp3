# mfetchy design

mfetchy has two faces on one model: the terminal UI and the desktop window. Both draw the
screens in `internal/screen`. This file is about how the desktop window should look and why.
The terminal keeps its look; it is a terminal.

## What "AI slop" design is, and why we design against it

"AI slop" is a community label, not a standard, and the sources are mostly practitioner blogs
whose lists overlap heavily. They agree on a recognisable set of tells (see
[impeccable.style/slop](https://impeccable.style/slop),
[Why your AI keeps building the same purple gradient website](https://prg.sh/ramblings/Why-Your-AI-Keeps-Building-the-Same-Purple-Gradient-Website),
[AI slop fonts and gradients](https://www.925studios.co/blog/ai-slop-design-tells),
[the AI slop catalog](https://cdn.jsdelivr.net/npm/@wystack/agent-kit@0.12.0/wystack-agent-kit/references/anti-patterns.md)).
The shared cause: a model (or a person following a template) with no point of view drifts to
the statistical average of its training data. The tells are symptoms of "nobody decided".

The tells, and what we do instead:

| Tell | Why it reads as slop | mfetchy |
| --- | --- | --- |
| Purple-to-blue gradients, neon on near-black, glowing halos | The palette is a default, not an identity | Flat colour. One accent (the teal-cyan the terminal already uses), never a gradient, no glow |
| Glassmorphism, blur, frosted panels | Decoration with no layering problem to solve | Opaque surfaces only |
| Identical rounded cards in a grid, cards inside cards | Everything gets equal weight; padding eats the content | One grouped list per section, hairline dividers, a single level of container |
| Big radii, shadows plus borders on the same edge | Defines an edge twice, looks inflated | Radius 6 everywhere, borders only, no shadows (except OS popups) |
| Icon-in-a-circle bullets, emoji as icons | Placeholder iconography | No emoji, no icon tiles. Text labels; a status dot where state matters |
| Inter/system default at one size, no hierarchy | Unrelated products look alike | A defined scale (below): page title, section heading, label, help text, mono for code-like values only |
| Centred hero, oversized headline, hero metric | A landing page, not a tool | Left-aligned content, page title at 24px, density suited to a settings tool |
| Uniform spacing everywhere | Related things stop grouping | A spacing scale where the gap between groups is clearly bigger than inside them |
| Fake stats, hype words, "supercharge", forced contrast copy, em-dash in every line | Promise without information | Honest microcopy: say what it does, what it needs, what is missing |
| Pulsing dots, bounce easing | Attention with no signal | No animation beyond what the toolkit does for free |

## What good native desktop software does

References: the [GNOME HIG on boxed lists](https://developer.gnome.org/hig/patterns/containers/boxed-lists.html)
(rows with a title and a subtitle, one control per row, switches for on/off, differentiate text
by size/weight/colour, controls focusable), Apple's and Microsoft's sidebar-plus-content
settings layouts, and the common thread of all three:

- Navigation is a sidebar with text labels and a clear active state.
- A page has a title, then sections with headings, then rows. Rows are label on the left,
  control on the right, explanation underneath in a quieter colour.
- The control matches the data: switch for on/off, dropdown or segmented control for a
  choice, stepper for a number, text field plus Browse for a path, button for an action.
- Keyboard works everywhere and the focus is visible.
- Empty and error states say what happened and what to do, next to where it happened.
- Light and dark follow the system.

## Principles for mfetchy

1. **It is a tool, not a landing page.** Dense enough to see a whole section without
   scrolling, calm enough to read. Left-aligned, content column at most 720px wide.
2. **One accent, used for meaning.** Accent marks the active sidebar item, the keyboard focus,
   a switch that is on, and primary actions. Nothing else is coloured except status
   (green set up, amber caution, red problem), and status always also has words.
3. **Hierarchy by size, weight and colour, not boxes.** Four text levels, no more.
4. **Monospace is for paths and code-like values only.** Everything else is the proportional
   UI font. A folder path and the config file location are mono; "Format: mp3" is not.
5. **Native controls, same behaviour.** A switch can only do what Space does in the terminal.
   The model decides; the window draws. No window-only features.
6. **Honest copy.** "Sign-in happens in the terminal" is a banner next to the thing you
   clicked, stating what to run. No exclamation marks, no emoji, no "oops".
7. **Room to grow.** The sidebar is built from the list of tabs; Library, Downloads and
   Lyrics later are another tab each, not a redesign. (They are not built yet.)

## Palette

Both themes follow the system. Flat colours, defined once in `cmd/mfy-desktop/theme.go`.

| Role | Dark | Light |
| --- | --- | --- |
| Window background | `#121519` | `#f4f5f6` |
| Sidebar | `#0e1013` | `#e9ebed` |
| Surface (section list) | `#191d22` | `#ffffff` |
| Hover | `#20252b` | `#f0f2f3` |
| Border / divider | `#2a3038` | `#d5d9dd` |
| Text | `#e8eaed` | `#1a1d21` |
| Muted text | `#8d96a1` | `#5b646e` |
| Accent | `#5cc8d8` | `#0b7285` |
| Text on accent | `#06242a` | `#ffffff` |
| Success | `#62cf83` | `#17794a` |
| Caution | `#e2b04a` | `#8f5d00` |
| Problem | `#f2796d` | `#b3261e` |

Muted text meets 4.5:1 against its surface in both themes; the light accent against white is
about 5.5:1.

## Type scale

The UI font is the toolkit's proportional face (Noto Sans); we do not ship a decorative font.

| Use | Size | Weight | Colour |
| --- | --- | --- | --- |
| Page title | 24 | bold | text |
| Section heading | 13 | bold | muted |
| Row label, sidebar item, button | 14 | regular (label bold when it is the row's name) | text |
| Help text, status, notices | 12-13 | regular | muted |
| Paths, config location | 13 | mono | text / muted |

## Spacing scale

4, 8, 12, 16, 24, 32. Inside a row: 12 vertical, 16 horizontal, 4 between label and help.
Between rows: a 1px divider. Between a section heading and its list: 8. Between sections: 24.
Page margin: 32 (16 when the window is narrow). Sidebar: 200 wide.

## Component rules

- **Sidebar**: wordmark "mfetchy" at the top (text, no logo tile), then one item per tab. Active
  item: accent text, 3px accent bar at its left edge, hover-tint background. No emoji, no icons.
- **Section**: heading (muted, 13 bold) above one bordered, radius-6 surface. Rows separated by
  hairlines. A section starts where the model sets `Row.Group`.
- **Row**: label and help on the left, one control on the right. The row the keyboard is on
  gets a 2px accent outline (the focus ring); it is the same cursor the terminal shows.
- **Switch** for on/off. **Segmented control** for 2-5 choices, **dropdown** for more.
  **Stepper** (minus, value, plus) for numbers, with words like "Auto" rather than 0.
  **Text field with Browse** for the music folder. **Button** for actions: outlined, neutral,
  never filled with the accent (the accent stays reserved for state and focus).
- **Disabled** rows (the model's `Dim`) are dimmed and say why in their help or status.
- **Status**: a 8px dot plus words ("Signed in as ...", "Not signed in"). Never colour alone.
- **Notice**: an inline message at the top of the content, surface background, 3px caution
  bar on its left, a bold lead line and one sentence. Replaced by the next action.
- **Errors**: red text under the row they belong to.
- **Keys**: arrows, Tab, vi keys, Space and Enter work as in the terminal. Click targets are
  the whole control.

## Checklist before shipping a screen change

Look at light and dark, wide and narrow screenshots (`MFY_SHOT_DIR=/tmp/mfetchy go test
./cmd/mfy-desktop -run Screenshot`). Then ask: is there a gradient, glow, emoji, card grid,
centred block, or a sentence that says nothing? If yes, remove it.
