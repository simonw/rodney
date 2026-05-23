# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-05-06

Initial release of the Battle-Creek-LLC fork of [simonw/rodney](https://github.com/simonw/rodney).

### Added
- Forked upstream `simonw/rodney` and merged through commit `8325836`.
- Use system-installed Chrome before falling back to auto-download (#2).
- Crash-loop detection via state ring buffer.
- Contextual error suggestions for common failures.
- `--text` and `--gone` options on the `wait` command.
- `--stealth` flag to remove automation fingerprints.
- `--forms`, `--links`, `--interactive` modes on `discover`.
- Accessibility selectors (`--role`, `--name`) on action commands.
- Composable `check` command for batched assertions.
- Upstream-comparison helper script and README section.
- Repository-policy baseline via repocat (branch protection, signed commits,
  secret scanning, Dependabot, dependency-review workflow, pinned action SHAs).

### Fixed
- Chrome launch crashes on macOS desktop.

[Unreleased]: https://github.com/Battle-Creek-LLC/rodney/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Battle-Creek-LLC/rodney/releases/tag/v0.1.0
