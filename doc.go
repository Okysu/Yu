// Package yu implements a type-driven HTTP framework.
//
// Skills are plain Go functions. At startup the engine reflects their
// parameter and return types and compiles them into a DAG: skills in the same
// wave run concurrently, and skills in later waves consume values produced by
// earlier waves. Requests are frozen into immutable values, singletons are
// injected by type, and same-key writes can be serialized through a sharded
// scheduler.
//
// See README.md for the design rationale and the full list of compile-time
// rules.
package yu
