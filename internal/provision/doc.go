// Package provision advances a collector's first visit toward "ready" in
// idempotent steps recorded in state/users/<user>/provision.json (ADR-0014):
// registry, dirs, template, fourcat_account, fourcat_token, render. A crash
// or an error leaves a file that says exactly where to resume, and a second
// visit, a double-click or a desk restart never repeats a done step. Starting
// the containers is not a step: the page asks the session manager once the
// state is ready.
//
// Locking: Advance holds the user's lock for the whole run; the registry step
// takes the global lock inside it. The order is always user lock, then global
// lock, never the reverse.
//
// Secrets: a 4CAT password or token never reaches provision.json or an error
// text. Known errors are recorded verbatim; registry.ErrFull becomes exactly
// "error: registry full"; any other error is recorded by its Go type name
// only, never its message.
package provision
