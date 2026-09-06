# Devin Code direct API spike

This isolated spike tests whether Devin's existing local login can call its private Connect/protobuf backend without launching `devin -p` or ACP.

## Evidence and provenance

The installed Devin CLI 2026.8.18 embeds these method paths and the strings `connect-protocol-version` and `application/proto`:

- `/exa.api_server_pb.ApiServerService/GetCliModelConfigs`
- `/exa.api_server_pb.ApiServerService/AssignModel`
- `/exa.api_server_pb.ApiServerService/GetChatMessage`

The same binary names `GetCliModelConfigsResponse`, `ModelAssignment`, `AssignModelResponse`, `GetChatMessageResponse`, and streaming delta fields. No standalone `.proto` files or serialized `FileDescriptorSet` were found. It does **not** guess the complex chat request schema.

## Secret safety

The credential file is read only for explicit live discovery. The API key is held in an unexported field and used only to construct the request header. Output contains only server origin, key presence, HTTP status, response length, and protobuf field/wire counts. It never prints request headers, response bodies, credential values, or TOML contents.

Credential access fails closed unless the path is a regular non-symlink owned by the current user with no group/world permissions. Calls are pinned to `server.codeium.com`; redirects are rejected. The installed credential file was mode `0644`, so the hardened spike refuses to use it and does not mutate it.

## Run

Inspection only:

```bash
go run .
```

Read-only live discovery:

```bash
go run . -live-discovery
```

Before fail-closed permission handling was added, a Bearer request reached the service but returned HTTP 400. Separate manual probes tried other header spellings with the same status; that is anecdotal and does not prove authentication. The principal blocker is the missing `GetCliModelConfigs` request descriptor: its request is not known to be empty. The spike intentionally reports only status and never emits the error body.

Tests use fake credentials only:

```bash
go test ./...
```

## Chat blocker

Typed discovery needs the exact `GetCliModelConfigs` request schema. Direct chat additionally needs exact schemas for `AssignModel` and streaming `GetChatMessage`, including assignment JWT placement, conversation nodes, tools, and Connect streaming framing. Binary symbol strings establish names but not field numbers or cardinality. Until descriptors are recovered from an authoritative artifact, ACP remains the supported adapter and direct chat must not send guessed protobuf to a private production endpoint.
