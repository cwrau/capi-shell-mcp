import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import type { AppConfig } from '../config.js';
import { createCacheStore } from '../cache.js';

const stubs = vi.hoisted(() => ({
  customObjects: {} as Record<string, ReturnType<typeof vi.fn>>,
}));

vi.mock('@kubernetes/client-node', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@kubernetes/client-node')>();

  class FakeKubeConfig {
    loadFromFile(_path: string) {}
    setCurrentContext(_ctx: string) {}
    makeApiClient(apiClientType: unknown) {
      if (apiClientType === actual.CustomObjectsApi) return stubs.customObjects;
      throw new Error('unexpected api client type in test');
    }
  }

  return { ...actual, KubeConfig: FakeKubeConfig };
});

const testConfig: AppConfig = {
  management_clusters: { prod: { name: 'prod', kubeconfig: '/kube/prod.yaml', context: 'prod-admin' } },
  cache: { cluster_list_ttl: 300, kubeconfig_ttl: 3600 },
};

// The ambient environment (e.g. a systemd --user session) may already have
// INVOCATION_ID set, which would make startHttpServer spuriously try to call
// the real systemd-notify binary. Force it unset for the duration of each test.
let savedInvocationId: string | undefined;

function suppressInvocationId() {
  savedInvocationId = process.env.INVOCATION_ID;
  delete process.env.INVOCATION_ID;
}

function restoreInvocationId() {
  if (savedInvocationId === undefined) delete process.env.INVOCATION_ID;
  else process.env.INVOCATION_ID = savedInvocationId;
}

async function connectClient(port: number): Promise<Client> {
  const client = new Client({ name: 'test-client', version: '0.0.0' });
  const transport = new StreamableHTTPClientTransport(new URL(`http://127.0.0.1:${port}/mcp`));
  await client.connect(transport);
  return client;
}

describe('startHttpServer', () => {
  let close: () => Promise<void>;
  let port: number;

  beforeEach(async () => {
    suppressInvocationId();

    stubs.customObjects.listCustomObjectForAllNamespaces = vi.fn().mockResolvedValue({
      items: [{ metadata: { name: 'cluster-1', namespace: 'ns-1' } }],
    });

    const { startHttpServer } = await import('../http-server.js');
    const cache = createCacheStore(testConfig.cache);
    port = 34000 + Math.floor(Math.random() * 1000);
    const server = await startHttpServer(testConfig, cache, port);
    close = server.close;
  });

  afterEach(async () => {
    await close();
    vi.restoreAllMocks();
    restoreInvocationId();
  });

  it('serves two separate sessions that share the same cluster-list cache', async () => {
    const clientA = await connectClient(port);
    const resultA = await clientA.callTool({ name: 'list_clusters', arguments: {} });
    expect(JSON.stringify(resultA.content)).toContain('cluster-1');

    const clientB = await connectClient(port);
    const resultB = await clientB.callTool({ name: 'list_clusters', arguments: {} });
    expect(JSON.stringify(resultB.content)).toContain('cluster-1');

    // Two independent sessions, one shared cache: the mocked k8s API is hit once.
    expect(stubs.customObjects.listCustomObjectForAllNamespaces).toHaveBeenCalledTimes(1);

    await clientA.close();
    await clientB.close();
  });
});
