# log-mcp

Read-only MCP server for querying logs across local and SSH hosts.

## Build

```bash
go build ./cmd/log-mcp
```

## Run

Stdio mode:

```bash
./log-mcp -config config.example.json
```

HTTP mode for supervisor:

```bash
./log-mcp -config /var/www/slp/log-mcp/config.json -listen 127.0.0.1:18080
```

The server supports MCP-style JSON-RPC over stdio by default. With `-listen`, it exposes:

- `GET /healthz`
- `POST /mcp`

Configure your MCP client or proxy to call the HTTP endpoint.

## Jenkins Deploy

The included `Jenkinsfile` builds the binary into `dist/log-mcp`. On the `main` branch it deploys to:

```text
/var/www/slp/log-mcp
```

It also installs:

```text
/etc/supervisor/conf.d/log-mcp.conf
```

and runs:

```bash
sudo supervisorctl reread
sudo supervisorctl update
sudo supervisorctl restart log-mcp
```

## Tools

- `list_log_sources`: list configured log sources and hosts.
- `tail_log`: read the newest lines from one source.
- `search_log`: search one source with a regular expression.
- `search_all_logs`: search every configured source.
- `read_log_window`: read lines around a line number from one concrete log file.

## Configuration

Hosts can be local or SSH:

```json
{
  "id": "test-a",
  "type": "ssh",
  "ssh_target": "deploy@test-a.example.com",
  "ssh_options": ["-o", "BatchMode=yes", "-o", "ConnectTimeout=5"]
}
```

Sources are whitelisted paths or globs:

```json
{
  "id": "supervisor_a",
  "host": "test-a",
  "name": "test-a supervisor logs",
  "path_glob": "/var/log/supervisor/*.log"
}
```

The server never exposes arbitrary shell execution to the model. It only runs bounded `tail`, `rg`, and `awk` commands against configured paths.
