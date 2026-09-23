# Tests

Pytest modules for the factory tools, run by `just packages-test` and by
`validate-packages.yml` on every change under `packages/`.

`test_factory.py` asserts the contract and allow-list load, every entry has
exactly one spec, the layout cross-check is clean, and every spec's local
sources resolve. A broken allow-list or a spec referencing a missing patch fails
the PR before any image or package build.
