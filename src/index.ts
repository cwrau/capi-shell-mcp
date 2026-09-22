#!/usr/bin/env node
import { loadConfig } from './config.js';
import { createCacheStore } from './cache.js';
import { reconcileProxies } from './proxy.js';
import { startHttpServer } from './http-server.js';

const config = loadConfig();
const cache = createCacheStore(config.cache);

try {
  await reconcileProxies(config.cache.kubeconfig_ttl);
} catch (err) {
  console.error('proxy reconciliation failed, continuing without adopting existing proxies:', err);
}

const port = Number(process.env.CAPI_SHELL_MCP_PORT ?? '4737');
const server = await startHttpServer(config, cache, port);

async function shutdown(): Promise<void> {
  await server.close();
  process.exit(0);
}

process.on('SIGINT', () => { shutdown().catch((err) => { console.error('shutdown failed:', err); process.exit(1); }); });
process.on('SIGTERM', () => { shutdown().catch((err) => { console.error('shutdown failed:', err); process.exit(1); }); });
