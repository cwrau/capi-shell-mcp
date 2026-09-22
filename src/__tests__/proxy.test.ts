import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { EventEmitter } from 'node:events';
import { proxyShell, ensureProxy, killProxy, killAllProxies, refreshProxy } from '../proxy.js';

function makeFakeProcess() {
  const ee = new EventEmitter() as EventEmitter & {
    kill: ReturnType<typeof vi.fn>;
    unref: ReturnType<typeof vi.fn>;
    exitCode: number | null;
  };
  ee.kill = vi.fn();
  ee.unref = vi.fn();
  ee.exitCode = null;
  return ee;
}

describe('ensureProxy', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    killAllProxies();
  });

  afterEach(() => {
    killAllProxies();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('starts sshuttle and stores entry', async () => {
    const fakeProc = makeFakeProcess();
    vi.spyOn(proxyShell, 'spawn').mockReturnValue(fakeProc as never);

    const promise = ensureProxy('10.0.0.1', '6443', 'user@bastion', 60);
    await vi.advanceTimersByTimeAsync(1100);
    await promise;

    expect(proxyShell.spawn).toHaveBeenCalledWith('user@bastion', '10.0.0.1', '6443');
    expect(fakeProc.unref).toHaveBeenCalled();
  });

  it('refreshes timer on repeated call without restarting', async () => {
    const fakeProc = makeFakeProcess();
    vi.spyOn(proxyShell, 'spawn').mockReturnValue(fakeProc as never);

    const p1 = ensureProxy('10.0.1.1', '6443', 'user@bastion', 60);
    await vi.advanceTimersByTimeAsync(1100);
    await p1;

    await ensureProxy('10.0.1.1', '6443', 'user@bastion', 60);
    expect(proxyShell.spawn).toHaveBeenCalledTimes(1);
  });

  it('kills proxy after TTL expires', async () => {
    const fakeProc = makeFakeProcess();
    vi.spyOn(proxyShell, 'spawn').mockReturnValue(fakeProc as never);

    const p = ensureProxy('10.0.2.1', '6443', 'user@bastion', 60);
    await vi.advanceTimersByTimeAsync(1100);
    await p;

    vi.advanceTimersByTime(61_000);
    expect(fakeProc.kill).toHaveBeenCalledWith('SIGTERM');
  });

  it('rejects if sshuttle exits during startup window', async () => {
    const fakeProc = makeFakeProcess();
    vi.spyOn(proxyShell, 'spawn').mockReturnValue(fakeProc as never);

    const promise = ensureProxy('10.0.3.1', '6443', 'user@bastion', 60);
    fakeProc.emit('exit', 1);

    await expect(promise).rejects.toThrow(/exited early/);
  });

  it('spawns exactly once for two concurrent calls with the same target', async () => {
    const fakeProc = makeFakeProcess();
    vi.spyOn(proxyShell, 'spawn').mockReturnValue(fakeProc as never);

    const p1 = ensureProxy('10.0.4.1', '6443', 'user@bastion', 60);
    const p2 = ensureProxy('10.0.4.1', '6443', 'user@bastion', 60);
    await vi.advanceTimersByTimeAsync(1100);
    await Promise.all([p1, p2]);

    expect(proxyShell.spawn).toHaveBeenCalledTimes(1);
  });

  it('shares one proxy across different sshuttleHost values for the same target', async () => {
    const fakeProc = makeFakeProcess();
    vi.spyOn(proxyShell, 'spawn').mockReturnValue(fakeProc as never);

    const p1 = ensureProxy('10.0.5.1', '6443', 'user@bastion-a', 60);
    await vi.advanceTimersByTimeAsync(1100);
    await p1;

    // Different sshuttleHost, same target — dedup wins, no second spawn.
    await ensureProxy('10.0.5.1', '6443', 'user@bastion-b', 60);
    expect(proxyShell.spawn).toHaveBeenCalledTimes(1);
  });
});

describe('refreshProxy', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    killAllProxies();
  });

  afterEach(() => {
    killAllProxies();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('extends proxy TTL', async () => {
    const fakeProc = makeFakeProcess();
    vi.spyOn(proxyShell, 'spawn').mockReturnValue(fakeProc as never);

    const p = ensureProxy('10.0.6.1', '6443', 'user@bastion', 60);
    await vi.advanceTimersByTimeAsync(1100);
    await p;

    vi.advanceTimersByTime(50_000);
    refreshProxy('10.0.6.1', '6443', 60);

    vi.advanceTimersByTime(50_000);
    expect(fakeProc.kill).not.toHaveBeenCalled();

    vi.advanceTimersByTime(10_000);
    expect(fakeProc.kill).toHaveBeenCalledWith('SIGTERM');
  });
});

describe('killProxy', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    killAllProxies();
  });

  afterEach(() => {
    killAllProxies();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('kills process and removes from store', async () => {
    const fakeProc = makeFakeProcess();
    vi.spyOn(proxyShell, 'spawn').mockReturnValue(fakeProc as never);

    const p = ensureProxy('10.0.7.1', '6443', 'user@bastion', 60);
    await vi.advanceTimersByTimeAsync(1100);
    await p;

    killProxy('10.0.7.1', '6443');
    expect(fakeProc.kill).toHaveBeenCalledWith('SIGTERM');

    killProxy('10.0.7.1', '6443');
    expect(fakeProc.kill).toHaveBeenCalledTimes(1);
  });
});
