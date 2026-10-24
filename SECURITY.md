# Security policy

edge-mcp holds Kubernetes credentials and can change cluster state when `--allow-writes` is set, so please report vulnerabilities privately.

## Reporting

Use [GitHub private vulnerability reporting](https://github.com/xudipta/edge-mcp/security/advisories/new). Please do not open a public issue for a suspected vulnerability.

Include the version (`edge-mcp --version`), how you run it (stdio or HTTP, flags), and steps to reproduce. You can expect an acknowledgement within a few days.

## Scope

In scope: bypasses of the read-only default, the namespace allowlist, dry-run or the replica cap; authentication problems in HTTP mode; leaking secrets through tool output.

Out of scope: access that the Kubernetes service account or kubeconfig you gave edge-mcp already grants. Run it with the least-privileged RBAC (`deploy/rbac-readonly.yaml`) that fits your use.

## Supported versions

Only the latest release receives security fixes.
