import { loadConfig } from './config.js';
import { createCacheStore } from './cache.js';
import { reconcileProxies } from './proxy.js';
import { startHttpServer } from './http-server.js';

const config = loadConfig();
const cache = createCacheStore(config.cache);

await reconcileProxies(config.cache.kubeconfig_ttl);

const port = Number(process.env.CAPI_SHELL_MCP_PORT ?? '4737');
const server = await startHttpServer(config, cache, port);

async function shutdown(): Promise<void> {
  await server.close();
  process.exit(0);
}

process.on('SIGINT', () => { void shutdown(); });
process.on('SIGTERM', () => { void shutdown(); });
