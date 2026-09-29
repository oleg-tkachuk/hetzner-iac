# stackstatus

The report behind `task platform:status`: the types a stack is read into, the
judgements about which rows need attention, and the table.

Pulumi-free on purpose. [tools/stackstatus](../../../tools/stackstatus) does
the reading through the Automation API and maps it onto these types, so what
is worth testing — a reference to another environment's cluster, a consumer
applied against a contract the cluster no longer publishes, an update that was
interrupted — is tested without a backend, and the package costs nothing to
import.

The marks are the repository's own — the glyphs Taskfile.yaml's `_OK`, `_RUN`,
`_WARN`, `_SKIP` and `_ERR` print.

Held equal to them by `TestStatusMarks_MatchTheTaskfile` in internal/ci, since this
package cannot import pulumilog to share them.
