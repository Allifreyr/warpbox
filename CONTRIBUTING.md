# Contributing

Bug fixes are welcome. Feature requests and feature PRs are not currently
being sought — open a discussion first if you're unsure.

## Before you start

Always open an issue and get maintainer approval before submitting a PR.
This avoids wasted effort on something that won't be merged.

## Reporting bugs

Use the bug report template when filing an issue. Include:
- Warpbox version (`warpbox --version`)
- Your OS and architecture
- Steps to reproduce
- Relevant logs from `/logs/`

## Development Setup

1. **Prerequisites:**
   - Go 1.26+
   - MinGW-w64 GCC (Windows) or GCC (Linux/macOS) — required by the cgo-based SQLite driver
   - A TorBox API key for testing

2. **Clone and build:**
    ```bash
    git clone https://github.com/mainlink0435/warpbox.git
    cd warpbox
    CGO_ENABLED=1 go build -o warpbox ./cmd/warpbox/
    ```

3. **Run tests:**
   ```bash
   CGO_ENABLED=1 go test ./... -count=1 -timeout 120s
   ```

4. **Local testing:**
   ```bash
   ./warpbox -config config.yml -db warpbox.db
   ```
   Then browse to `http://localhost:1412/` and `http://localhost:1412/http/`.

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`).
- Use [conventional commits](https://www.conventionalcommits.org/) for commit messages (`feat:`, `fix:`, `refactor:`, `docs:`, `chore:`).
- Include `refs #N` in commit messages to reference issues.
- Run `go vet ./...` before committing.

## Pull Request Process

1. Open an issue describing the bug.
2. Fork and create a feature branch from `main`.
3. Make your changes, ensure all tests pass.
4. Open a pull request against `main`, referencing the issue.

## License

By contributing, you agree that your contributions will be licensed under the GNU General Public License v3.0.