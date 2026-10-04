# Themes

Themes are decided on the server so that every device of a household looks the same. The common
answer, "custom CSS", does not fit:

- it is **web only**: Android, iOS and TV clients draw natively and read no CSS;
- it is **fragile**: it breaks with every interface update;
- it is **unverifiable**: nothing guarantees a theme stays readable.

So a theme is a set of **design tokens**, never CSS, not even a web-only field. Administrators
create themes and each profile picks among them.

## What a theme is

`domain.ThemeTokens`:

- **Two palettes**, dark and light, giving an `#rrggbb` color to each role:
  - backgrounds: `background`, `surface`, `surface_raised`;
  - text: `text`, `text_muted`;
  - accent: `accent` and `on_accent` (text placed on it);
  - `outline` (decorative);
  - states: `error` and `on_error`, `success`, `warning`.
- **Shape**: corner radius (0 to 24 points) and density (compact, comfortable, spacious).
- **Font** from a fixed list (system, Inter, Atkinson Hyperlegible, Lexend, serif): each client
  ships them or falls back to the system font.
- An optional **logo** and **background image**.

Each client maps the tokens to its own toolkit: CSS variables, Compose, SwiftUI.

## Readability is enforced

A theme is refused if a contrast is too low (WCAG 2.2 AA, relative luminance computed in
`domain`):

- 4.5:1 for `text` and `text_muted` on `background` and `surface`, for `text` on
  `surface_raised`, for `on_accent` on `accent` and `on_error` on `error`;
- 3:1 for `accent`, `error`, `success` and `warning` on `background` (interface components).

The refusal lists each problem with its value, as causes of the `theme.rejected` error. Short
colors (`#abc`) are accepted and normalized.

## Built-in and custom themes

- **Four built-in themes**: Laterna (amber accent, the default), Ocean, Forest and High
  contrast. They are written at startup with fixed IDs, so a new version updates them. They
  cannot be edited or deleted; make a copy instead. A test runs them through the same checks as
  any other theme.
- **Custom themes** (`ThemeService`, administrators): create, edit, delete, set a logo and a
  background (JPEG, PNG or WebP, 8 MiB at most). 100 themes at most.
- **Export and import** as a single JSON file (`laterna-theme`, with tokens and images in
  base64) to move a theme between servers. An import is checked like a creation; a name already
  taken gets a number.

The logo and the background are images like any other: stored under the metadata directory, served
by the image route with reduced versions, a BlurHash and an immutable cache, and removed with
their theme.

## Choosing

- **Server theme**: the `theme.default` setting, Laterna otherwise. It can be read without
  signing in (`GetServerTheme`), to dress the sign-in screen.
- **Profile theme** and mode (auto, dark, light). Any profile chooses, even a restricted one,
  since it chooses among the server's themes. Without a choice, the profile follows the server
  theme. A deleted theme sends its profiles back to the server theme.
- The **`ThemesChanged` event** tells clients to reload `GetMyTheme`: sent to everyone when a
  theme or the server theme changes, and to the one profile concerned when it changes its choice
  from another device.

## Consequences

- One theme works on web, mobile and TV, and every accepted theme is readable.
- Clients must map every role and ship the five fonts. A role added later will need a default
  value for existing themes.
- A parent cannot yet set the theme of a child's profile.
