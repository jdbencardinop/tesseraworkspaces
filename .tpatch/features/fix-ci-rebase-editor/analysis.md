# Analysis

The v1.2.3 GitHub Actions failures occur only in two checkout sync integration tests. Both invoke `git rebase --continue` through the shared gitRunCS helper. CI has no editor and a dumb terminal, so Git cannot reuse the existing commit message. Setting GIT_EDITOR=true (and GIT_SEQUENCE_EDITOR=true defensively) in the test helper makes the tests noninteractive without changing production behavior.
