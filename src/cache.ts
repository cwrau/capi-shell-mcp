import type { CacheConfig } from './config.js';

export class TTLCache<K, V> {
  private store = new Map<K, { value: V; expiresAt: number }>();

  constructor(private readonly ttlSeconds: number) {}

  get(key: K): V | undefined {
    const entry = this.store.get(key);
    if (!entry) return undefined;
    if (Date.now() > entry.expiresAt) {
      this.store.delete(key);
      return undefined;
    }
    return entry.value;
  }

  set(key: K, value: V): void {
    this.store.set(key, { value, expiresAt: Date.now() + this.ttlSeconds * 1000 });
  }

  delete(key: K): void {
    this.store.delete(key);
  }

  clear(): void {
    this.store.clear();
  }
}

export interface ProxyTarget {
  sshuttleHost: string;
  apiServerIp: string;
  apiServerPort: string;
}

export interface CachedKubeconfig {
  kubeconfig: string;
  proxyTarget?: ProxyTarget;
}

export interface CacheStore {
  clusterList: TTLCache<string, import('./k8s.js').CAPICluster[]>;
  kubeconfig: TTLCache<string, CachedKubeconfig>;
}

export function clusterListKey(managementCluster: string, context: string): string {
  return `${managementCluster}:${context}`;
}

export function kubeconfigKey(
  managementCluster: string,
  context: string,
  namespace: string,
  clusterName: string,
): string {
  return `${managementCluster}:${context}:${namespace}:${clusterName}`;
}

export function createCacheStore(cfg: CacheConfig): CacheStore {
  return {
    clusterList: new TTLCache(cfg.cluster_list_ttl),
    kubeconfig: new TTLCache(cfg.kubeconfig_ttl),
  };
}
