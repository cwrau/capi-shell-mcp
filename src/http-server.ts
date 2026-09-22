import * as http from 'node:http';
import { randomUUID } from 'node:crypto';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { buildServer } from './server.js';
import type { AppConfig } from './config.js';
import type { CacheStore } from './cache.js';

async function notifySystemd(arg: '--ready' | '--stopping'): Promise<void> {
  if (process.env.NOTIFY_SOCKET === undefined) return;
  try {
    const { shell } = await import('./shell.js');
    await shell.execFile('systemd-notify', [arg], {});
  } catch (err) {
    console.error(`systemd-notify ${arg} failed:`, err);
  }
}

export async function startHttpServer(
  config: AppConfig,
  cache: CacheStore,
  port: number,
): Promise<{ close(): Promise<void> }> {
  const transports = new Map<string, StreamableHTTPServerTransport>();

  const httpServer = http.createServer((req, res) => {
    void (async () => {
      try {
        if (req.url !== '/mcp') {
          res.writeHead(404).end();
          return;
        }

        const host = req.headers.host ?? '';
        // Only 127.0.0.1 is accepted (not 'localhost') — MCP client configs must use the literal IP.
        if (host !== '127.0.0.1' && host !== `127.0.0.1:${port}`) {
          res.writeHead(421, { 'content-type': 'text/plain' }).end('Misdirected Request');
          return;
        }

        const sessionId = req.headers['mcp-session-id'];
        const existing = typeof sessionId === 'string' ? transports.get(sessionId) : undefined;
        if (existing) {
          await existing.handleRequest(req, res);
          return;
        }

        const transport = new StreamableHTTPServerTransport({
          sessionIdGenerator: randomUUID,
          onsessioninitialized: (id) => {
            transports.set(id, transport);
          },
        });
        transport.onclose = () => {
          if (transport.sessionId) transports.delete(transport.sessionId);
        };

        const server = buildServer(config, cache);
        await server.connect(transport);
        await transport.handleRequest(req, res);
      } catch (err) {
        console.error('error handling /mcp request:', err);
        if (!res.headersSent) res.writeHead(500).end();
      }
    })();
  });

  await new Promise<void>((resolve) => httpServer.listen(port, '127.0.0.1', resolve));

  await notifySystemd('--ready');

  return {
    async close() {
      for (const transport of transports.values()) await transport.close();
      await notifySystemd('--stopping');
      await new Promise<void>((resolve, reject) =>
        httpServer.close((err) => (err ? reject(err) : resolve())),
      );
    },
  };
}
