import { spawn, type ChildProcess } from 'node:child_process';
import { shell } from './shell.js';

type ProxyEntry =
  | { kind: 'process'; process: ChildProcess; timer: ReturnType<typeof setTimeout> }
  | { kind: 'systemd'; unitName: string; timer: ReturnType<typeof setTimeout> };

function unitName(apiServerIp: string, apiServerPort: string): string {
  return `capi-shell-api-endpoint-proxy-sshuttle-${apiServerIp}:${apiServerPort}.service`;
}

function isUnderSystemd(): boolean {
  return process.env.INVOCATION_ID !== undefined;
}

async function stopUnit(name: string): Promise<void> {
  await shell.execFile('systemctl', ['--user', 'stop', name], {});
}

const proxyStore = new Map<string, ProxyEntry>();

export const proxyShell = {
  spawn(host: string, ip: string, port: string): ChildProcess {
    return spawn('sshuttle', ['-r', host, `${ip}:${port}`], { stdio: 'ignore' });
  },
};

function targetKey(apiServerIp: string, apiServerPort: string): string {
  return `${apiServerIp}:${apiServerPort}`;
}

export function killProxy(apiServerIp: string, apiServerPort: string): void {
  const key = targetKey(apiServerIp, apiServerPort);
  const entry = proxyStore.get(key);
  if (!entry) return;
  clearTimeout(entry.timer);
  proxyStore.delete(key);
  if (entry.kind === 'process') entry.process.kill('SIGTERM');
  else void stopUnit(entry.unitName);
}

export function killAllProxies(): void {
  for (const key of [...proxyStore.keys()]) {
    const entry = proxyStore.get(key);
    if (!entry) continue;
    clearTimeout(entry.timer);
    proxyStore.delete(key);
    if (entry.kind === 'process') entry.process.kill('SIGTERM');
    else void stopUnit(entry.unitName);
  }
}

export function refreshProxy(apiServerIp: string, apiServerPort: string, ttlSeconds: number): void {
  const key = targetKey(apiServerIp, apiServerPort);
  const entry = proxyStore.get(key);
  if (!entry) return;
  clearTimeout(entry.timer);
  entry.timer = setTimeout(() => killProxy(apiServerIp, apiServerPort), ttlSeconds * 1000);
}

const pendingProxies = new Map<string, Promise<void>>();

export async function ensureProxy(
  apiServerIp: string,
  apiServerPort: string,
  sshuttleHost: string,
  ttlSeconds: number,
): Promise<void> {
  const key = targetKey(apiServerIp, apiServerPort);

  if (proxyStore.has(key)) {
    refreshProxy(apiServerIp, apiServerPort, ttlSeconds);
    return;
  }

  const pending = pendingProxies.get(key);
  if (pending) return pending;

  const spawnPromise = isUnderSystemd()
    ? (async () => {
        const name = unitName(apiServerIp, apiServerPort);
        await shell.execFile('systemd-run', [
          '--user',
          `--unit=${name}`,
          '--slice=capi-shell-api-endpoint-proxy.slice',
          '--',
          'sshuttle', '-r', sshuttleHost, `${apiServerIp}:${apiServerPort}`,
        ], {});
        const killTimer = setTimeout(() => killProxy(apiServerIp, apiServerPort), ttlSeconds * 1000);
        proxyStore.set(key, { kind: 'systemd', unitName: name, timer: killTimer });
      })()
    : (async () => {
        const proc = proxyShell.spawn(sshuttleHost, apiServerIp, apiServerPort);
        proc.unref();

        // Wait up to 1s for sshuttle to start; reject if it dies first
        await new Promise<void>((resolve, reject) => {
          const t = setTimeout(resolve, 1000);
          proc.once('error', (err) => { clearTimeout(t); reject(err); });
          proc.once('exit', (code) => {
            clearTimeout(t);
            reject(new Error(`sshuttle exited early with code ${code ?? 'unknown'}`));
          });
        });

        const killTimer = setTimeout(() => killProxy(apiServerIp, apiServerPort), ttlSeconds * 1000);
        proxyStore.set(key, { kind: 'process', process: proc, timer: killTimer });

        // If sshuttle exits after startup, remove the dead entry from the store
        proc.once('exit', () => {
          const entry = proxyStore.get(key);
          if (entry) {
            clearTimeout(entry.timer);
            proxyStore.delete(key);
          }
        });
      })();

  pendingProxies.set(key, spawnPromise);
  try {
    await spawnPromise;
  } finally {
    pendingProxies.delete(key);
  }
}

process.on('exit', killAllProxies);
process.on('SIGINT', () => { killAllProxies(); process.exit(0); });
process.on('SIGTERM', () => { killAllProxies(); process.exit(0); });
