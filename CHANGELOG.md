# Changelog

## [2.1.0](https://github.com/cwrau/capi-shell-mcp/compare/v2.0.0...v2.1.0) (2026-09-25)


### Features

* bundle systemd unit files into release tarballs ([75570c7](https://github.com/cwrau/capi-shell-mcp/commit/75570c716761cdde697e8276935238c26d363fe7))
* include LICENSE in release tarballs ([47c03b2](https://github.com/cwrau/capi-shell-mcp/commit/47c03b2e90d2515b4e89625f59852b40246bb7c9))


### Bug Fixes

* strip debug symbols from release binaries ([a86a3d9](https://github.com/cwrau/capi-shell-mcp/commit/a86a3d95ad3d3d4b1e4b96baeb6f2e881468b288))

## [2.0.0](https://github.com/cwrau/capi-shell-mcp/compare/v1.0.0...v2.0.0) (2026-09-25)


### ⚠ BREAKING CHANGES

* rewrite in Go, embedding kubernetes-mcp-server's tools
* rename capo-shell-mcp to capi-shell-mcp

### Features

* add read-only functions ([c732445](https://github.com/cwrau/capi-shell-mcp/commit/c73244561c2179025833c4c41ec1045752987754))
* attach npm pack tarball to GitHub releases ([08af99b](https://github.com/cwrau/capi-shell-mcp/commit/08af99b374556e4d601ef6f09a7e30bee492790f))
* bundle runtime deps into the release tarball ([c03f77b](https://github.com/cwrau/capi-shell-mcp/commit/c03f77be0395869849acc84168430378bc2238f6))
* enable renovate ([786ecbb](https://github.com/cwrau/capi-shell-mcp/commit/786ecbb19d3d99613ca44c0d0733996b0ecd1e9b))
* initial commit ([f731ee2](https://github.com/cwrau/capi-shell-mcp/commit/f731ee2492a8c75ff19ea48ea47bddcc603cd2af))
* read sshuttle host from plugins.api-endpoint-proxy.sshuttle.host, matching the real capi-shell CLI ([c9f3a9c](https://github.com/cwrau/capi-shell-mcp/commit/c9f3a9c4d2568b1e56f69ddc3104910f60e0338c))
* reconcile existing systemd-managed proxies on daemon startup ([716b5da](https://github.com/cwrau/capi-shell-mcp/commit/716b5da80690ffd9c77b8d9f51cb38bc5da77195))
* rename capo-shell-mcp to capi-shell-mcp ([dfe8bb7](https://github.com/cwrau/capi-shell-mcp/commit/dfe8bb7a056fce40681bbaba5f6ccd39253027c3))
* replace stdio transport with a persistent HTTP daemon ([1073fb5](https://github.com/cwrau/capi-shell-mcp/commit/1073fb5a997333f8e1e7d2074dfdae022f6c7eed))
* rewrite in Go, embedding kubernetes-mcp-server's tools ([608bd78](https://github.com/cwrau/capi-shell-mcp/commit/608bd78176f4d7e92db82e7fb8df05ba86c94f99))
* setup release-please and CI ([4c2e13b](https://github.com/cwrau/capi-shell-mcp/commit/4c2e13b81484425c5e6bfac449359187ccac0cdd))
* spawn proxies via systemd-run when running under systemd ([691e3d0](https://github.com/cwrau/capi-shell-mcp/commit/691e3d05099e0140266d763097f31e3450a13337))
* update node version ([2087933](https://github.com/cwrau/capi-shell-mcp/commit/2087933fb283f9a886d92f6513520a4b731628b4))
* use native api calls ([333f471](https://github.com/cwrau/capi-shell-mcp/commit/333f4711876b2a44bdaec7a37b278d645e8d66bc))


### Bug Fixes

* add NotifyAccess=all so systemd-notify from a child process is accepted ([1458351](https://github.com/cwrau/capi-shell-mcp/commit/1458351086b2d9944c180f32d685764c4971a330))
* adjust branch ([0156aff](https://github.com/cwrau/capi-shell-mcp/commit/0156aff1480f2c6e0c162546024e73c83208f580))
* bump @kubernetes/client-node to v2 and vitest to v5 ([10315e6](https://github.com/cwrau/capi-shell-mcp/commit/10315e65a0c992bf549d1ab56386109f3da20bdf))
* correct reconciliation TTL parsing and fire-and-forget teardown ([1df2a92](https://github.com/cwrau/capi-shell-mcp/commit/1df2a92471685a5b15c6904c56401a27a1415dc8))
* dedup concurrent ensureProxy calls for the same key ([e604fb9](https://github.com/cwrau/capi-shell-mcp/commit/e604fb9a3f56394b6f5c028b3bca89381229fb6e))
* exclude tests and dts/map output from published package ([24c6705](https://github.com/cwrau/capi-shell-mcp/commit/24c67050b361a758de2b6c7e10ea22b86a908a3a))
* harden daemon startup/shutdown and document host-header constraint ([98870ac](https://github.com/cwrau/capi-shell-mcp/commit/98870acd8d236d10963cdccc37e5718b264628a2))
* make dist/index.js self-executing via shebang, matching the bin field ([d07d811](https://github.com/cwrau/capi-shell-mcp/commit/d07d811d5b65924c4adf6032d8ede915b0a8ce79))
* management_clusters config as name-to-config map ([b35fcf4](https://github.com/cwrau/capi-shell-mcp/commit/b35fcf45e3a1c9705f06f74bf21481c01bca9b9c))
* override @hono/node-server to patch path-traversal advisory ([0451294](https://github.com/cwrau/capi-shell-mcp/commit/0451294722743b26e04d885d711893d83c32063d))
* pin npm@11.13.0 via packageManager and sync lockfile ([7764868](https://github.com/cwrau/capi-shell-mcp/commit/7764868bbd422a0732e7ae6eb4052a7e7005d3b0))
* strip all bare-* prebuilds, not just non-Linux ones ([5c546b6](https://github.com/cwrau/capi-shell-mcp/commit/5c546b6ea4cff2309fdc7bb6350c10f6f34a522b))
* sync lockfile and bump CI action versions ([6091e7c](https://github.com/cwrau/capi-shell-mcp/commit/6091e7c69578ccc932a4f2b26c2c1d5df639be02))
* update dependencies to clear npm audit vulnerabilities ([a687188](https://github.com/cwrau/capi-shell-mcp/commit/a68718831760a817cf70c4990039da06c8c5a77a))

## [1.0.0](https://github.com/cwrau/capi-shell-mcp/compare/capi-shell-mcp-v0.4.0...capi-shell-mcp-v1.0.0) (2026-09-22)


### ⚠ BREAKING CHANGES

* rename capo-shell-mcp to capi-shell-mcp

### Features

* add read-only functions ([c732445](https://github.com/cwrau/capi-shell-mcp/commit/c73244561c2179025833c4c41ec1045752987754))
* enable renovate ([786ecbb](https://github.com/cwrau/capi-shell-mcp/commit/786ecbb19d3d99613ca44c0d0733996b0ecd1e9b))
* initial commit ([f731ee2](https://github.com/cwrau/capi-shell-mcp/commit/f731ee2492a8c75ff19ea48ea47bddcc603cd2af))
* read sshuttle host from plugins.api-endpoint-proxy.sshuttle.host, matching the real capi-shell CLI ([c9f3a9c](https://github.com/cwrau/capi-shell-mcp/commit/c9f3a9c4d2568b1e56f69ddc3104910f60e0338c))
* reconcile existing systemd-managed proxies on daemon startup ([716b5da](https://github.com/cwrau/capi-shell-mcp/commit/716b5da80690ffd9c77b8d9f51cb38bc5da77195))
* rename capo-shell-mcp to capi-shell-mcp ([dfe8bb7](https://github.com/cwrau/capi-shell-mcp/commit/dfe8bb7a056fce40681bbaba5f6ccd39253027c3))
* replace stdio transport with a persistent HTTP daemon ([1073fb5](https://github.com/cwrau/capi-shell-mcp/commit/1073fb5a997333f8e1e7d2074dfdae022f6c7eed))
* setup release-please and CI ([4c2e13b](https://github.com/cwrau/capi-shell-mcp/commit/4c2e13b81484425c5e6bfac449359187ccac0cdd))
* spawn proxies via systemd-run when running under systemd ([691e3d0](https://github.com/cwrau/capi-shell-mcp/commit/691e3d05099e0140266d763097f31e3450a13337))
* update node version ([2087933](https://github.com/cwrau/capi-shell-mcp/commit/2087933fb283f9a886d92f6513520a4b731628b4))
* use native api calls ([333f471](https://github.com/cwrau/capi-shell-mcp/commit/333f4711876b2a44bdaec7a37b278d645e8d66bc))


### Bug Fixes

* add NotifyAccess=all so systemd-notify from a child process is accepted ([1458351](https://github.com/cwrau/capi-shell-mcp/commit/1458351086b2d9944c180f32d685764c4971a330))
* adjust branch ([0156aff](https://github.com/cwrau/capi-shell-mcp/commit/0156aff1480f2c6e0c162546024e73c83208f580))
* bump @kubernetes/client-node to v2 and vitest to v5 ([10315e6](https://github.com/cwrau/capi-shell-mcp/commit/10315e65a0c992bf549d1ab56386109f3da20bdf))
* correct reconciliation TTL parsing and fire-and-forget teardown ([1df2a92](https://github.com/cwrau/capi-shell-mcp/commit/1df2a92471685a5b15c6904c56401a27a1415dc8))
* dedup concurrent ensureProxy calls for the same key ([e604fb9](https://github.com/cwrau/capi-shell-mcp/commit/e604fb9a3f56394b6f5c028b3bca89381229fb6e))
* exclude tests and dts/map output from published package ([24c6705](https://github.com/cwrau/capi-shell-mcp/commit/24c67050b361a758de2b6c7e10ea22b86a908a3a))
* harden daemon startup/shutdown and document host-header constraint ([98870ac](https://github.com/cwrau/capi-shell-mcp/commit/98870acd8d236d10963cdccc37e5718b264628a2))
* make dist/index.js self-executing via shebang, matching the bin field ([d07d811](https://github.com/cwrau/capi-shell-mcp/commit/d07d811d5b65924c4adf6032d8ede915b0a8ce79))
* management_clusters config as name-to-config map ([b35fcf4](https://github.com/cwrau/capi-shell-mcp/commit/b35fcf45e3a1c9705f06f74bf21481c01bca9b9c))
* override @hono/node-server to patch path-traversal advisory ([0451294](https://github.com/cwrau/capi-shell-mcp/commit/0451294722743b26e04d885d711893d83c32063d))
* pin npm@11.13.0 via packageManager and sync lockfile ([7764868](https://github.com/cwrau/capi-shell-mcp/commit/7764868bbd422a0732e7ae6eb4052a7e7005d3b0))
* sync lockfile and bump CI action versions ([6091e7c](https://github.com/cwrau/capi-shell-mcp/commit/6091e7c69578ccc932a4f2b26c2c1d5df639be02))
* update dependencies to clear npm audit vulnerabilities ([a687188](https://github.com/cwrau/capi-shell-mcp/commit/a68718831760a817cf70c4990039da06c8c5a77a))

## [0.4.0](https://github.com/cwrau/capo-shell-mcp/compare/capo-shell-mcp-v0.3.0...capo-shell-mcp-v0.4.0) (2026-07-23)


### Features

* use native api calls ([333f471](https://github.com/cwrau/capo-shell-mcp/commit/333f4711876b2a44bdaec7a37b278d645e8d66bc))


### Bug Fixes

* override @hono/node-server to patch path-traversal advisory ([0451294](https://github.com/cwrau/capo-shell-mcp/commit/0451294722743b26e04d885d711893d83c32063d))

## [0.3.0](https://github.com/cwrau/capo-shell-mcp/compare/capo-shell-mcp-v0.2.0...capo-shell-mcp-v0.3.0) (2026-07-01)


### Features

* add read-only functions ([c732445](https://github.com/cwrau/capo-shell-mcp/commit/c73244561c2179025833c4c41ec1045752987754))

## [0.2.0](https://github.com/cwrau/capo-shell-mcp/compare/capo-shell-mcp-v0.1.0...capo-shell-mcp-v0.2.0) (2026-06-23)


### Features

* enable renovate ([786ecbb](https://github.com/cwrau/capo-shell-mcp/commit/786ecbb19d3d99613ca44c0d0733996b0ecd1e9b))
* initial commit ([f731ee2](https://github.com/cwrau/capo-shell-mcp/commit/f731ee2492a8c75ff19ea48ea47bddcc603cd2af))
* setup release-please and CI ([4c2e13b](https://github.com/cwrau/capo-shell-mcp/commit/4c2e13b81484425c5e6bfac449359187ccac0cdd))
* update node version ([2087933](https://github.com/cwrau/capo-shell-mcp/commit/2087933fb283f9a886d92f6513520a4b731628b4))


### Bug Fixes

* adjust branch ([0156aff](https://github.com/cwrau/capo-shell-mcp/commit/0156aff1480f2c6e0c162546024e73c83208f580))
* pin npm@11.13.0 via packageManager and sync lockfile ([7764868](https://github.com/cwrau/capo-shell-mcp/commit/7764868bbd422a0732e7ae6eb4052a7e7005d3b0))
* sync lockfile and bump CI action versions ([6091e7c](https://github.com/cwrau/capo-shell-mcp/commit/6091e7c69578ccc932a4f2b26c2c1d5df639be02))
