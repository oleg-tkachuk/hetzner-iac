# Architecture Decision Records

Numbered records of the decisions this repository is built on: what was
decided, what it gives, and the consequences accepted. They describe the
current state and are the starting point for whatever changes it.

| ADR | Decision | Status |
|-----|----------|--------|
| [0001](0001-provisioning-tool.md) | Provisioning with the Pulumi Go SDK | Accepted |
| [0002](0002-workload-runtime.md) | Talos Linux, with Kubernetes pinned in the topology | Accepted |
| [0003](0003-state-and-secrets.md) | Pulumi Cloud state; Pulumi's own encryption for secrets, SOPS + age when one lands outside it | Accepted |
| [0004](0004-repository-structure.md) | A Pulumi project per concern; an environment is a stack, and `stack=` has no default | Accepted |
| [0005](0005-internal-packages.md) | The implementation is `internal/pkg/`; the tags are tree releases, not module versions | Accepted |
