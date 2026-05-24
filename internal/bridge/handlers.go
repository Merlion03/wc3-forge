package bridge

// Handlers live in the forge package (the domain layer) because Phase 0's
// stub bridge.ping was extended in Phase 1 to report map state, and the
// bridge package stays domain-free.
//
// See internal/forge/handlers.go for the registered set.
