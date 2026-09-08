# Local Broker service deployment

The [`atl-broker.service`](../examples/broker-local/atl-broker.service) and
configuration template run the read-only ATL Broker as one foreground systemd
service on two loopback TLS listeners. They do not provision an authority,
policy, certificates, or credentials, and they do not enable Broker client
mode.

Copy [`broker.example.json`](../examples/broker-local/broker.example.json) to
an owner-private directory such as `/etc/atl-broker`, replace every example
origin and identity, and place each referenced file beside it. The directory
must be owned by the dedicated service account with mode `0700`; every file
must be regular, owned by that account, and mode `0600`. Credential files
contain the exact bearer bytes with no newline. The Broker refuses inline
secrets, path traversal, ambient proxy variables, system trust fallback,
public listeners, symlinks, special files, and loose owner permissions.

Validate the example unit before activation:

```bash named-broker-systemd-validation
systemd-analyze verify ./examples/broker-local/atl-broker.service
```

The sample uses directives documented by the official
[`systemd.exec`](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html),
[`systemd.resource-control`](https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html),
and [`systemd.service`](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html)
references. The deployment owner must additionally enforce destination-level
egress outside this unit: allow only the exact authority and selected backend
origins, and deny direct client access to Jira/Confluence. Address-family
restriction alone is not a destination allowlist.

Start only after the external authority implements the published Broker
authentication and decision protocol. The service performs no startup probe;
readiness is an authenticated, content-minimized local state and never proves
that a particular resource is accessible.
