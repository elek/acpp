// Headless Chromium aborts the renderer on any page that selects a font-family
// when the host has no fontconfig setup (empty /etc/fonts, `fc-list` empty) —
// the error is "Fontconfig error: Cannot load default config file" followed by a
// Skia font-manager crash, which closes the page mid-navigation.
//
// ensureFonts makes the suite self-sufficient on such hosts: it discovers any
// font files on disk, writes a private fontconfig that points at them, and
// exports FONTCONFIG_FILE so both the test runner and the browser it launches
// pick it up. On a normal host/CI (fonts already visible to fontconfig) it does
// nothing.
import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { E2E_DIR } from './env';

const FC_DIR = path.join(E2E_DIR, '.fontconfig');
const FC_FILE = path.join(FC_DIR, 'fonts.conf');
const FC_CACHE = path.join(FC_DIR, 'cache');

function systemHasFonts(): boolean {
  try {
    return execFileSync('sh', ['-c', 'fc-list 2>/dev/null | head -1']).toString().trim().length > 0;
  } catch {
    return false;
  }
}

function findFontDirs(): string[] {
  const roots = [
    '/usr/share/fonts',
    '/usr/local/share/fonts',
    path.join(os.homedir(), '.fonts'),
    path.join(os.homedir(), '.local/share/fonts'),
    '/usr/lib',
  ].filter((r) => {
    try {
      return fs.statSync(r).isDirectory();
    } catch {
      return false;
    }
  });
  if (roots.length === 0) return [];
  try {
    const quoted = roots.map((r) => JSON.stringify(r)).join(' ');
    const out = execFileSync('sh', [
      '-c',
      `find ${quoted} -maxdepth 6 \\( -iname '*.ttf' -o -iname '*.otf' -o -iname '*.ttc' \\) -printf '%h\\n' 2>/dev/null | sort -u`,
    ]).toString().trim();
    return out ? out.split('\n') : [];
  } catch {
    return [];
  }
}

export function ensureFonts(): void {
  if (process.env.FONTCONFIG_FILE) return;
  if (systemHasFonts()) return;

  if (!fs.existsSync(FC_FILE)) {
    const dirs = findFontDirs();
    fs.mkdirSync(FC_CACHE, { recursive: true });
    const body = [
      '<?xml version="1.0"?>',
      '<!DOCTYPE fontconfig SYSTEM "fonts.dtd">',
      '<fontconfig>',
      ...dirs.map((d) => `  <dir>${d}</dir>`),
      `  <cachedir>${FC_CACHE}</cachedir>`,
      '</fontconfig>',
      '',
    ].join('\n');
    fs.writeFileSync(FC_FILE, body);
  }
  process.env.FONTCONFIG_FILE = FC_FILE;
}
