# Connecting clients to declared services

`pluto connect` prepares a route to one named service in a box contract. It
ensures the box is running, waits until the guest reports that service's
supervised process as `active`, and returns a local endpoint and connection
instructions. This readiness signal means the service manager sees the
process as active; it does not probe the service's protocol or guarantee that
the application has finished its own initialization.

```sh
pluto connect opencode --json
pluto connect <box-id> opencode --local-port 14096
```

The default access route is an SSH tunnel through the box's vsock SSH server.
The result includes the endpoint and an `ssh -N -L` command. Run that command
on the Pluto host and leave it running while a client on that host uses the
host-local endpoint. From another machine, forward that endpoint over SSH with
`ssh -L <local-port>:127.0.0.1:<local-port> <pluto-host>` and point the client
at its local port. The service remains responsible for its protocol,
authentication, pairing, and conversation state. Pluto does not return or
inspect service credentials.

## HTTP/JSON API

`POST /v1/boxes/{id}/connect` accepts:

```json
{
  "service": "opencode",
  "access_mode": "ssh-tunnel",
  "provider": "ssh",
  "local_port": 14096
}
```

`access_mode` and `provider` default to `ssh-tunnel` and `ssh`; `local_port`
defaults to the declared service port. The response contains `box_id`,
`service`, `endpoint`, and `access` (`mode`, `provider`, `instructions`, and
an authentication ownership note). An undeclared service, missing port,
unsupported route, or unapproved provider is rejected before wake. A provider
must declare `public_service_ingress`, be installed and enabled, and implement
the internal `ServiceIngress` interface before it can be selected with
`access_mode: "provider"`. The provider receives only the selected box,
service, and port; its route must not expose Pluto's task/control API.

Service readiness, provider, and runner failures return actionable JSON errors.
Service startup failure/timeout uses HTTP 503; lifecycle/provider failures
retain their corresponding conflict or gateway status. A failed service can
be inspected with `pluto logs <box> --service <name>`.

## OpenCode client compatibility

The verified compatibility check is OpenCode CLI v2.0.24 connecting to an
in-box OpenCode v2 server over the SSH tunnel described by
[`m5-client-compatibility.md`](research/m5-client-compatibility.md). It queried
the server API and rendered the remote TUI without making a model call. With
Pluto, use `pluto connect <box> opencode --local-port 14096 --json`, run the
returned SSH command, then use the endpoint from the JSON response:

```sh
OPENCODE_SERVER_PASSWORD='<box-password>' opencode api --server http://127.0.0.1:14096 GET /api/info
OPENCODE_SERVER_PASSWORD='<box-password>' opencode --server http://127.0.0.1:14096
```

The OpenCode CLI also accepts `OPENCODE_PASSWORD`. The check used CLI v2.0.24
on both sides, queried `/api/info`, `/api/config`, `/api/agent`, and
`/api/session`, and rendered the remote TUI without a model call. The password
belongs to the OpenCode service and must be kept private.

OpenCode Desktop v2 supports a native SSH server connection and its Add
Server/pairing flow according to its v2 documentation, but Pluto's Desktop GUI
connection has not been hands-on validated. The installed AppImage at
`~/AppImages/opencode.appimage` is v2.0.14 and was not scriptable in this
validation environment. Treat Desktop connectivity as a documented client
path awaiting manual GUI verification; do not read the CLI check as Desktop
validation. T3 Code is not a direct OpenCode server client.
