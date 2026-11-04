<p>
  <div align="center">
    <img src="docs/images/logo-light.svg#gh-light-mode-only" alt="Gogs Plus">
    <img src="docs/images/logo-dark.svg#gh-dark-mode-only" alt="Gogs Plus">
  </div>
  <div align="center">
    <strong>A fork of Gogs with sign-in protection and a portable Windows build.</strong>
  </div>
</p>

## What this is

`gogs-plus` is a fork of [Gogs](https://github.com/gogs/gogs), the painless self-hosted Git service written in Go. Upstream provides the application. This fork adds a set of hardening changes on top, and tracks upstream `main` so fixes and features keep flowing in.

If you want the original project, use [gogs/gogs](https://github.com/gogs/gogs). This repository is maintained independently and is not affiliated with the upstream maintainers.

## Features

Everything upstream Gogs provides, plus the additions listed below.

Upstream functionality:

- User dashboard, profiles, and activity timeline.
- Repositories over SSH, HTTP, and HTTPS.
- User, organization, and repository management with teams and collaboration.
- Issues, pull requests, wiki, protected branches, releases, and labels.
- Repository and organization webhooks, including Slack, Discord, and Dingtalk.
- Git hooks, deploy keys, and Git LFS.
- Repository migration and mirroring from other hosts.
- Web editor for repository files and wiki pages.
- Jupyter Notebook and PDF rendering.
- Authentication through SMTP, LDAP, PAM, reverse proxy, and GitHub with 2FA.
- PostgreSQL, MySQL, MariaDB, and SQLite3 backends.
- Localization for 30+ languages.

Added in this fork:

- **Sign-in rate limiting.** Counts failed sign-in attempts per client IP and optionally bans the address for a configurable window.
- **Account lockout.** Optionally locks an account after repeated failures, independent of the IP limit.
- **Sign-in captcha**, enabled by default.
- **Coverage beyond the web login.** The same counters and bans apply to Git HTTP Basic authentication, Git LFS, and the shared Macaron Basic auth path, so cloning over HTTP cannot be used to sidestep the limit.
- **File-backed bans.** Bans live in a JSON file rather than memory, so they survive restarts, can be inspected and hand-edited, and take effect on the next request without a restart.
- **Ban management page** at `/admin/login-bans`, showing the file location, current bans, the last account attempted from each banned IP, and buttons to unban or add bans.
- **Portable Windows package** with vendored configuration and a launcher, built from this tree.

Details of the design, threat model, and trade-offs are in [docs/dev/login-protection.md](docs/dev/login-protection.md).

## Sign-in protection

Configuration lives under `[security]` in `app.ini`:

| Option | Default | Meaning |
| --- | --- | --- |
| `ENABLE_LOGIN_CAPTCHA` | `true` | Show a captcha on the sign-in page. |
| `ENABLE_LOGIN_RATE_LIMIT` | `false` | Ban a client IP after repeated failures. |
| `LOGIN_RATE_LIMIT_ATTEMPTS` | `10` | Failures allowed per IP within the window. |
| `LOGIN_FAILURE_WINDOW` | `5m` | Counting window for both IP and account failures. |
| `LOGIN_RATE_LIMIT_BLOCK_DURATION` | `15m` | How long an IP stays banned. |
| `ENABLE_LOGIN_LOCKOUT` | `false` | Lock an account after repeated failures. |
| `LOGIN_LOCKOUT_ATTEMPTS` | `10` | Failures allowed per account within the window. |
| `LOGIN_LOCKOUT_DURATION` | `15m` | How long an account stays locked. |
| `LOGIN_BAN_FILE` | `data/login_bans.json` | Where bans are stored. |

Client IPs are taken from the connection by default. `X-Forwarded-For` and `X-Real-IP` are honored only when the immediate peer is listed in `[auth] TRUSTED_PROXY_IPS`, and the rightmost untrusted hop wins, so a directly connected client cannot spoof its address.

Bans apply to the web sign-in form, Git HTTP Basic authentication, and Git LFS. Access tokens are never counted as failures: a client sending a token in the password field fails the password check and then succeeds by token, which must not accumulate toward a ban.

## Quick start on Windows

The `dist` folder in a local build is a self-contained package. See `dist/README.txt` for the full notes.

```powershell
cd dist
.\gogs.exe admin create-user --name yourname --password "a strong password" --email you@example.com --admin
.\start.bat
```

Then open <http://localhost:3000/>. The first account created becomes the site administrator.

`admin`, `user`, `api`, `repo`, and several other words are reserved usernames and are rejected by `create-user`.

## Build from source

Requirements:

- Go 1.27 or newer.
- Node.js 24 and pnpm 11 for the web frontend.
- Git on `PATH`.
- A C compiler only if you build with the `pam` tag.

```bash
# Install dependencies and generate mocks.
go mod tidy
go generate ./...

# Build the frontend into public/dist.
pnpm install --frozen-lockfile
pnpm --filter gogs-web run build

# Build the server with the frontend embedded.
go build -tags prod -trimpath -o gogs ./cmd/gogs
```

The `prod` build tag embeds `public/dist` into the binary. Without it, the server expects a Vite dev server for frontend assets.

Tasks are also available through [moon](https://moonrepo.dev/):

```bash
moon run gogs:build-prod
moon run gogs:lint
moon run web:lint
moon run web:build
```

Note that `moon run gogs:lint` runs `go generate` first through its dependency chain. On this tree the regenerated mocks differ from the committed ones, which trips an import-cycle typecheck error. If you hit it, run `go generate ./...`, restore the two generated mock files, then run `golangci-lint run` directly.

## Documentation

- [Design and implementation notes for sign-in protection](docs/dev/login-protection.md)
- [Configuration primer](docs/fine-tuning/configuration-primer.mdx)
- [Reverse proxy setup](docs/fine-tuning/reverse-proxy.mdx)
- [Authentication backends](docs/advancing/authentication.mdx)
- [Local development](docs/dev/local_development.md)
- [CHANGELOG.md](CHANGELOG.md) for what changed in each release
- Upstream user documentation at [gogs.io](https://gogs.io)

## Disclaimer

This software is provided "as is", without warranty of any kind, express or implied, including but not limited to the warranties of merchantability, fitness for a particular purpose, and noninfringement. In no event shall the authors or copyright holders be liable for any claim, damages, or other liability, whether in an action of contract, tort, or otherwise, arising from, out of, or in connection with the software or the use or other dealings in the software.

Additional points specific to this fork:

- **Not affiliated with upstream.** This is an independent fork. Do not report bugs in this fork to the upstream Gogs project, and do not assume upstream has reviewed or endorsed these changes.
- **Security software is not a guarantee.** The sign-in protection here reduces brute-force exposure but does not make a deployment secure on its own. It does not replace TLS, patching, access control, backups, or monitoring.
- **You own your deployment.** Running a public Git service means you are responsible for the data on it, the accounts you issue, and compliance with any law or policy that applies to you.
- **Defaults may change.** Options, defaults, storage formats, and behavior may change between commits on `main`. Read `CHANGELOG.md` before upgrading, and keep a backup of `data/` and `custom/`.
- **Third-party components** carry their own licenses. Review them before redistribution.

Use at your own risk. If any of the above is not acceptable, do not use this software.

## Copyright and licensing

Copyright (c) The Gogs Authors.
Copyright (c) 2026 the gogs-plus contributors for changes made in this fork.

This project is distributed under the MIT License. See [LICENSE](LICENSE) for the full text.

The MIT License permits use, modification, and redistribution provided the copyright notice and permission notice are retained. It provides no warranty. This fork retains the original copyright notice as required. All upstream work remains the property of its respective authors.

The Gogs name and logo belong to the upstream project. This fork uses them to identify the software's origin and does not claim ownership.

## Contributing

- Read [AGENTS.md](AGENTS.md) for the conventions this fork follows.
- Run `moon run gogs:lint` and `moon run web:lint` before opening a pull request.
- Keep changes to `conf/locale/locale_en-US.ini` only. Other locale files are community-maintained.
- For security issues, read [SECURITY.md](SECURITY.md) first and report privately rather than in a public issue.

## Acknowledgments

Gogs would not exist without its upstream authors and contributors. See the [upstream contributors page](https://github.com/gogs/gogs/graphs/contributors) and the upstream `TRANSLATORS` file.

- Thanks [Egon Elbre](https://twitter.com/egonelbre) for designing the original logo.
- Thanks [Mintlify](https://mintlify.com), [Crowdin](https://crowdin.com), and [Buildkite](https://buildkite.com) for sponsoring the upstream documentation, translation, and CI plans.
