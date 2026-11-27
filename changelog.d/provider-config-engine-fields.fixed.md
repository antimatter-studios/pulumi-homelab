- **The Go provider accepts the engine's `version` field in its configuration.** An explicit `homelab`
  provider failed its config check with "Unrecognized field 'version'", because the engine adds that
  field (and `__`-prefixed internals) to every provider's inputs.
