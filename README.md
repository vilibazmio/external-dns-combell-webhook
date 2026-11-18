# external-dns-combell-webhook

A [webhook provider](https://kubernetes-sigs.github.io/external-dns/latest/docs/tutorials/webhook-provider/)
for [external-dns](https://github.com/kubernetes-sigs/external-dns), so it can
manage DNS records hosted at [Combell](https://www.combell.com).

external-dns has no built-in Combell provider. Instead it can call a small HTTP
server running next to it in the same pod, which is what this is. It
implements the external-dns
[webhook API](https://kubernetes-sigs.github.io/external-dns/latest/api/webhook.yaml).

## Before you start

Two things will stop this working regardless of how it is configured.

**The Combell API is only available on reseller accounts.** Check under
Dashboard / Settings / API / Users that you can generate a key and secret before
going further.

**Combell denies every source IP by default.** Add the cluster's outbound address
under Dashboard / Settings / API / IP restrictions. Every request returns 401
until you do, and the error does not mention the whitelist.

Be aware of what the credential is. Combell requires the API user to be an
administrator with access to all products in the account, so the same key reaches
domain transfer, mailboxes, hosting settings and SSH keys. There is no DNS-only
scope. Putting it in a cluster is a deliberate choice, not a detail.

## Install

Create the credentials in the namespace external-dns runs in:

```bash
kubectl -n external-dns create secret generic combell-credentials \
  --from-literal=api-key=<your api key> \
  --from-literal=api-secret=<your api secret>
```

Install the official external-dns Helm chart with these values. The chart runs
this webhook as a second container in the external-dns pod.

```yaml
provider:
  name: webhook
  webhook:
    image:
      repository: ghcr.io/vilibazmio/external-dns-combell-webhook
      tag: 0.1.0
    env:
      - name: COMBELL_API_KEY
        valueFrom:
          secretKeyRef:
            name: combell-credentials
            key: api-key
      - name: COMBELL_API_SECRET
        valueFrom:
          secretKeyRef:
            name: combell-credentials
            key: api-secret
      - name: DOMAIN_FILTER
        value: example.com

txtOwnerId: my-cluster

extraArgs:
  webhook-provider-read-timeout: 30s
  webhook-provider-write-timeout: 2m
```

```bash
helm repo add external-dns https://kubernetes-sigs.github.io/external-dns/
helm upgrade --install external-dns external-dns/external-dns \
  --namespace external-dns -f values.yaml
```

Try it with `extraArgs.dry-run: true` first. external-dns then logs what it
would change without changing anything.

### Configuration

All settings are environment variables on the webhook container.

| Variable | Required | Notes |
|---|---|---|
| `COMBELL_API_KEY` | yes | Combell API key |
| `COMBELL_API_SECRET` | yes | Combell API secret |
| `DOMAIN_FILTER` | no | Comma separated list of domains to manage, for example `example.com,example.be`. When empty, every domain in the Combell account is managed |

The webhook listens on `127.0.0.1:8888` for external-dns and serves `/healthz`
on port `8080` for the probes the chart already defines.

## How it behaves

- Only `A`, `AAAA`, `CNAME` and `TXT` records are read or changed. These are the
  types external-dns manages by default, plus `TXT` for the ownership records it
  uses to know which records it created. Every other record in the zone is left
  alone.
- Combell stores one record per value. An external-dns entry with several values
  (for example two IP addresses) becomes several Combell records with the same
  name, and is read back the same way.
- A record name belongs to the longest matching domain in the account, so
  `app.sub.example.com` goes to `sub.example.com` if both domains exist.
- TTLs are kept between 60 and 86400 seconds, which is what Combell accepts.
  Records without a TTL get 3600.

## Things not to change without reading first

- **TXT values are stored without surrounding quotes.** external-dns sends its
  ownership values wrapped in double quotes. The webhook removes them before
  writing, and compares values without them when deleting or updating. Without
  this, deletes would not find the record they are meant to remove.

- **The request path is part of the signed string.** Combell's documentation says
  the path is lowercased before signing, their own SDK does not lowercase it, and
  the two disagree. This code keeps every path lowercase so both readings produce
  the same signature, and filters records locally instead of relying on query
  parameters. Changing that brings back random 401 errors that look like bad
  credentials.

- **Records are deleted by id, matched on name, type and value.** Deleting every
  record at a name would also remove values external-dns does not own.

- **The external-dns timeouts in the example values matter.** By default
  external-dns waits 15 seconds in total for the webhook. Applying changes makes
  one Combell call per record, which can take longer on a large change, and
  external-dns then retries changes that may already be half applied.

## Verifying by hand

```bash
kubectl -n external-dns logs deploy/external-dns -c webhook
kubectl -n external-dns logs deploy/external-dns -c external-dns
```

A `401` in the webhook logs is almost always the IP whitelist.

To run it locally against a real account:

```bash
COMBELL_API_KEY=... COMBELL_API_SECRET=... DOMAIN_FILTER=example.com go run .
```

```bash
curl -s -H 'Accept: application/external.dns.webhook+json;version=1' localhost:8888/
```

```bash
curl -s -H 'Accept: application/external.dns.webhook+json;version=1' localhost:8888/records
```

```bash
curl -s localhost:8080/healthz
```

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

The tests cover the HMAC signing, the Combell client against a fake server, and
the conversion between external-dns changes and Combell records against a fake
client. None of them need credentials.

Releases are made by pushing a tag like `v0.1.0`, which publishes
`ghcr.io/vilibazmio/external-dns-combell-webhook:0.1.0`.

## Credits

The Combell client is shared with
[cert-manager-combell-webhook](https://github.com/vilibazmio/cert-manager-combell-webhook).
The HMAC signing follows Combell's own
[PHP SDK](https://github.com/combell/combell-api), which is the reference for two
details the documentation leaves unclear: the secret is used as raw bytes and
never base64 decoded, and the path is encoded with PHP's `urlencode` rules rather
than RFC 3986.
