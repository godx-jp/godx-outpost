/**
 * Central theme. All colour lives here — screens reference these tokens via
 * `useTheme<AppTheme>()` (Paper) and the navigator picks up `navTheme`.
 *
 * Rule for this app: components come from react-native-paper, colours come from
 * this theme, and StyleSheet is used only for layout (flex / spacing / size) —
 * never for colour.
 */
import { DarkTheme as NavDarkTheme } from '@react-navigation/native';
import { MD3DarkTheme } from 'react-native-paper';

const palette = {
  bg:         '#0d0d0d',
  surface:    '#141414',
  surfaceAlt: '#1b1b1b',
  text:       '#e6e6e6',
  dim:        '#9aa0a6',
  accent:     '#4fc3f7',
  danger:     '#ef5350',
  ok:         '#66bb6a',
  warn:       '#ffb74d',
  border:     '#262626',
};

export const theme = {
  ...MD3DarkTheme,
  // Paper multiplies this: Card = 3×, Button = 5×, Dialog = 7×. Keep it small
  // so cards/buttons have a crisp, subtle radius rather than pill-like corners.
  roundness: 2,
  colors: {
    ...MD3DarkTheme.colors,
    primary:            palette.accent,
    onPrimary:          '#04121a',
    primaryContainer:   '#0a3a4a',
    onPrimaryContainer: '#cdeefb',
    secondary:          palette.ok,
    onSecondary:        '#04140a',
    background:         palette.bg,
    onBackground:       palette.text,
    surface:            palette.surface,
    onSurface:          palette.text,
    surfaceVariant:     palette.surfaceAlt,
    onSurfaceVariant:   palette.dim,
    outline:            palette.border,
    outlineVariant:     '#202020',
    error:              palette.danger,
    onError:            '#1c0000',
    // Custom status tokens (accessed through useTheme<AppTheme>()).
    usageLow:           palette.ok,
    usageMid:           palette.warn,
    usageHigh:          palette.danger,
    elevation: {
      level0: 'transparent',
      level1: palette.surface,
      level2: '#171717',
      level3: '#1b1b1b',
      level4: '#1e1e1e',
      level5: '#222222',
    },
  },
};

export type AppTheme = typeof theme;

export const navTheme = {
  ...NavDarkTheme,
  colors: {
    ...NavDarkTheme.colors,
    background:   palette.bg,
    card:         palette.surface,
    text:         palette.text,
    border:       palette.border,
    primary:      palette.accent,
    notification: palette.danger,
  },
};

/** Green under light load, amber as it climbs, red when saturated. pct is 0–100. */
export function usageColor(colors: AppTheme['colors'], pct: number): string {
  if (pct >= 85) return colors.usageHigh;
  if (pct >= 60) return colors.usageMid;
  return colors.usageLow;
}
