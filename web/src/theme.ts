// Dark/light theme: toggles the `.dark` class the design tokens key off, and
// persists the choice. Default is dark.
const KEY = 'outpost-theme';

export function initTheme(): void {
  const stored = localStorage.getItem(KEY);
  applyDark(stored ? stored === 'dark' : true);
}

export function isDark(): boolean {
  return document.documentElement.classList.contains('dark');
}

export function applyDark(dark: boolean): void {
  document.documentElement.classList.toggle('dark', dark);
  localStorage.setItem(KEY, dark ? 'dark' : 'light');
}
