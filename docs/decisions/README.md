# Architectural decisions

This index is generated from the ADR frontmatter — do not edit by hand.
Load the ADR(s) whose filename matches the area you are touching.

## Index

### Boundary

- [0001 — Forward only two 4CAT API routes; 403 the rest](./0001-forward-only-the-two-4cat-upload-routes-403-the-rest.md)
- [0006 — Publish one Compose port: the gated listener on loopback](./0006-publish-one-compose-port-the-gated-listener-on-loopback.md)
- [0007 — Route a Firefox only through its own proxy](./0007-route-a-firefox-only-through-its-own-proxy.md)
- [0009 — Mount and inject a 4CAT token only for its own collector](./0009-mount-and-inject-a-4cat-token-only-for-its-own-collector.md)

### Access

- [0002 — Treat SRC-Nginx as the sole authenticated ingress](./0002-treat-src-nginx-as-the-sole-authenticated-ingress.md)
- [0003 — Check the gate secret before trusting identity headers](./0003-check-the-gate-secret-before-trusting-identity-headers.md)
- [0004 — Match roles as whole segments, never substrings](./0004-match-roles-as-whole-segments-never-substrings.md)
- [0005 — Revoke only on a successful poll that omits the user](./0005-revoke-only-on-a-successful-poll-that-omits-the-user.md)
- [0008 — Relay a screen only to its authenticated owner](./0008-relay-a-screen-only-to-its-authenticated-owner.md)

### Capture

- [0010 — Store the raw upstream object in every safety-copy record](./0010-store-the-raw-upstream-object-in-every-safety-copy-record.md)

### Browser

- [0011 — Build every collector profile from a verified release template](./0011-build-every-collector-profile-from-a-verified-release-template.md)
- [0012 — Allowlist-sanitise a template and verify it holds no data](./0012-allowlist-sanitise-a-template-and-verify-it-holds-no-data.md)
- [0013 — Give Firefox 0644 copies on the volume, never the repo copy](./0013-give-firefox-0644-copies-on-the-volume-never-the-repo-copy.md)
- [0022 — Lock network.manage-offline-status off in the collector policy](./0022-lock-network-manage-offline-status-off-in-the-collector-policy.md)

### Provisioning

- [0014 — Provision idempotently from the per-user step file](./0014-provision-idempotently-from-the-per-user-step-file.md)
- [0015 — Reserve the running-session cap atomically](./0015-reserve-the-running-session-cap-atomically.md)
- [0016 — Reattach by preserving data and regenerating workspace state](./0016-reattach-by-preserving-data-and-regenerating-workspace-state.md)
- [0021 — Create a collector's pair at start and remove it at stop](./0021-create-a-collector-s-pair-at-start-and-remove-it-at-stop.md)

### Deploy

- [0017 — Keep init.sh to validation, files and pulls; start nothing](./0017-keep-init-sh-to-validation-files-and-pulls-start-nothing.md)
- [0018 — Never modify or reload nginx from the running desk](./0018-never-modify-or-reload-nginx-from-the-running-desk.md)
- [0019 — Ship no Python in the product tree](./0019-ship-no-python-in-the-product-tree.md)
- [0020 — Take every image and template pin from the checked-out release, never from the catalog item](./0020-take-every-image-and-template-pin-from-the-checked-out-release-never-from-the-catalog-item.md)
